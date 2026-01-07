package executor

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	copilotauth "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/copilot"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/sjson"
)

// CopilotExecutor is a stateless executor for GitHub Copilot using OpenAI-compatible chat completions.
type CopilotExecutor struct {
	cfg *config.Config
}

func NewCopilotExecutor(cfg *config.Config) *CopilotExecutor { return &CopilotExecutor{cfg: cfg} }

func (e *CopilotExecutor) Identifier() string { return "copilot" }

func (e *CopilotExecutor) PrepareRequest(_ *http.Request, _ *cliproxyauth.Auth) error { return nil }

func (e *CopilotExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	token, baseURL, err := e.ensureValidToken(ctx, auth)
	if err != nil {
		return resp, fmt.Errorf("failed to get valid Copilot token: %w", err)
	}

	if baseURL == "" {
		baseURL = "https://api.individual.githubcopilot.com"
	}
	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	originalPayload := bytes.Clone(req.Payload)
	if len(opts.OriginalRequest) > 0 {
		originalPayload = bytes.Clone(opts.OriginalRequest)
	}
	originalTranslated := sdktranslator.TranslateRequest(from, to, req.Model, originalPayload, false)
	body := sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), false)
	body = ApplyReasoningEffortMetadata(body, req.Metadata, req.Model, "reasoning_effort", false)
	body, _ = sjson.SetBytes(body, "model", req.Model)
	body = NormalizeThinkingConfig(body, req.Model, false)
	if errValidate := ValidateThinkingConfig(body, req.Model); errValidate != nil {
		return resp, errValidate
	}
	body = applyPayloadConfigWithRoot(e.cfg, req.Model, to.String(), "", body, originalTranslated)

	url := strings.TrimSuffix(baseURL, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return resp, err
	}
	applyCopilotHeaders(httpReq, token)
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	recordAPIRequest(ctx, e.cfg, upstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      body,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("copilot executor: close response body error: %v", errClose)
		}
	}()
	recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		appendAPIResponseChunk(ctx, e.cfg, b)
		log.Debugf("request error, error status: %d, error body: %s", httpResp.StatusCode, summarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return resp, err
	}
	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	appendAPIResponseChunk(ctx, e.cfg, data)
	reporter.publish(ctx, parseOpenAIUsage(data))
	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, bytes.Clone(opts.OriginalRequest), body, data, &param)
	resp = cliproxyexecutor.Response{Payload: []byte(out)}
	return resp, nil
}

func (e *CopilotExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (stream <-chan cliproxyexecutor.StreamChunk, err error) {
	token, baseURL, err := e.ensureValidToken(ctx, auth)
	if err != nil {
		return nil, fmt.Errorf("failed to get valid Copilot token: %w", err)
	}

	if baseURL == "" {
		baseURL = "https://api.individual.githubcopilot.com"
	}
	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	originalPayload := bytes.Clone(req.Payload)
	if len(opts.OriginalRequest) > 0 {
		originalPayload = bytes.Clone(opts.OriginalRequest)
	}
	originalTranslated := sdktranslator.TranslateRequest(from, to, req.Model, originalPayload, true)
	body := sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), true)

	body = ApplyReasoningEffortMetadata(body, req.Metadata, req.Model, "reasoning_effort", false)
	body, _ = sjson.SetBytes(body, "model", req.Model)
	body = NormalizeThinkingConfig(body, req.Model, false)
	if errValidate := ValidateThinkingConfig(body, req.Model); errValidate != nil {
		return nil, errValidate
	}
	body, _ = sjson.SetBytes(body, "stream_options.include_usage", true)
	body = applyPayloadConfigWithRoot(e.cfg, req.Model, to.String(), "", body, originalTranslated)

	url := strings.TrimSuffix(baseURL, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	applyCopilotHeaders(httpReq, token)
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	recordAPIRequest(ctx, e.cfg, upstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   httpReq.Header.Clone(),
		Body:      body,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		appendAPIResponseChunk(ctx, e.cfg, b)
		log.Debugf("request error, error status: %d, error body: %s", httpResp.StatusCode, summarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("copilot executor: close response body error: %v", errClose)
		}
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return nil, err
	}
	out := make(chan cliproxyexecutor.StreamChunk)
	stream = out
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("copilot executor: close stream response body error: %v", errClose)
			}
		}()
		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 52_428_800) // 50MB
		var param any
		for scanner.Scan() {
			line := scanner.Bytes()
			appendAPIResponseChunk(ctx, e.cfg, line)
			if detail, ok := parseOpenAIStreamUsage(line); ok {
				reporter.publish(ctx, detail)
			}
			chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, bytes.Clone(opts.OriginalRequest), body, bytes.Clone(line), &param)
			for i := range chunks {
				out <- cliproxyexecutor.StreamChunk{Payload: []byte(chunks[i])}
			}
		}
		doneChunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, bytes.Clone(opts.OriginalRequest), body, bytes.Clone([]byte("[DONE]")), &param)
		for i := range doneChunks {
			out <- cliproxyexecutor.StreamChunk{Payload: []byte(doneChunks[i])}
		}
		if errScan := scanner.Err(); errScan != nil {
			recordAPIResponseError(ctx, e.cfg, errScan)
			reporter.publishFailure(ctx)
			out <- cliproxyexecutor.StreamChunk{Err: errScan}
		}
	}()
	return stream, nil
}

// ensureValidToken retrieves and refreshes Copilot token if needed
func (e *CopilotExecutor) ensureValidToken(ctx context.Context, auth *cliproxyauth.Auth) (token, baseURL string, err error) {
	if auth == nil || auth.Metadata == nil {
		return "", "", fmt.Errorf("auth metadata is required")
	}

	// Get stored tokens
	githubToken, _ := auth.Metadata["github_token"].(string)
	copilotToken, _ := auth.Metadata["copilot_token"].(string)
	copilotExpire, _ := auth.Metadata["copilot_expire"].(string)
	copilotAPIBase, _ := auth.Metadata["copilot_api_base"].(string)

	if githubToken == "" {
		return "", "", fmt.Errorf("github_token not found in metadata")
	}

	// Check if Copilot token needs refresh
	needsRefresh := false
	if copilotToken == "" {
		needsRefresh = true
	} else if copilotExpire != "" {
		expireTime, parseErr := time.Parse(time.RFC3339, copilotExpire)
		if parseErr != nil {
			needsRefresh = true
		} else {
			// Refresh if token expires in less than 5 minutes
			if time.Until(expireTime) < 5*time.Minute {
				needsRefresh = true
			}
		}
	}

	if needsRefresh {
		log.Infof("Copilot token expired or missing, refreshing using GitHub token")
		copilotAuth := copilotauth.NewCopilotAuth(e.cfg)
		tokenData, refreshErr := copilotAuth.RefreshCopilotToken(ctx, githubToken)
		if refreshErr != nil {
			return "", "", fmt.Errorf("failed to refresh Copilot token: %w", refreshErr)
		}

		// Update auth metadata with new token
		auth.Metadata["copilot_token"] = tokenData.CopilotToken
		auth.Metadata["copilot_api_base"] = tokenData.CopilotAPIBase
		auth.Metadata["copilot_expire"] = tokenData.CopilotExpire
		auth.Metadata["last_refresh"] = time.Now().Format(time.RFC3339)
		if tokenData.SKU != "" {
			auth.Metadata["sku"] = tokenData.SKU
		}

		copilotToken = tokenData.CopilotToken
		copilotAPIBase = tokenData.CopilotAPIBase

		log.Infof("Copilot token refreshed successfully (expires: %s, sku: %s)", tokenData.CopilotExpire, tokenData.SKU)
	}

	return copilotToken, copilotAPIBase, nil
}

// applyCopilotHeaders adds required GitHub Copilot headers
func applyCopilotHeaders(req *http.Request, token string) {
	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "GitHubCopilotChat/1.0")
	req.Header.Set("Editor-Version", "vscode/1.85.0")
	req.Header.Set("Editor-Plugin-Version", "copilot-chat/0.11.1")
	req.Header.Set("Openai-Organization", "github-copilot")
	req.Header.Set("Openai-Intent", "conversation-panel")
	req.Header.Set("VScode-SessionId", randomHex(16))
	req.Header.Set("VScode-MachineId", randomHex(32))
}

// randomHex generates a random hex string of specified byte length
func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// Fallback to timestamp-based random on error
		return fmt.Sprintf("%032x", time.Now().UnixNano())[:n*2]
	}
	return hex.EncodeToString(b)
}

// Refresh refreshes the Copilot token using the stored GitHub token
func (e *CopilotExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("copilot executor: refresh called")
	if auth == nil {
		return nil, fmt.Errorf("copilot executor: auth is nil")
	}

	// Get GitHub token from metadata
	var githubToken string
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["github_token"].(string); ok && strings.TrimSpace(v) != "" {
			githubToken = v
		}
	}

	if githubToken == "" {
		return nil, fmt.Errorf("copilot executor: github_token not found in metadata")
	}

	log.Infof("copilot executor: refreshing Copilot token using GitHub token")
	copilotAuth := copilotauth.NewCopilotAuth(e.cfg)
	tokenData, err := copilotAuth.RefreshCopilotToken(ctx, githubToken)
	if err != nil {
		return nil, fmt.Errorf("copilot executor: failed to refresh Copilot token: %w", err)
	}

	// Update metadata with new token
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["copilot_token"] = tokenData.CopilotToken
	auth.Metadata["copilot_api_base"] = tokenData.CopilotAPIBase
	auth.Metadata["copilot_expire"] = tokenData.CopilotExpire
	auth.Metadata["last_refresh"] = time.Now().Format(time.RFC3339)
	if tokenData.SKU != "" {
		auth.Metadata["sku"] = tokenData.SKU
	}

	log.Infof("copilot executor: token refreshed successfully (expires: %s, sku: %s)", tokenData.CopilotExpire, tokenData.SKU)
	return auth, nil
}

// CountTokens returns the token count for the given request
func (e *CopilotExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	body := sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), false)

	modelName := req.Model
	if v := string(body); v != "" {
		if m := extractModelFromJSON(v); m != "" {
			modelName = m
		}
	}

	enc, err := tokenizerForModel(modelName)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("copilot executor: tokenizer init failed: %w", err)
	}

	count, err := countOpenAIChatTokens(enc, body)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("copilot executor: token count failed: %w", err)
	}

	response := fmt.Sprintf(`{"total_tokens":%d}`, count)
	return cliproxyexecutor.Response{Payload: []byte(response)}, nil
}

// extractModelFromJSON extracts the model field from JSON body
func extractModelFromJSON(jsonStr string) string {
	// Simple extraction without importing gjson
	if idx := strings.Index(jsonStr, `"model"`); idx != -1 {
		rest := jsonStr[idx+7:]
		if colon := strings.Index(rest, ":"); colon != -1 {
			rest = strings.TrimSpace(rest[colon+1:])
			if strings.HasPrefix(rest, `"`) {
				rest = rest[1:]
				if end := strings.Index(rest, `"`); end != -1 {
					return rest[:end]
				}
			}
		}
	}
	return ""
}
