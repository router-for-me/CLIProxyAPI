/**
 * @file Cline executor implementation for API requests
 * @description Stateless executor for Cline API using OpenAI-compatible chat completions.
 * Handles both streaming and non-streaming requests, automatic token refresh, and usage tracking.
 * Cline API uses JWT tokens with "workos:" prefix for authentication and supports various AI models
 * including Minimax, Grok, Claude, and OpenRouter models through a unified interface.
 */

package executor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	clineauth "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/cline"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	log "github.com/sirupsen/logrus"
)

const (
	clineAPIBaseURL = "https://api.cline.bot"
)

// ClineExecutor is a stateless executor for Cline API using OpenAI-compatible chat completions.
type ClineExecutor struct {
	cfg *config.Config
}

// NewClineExecutor creates a new Cline executor instance.
func NewClineExecutor(cfg *config.Config) *ClineExecutor {
	return &ClineExecutor{cfg: cfg}
}

// Identifier returns the provider identifier for this executor.
func (e *ClineExecutor) Identifier() string {
	return "cline"
}

// PrepareRequest prepares the HTTP request with necessary headers and authentication.
func (e *ClineExecutor) PrepareRequest(_ *http.Request, _ *cliproxyauth.Auth) error {
	return nil
}

// Execute performs a non-streaming request to Cline API.
func (e *ClineExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	token, baseURL := clineCredentials(auth)
	if token == "" {
		return resp, fmt.Errorf("cline access token not available")
	}

	if baseURL == "" {
		baseURL = clineAPIBaseURL
	}

	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")

	// Translate request to OpenAI format
	body := sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), false)
	body = applyPayloadConfig(e.cfg, req.Model, body)

	url := strings.TrimSuffix(baseURL, "/") + "/api/v1/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return resp, err
	}

	e.applyClineHeaders(httpReq, token, false)

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
			log.Errorf("cline executor: close response body error: %v", errClose)
		}
	}()

	recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		appendAPIResponseChunk(ctx, e.cfg, b)
		log.Debugf("cline request error, status: %d, body: %s", httpResp.StatusCode, summarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
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

	// Cline executor uses ONLY new translator (no fallback to old translator)
	translatedResp, errTranslate := TranslateOpenAIResponseNonStreamForced(from, data, req.Model)
	if errTranslate != nil {
		return resp, fmt.Errorf("cline executor: translation failed: %w", errTranslate)
	}
	if translatedResp == nil {
		return resp, fmt.Errorf("cline executor: translation returned nil")
	}
	resp = cliproxyexecutor.Response{Payload: translatedResp}
	return resp, nil
}

// ExecuteStream performs a streaming request to Cline API.
func (e *ClineExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (stream <-chan cliproxyexecutor.StreamChunk, err error) {
	token, baseURL := clineCredentials(auth)
	if token == "" {
		return nil, fmt.Errorf("cline access token not available")
	}

	if baseURL == "" {
		baseURL = clineAPIBaseURL
	}

	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")

	// Translate request to OpenAI format
	body := sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), true)
	body = applyPayloadConfig(e.cfg, req.Model, body)

	url := strings.TrimSuffix(baseURL, "/") + "/api/v1/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	e.applyClineHeaders(httpReq, token, true)

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
		log.Debugf("cline stream request error, status: %d, body: %s", httpResp.StatusCode, summarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("cline executor: close response body error: %v", errClose)
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
				log.Errorf("cline executor: close response body error: %v", errClose)
			}
		}()

		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 52_428_800) // 50MB

		// State for new translator (tracks reasoning tokens)
		streamState := &OpenAIStreamState{}
		messageID := "chatcmpl-" + req.Model

		// Cline executor uses ONLY new translator (no fallback)
		firstChunk := true
		for scanner.Scan() {
			line := scanner.Bytes()
			appendAPIResponseChunk(ctx, e.cfg, line)

			if detail, ok := parseOpenAIStreamUsage(line); ok {
				reporter.publish(ctx, detail)
			}

			payload := line
			if bytes.HasPrefix(line, []byte("data: ")) {
				payload = bytes.TrimSpace(line[6:])
			} else if bytes.HasPrefix(line, []byte("data:")) {
				payload = bytes.TrimSpace(line[5:])
			}

			if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
				continue
			}

			payload = convertClineReasoningToOpenAI(payload, firstChunk)

			if !firstChunk && shouldSkipEmptyContentChunk(payload) {
				continue
			}

			firstChunk = false

			// Use new translator (re-add "data: " prefix for parser)
			lineWithPrefix := append([]byte("data: "), payload...)
			translatedChunks, errTranslate := TranslateOpenAIResponseStreamForced(from, lineWithPrefix, req.Model, messageID, streamState)
			if errTranslate == nil && translatedChunks != nil {
				for _, chunk := range translatedChunks {
					out <- cliproxyexecutor.StreamChunk{Payload: chunk}
				}
			}
		}
		if errScan := scanner.Err(); errScan != nil {
			recordAPIResponseError(ctx, e.cfg, errScan)
			reporter.publishFailure(ctx)
			out <- cliproxyexecutor.StreamChunk{Err: errScan}
		}
	}()

	return stream, nil
}

// clineCredentials extracts access token and base URL from auth metadata.
func clineCredentials(a *cliproxyauth.Auth) (token, baseURL string) {
	if a == nil {
		return "", ""
	}
	if a.Attributes != nil {
		if v := a.Attributes["api_key"]; v != "" {
			token = v
		}
		if v := a.Attributes["base_url"]; v != "" {
			baseURL = v
		}
	}
	if token == "" && a.Metadata != nil {
		if v, ok := a.Metadata["access_token"].(string); ok {
			token = v
		}
	}
	if token != "" {
		token = "workos:" + token
	}
	return token, baseURL
}

// applyClineHeaders applies necessary headers for Cline API requests.
func (e *ClineExecutor) applyClineHeaders(req *http.Request, token string, stream bool) {
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	if stream {
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("Cache-Control", "no-cache")
		req.Header.Set("Connection", "keep-alive")
	}
}

// convertClineReasoningToOpenAI converts Cline API's "reasoning" field to OpenAI's "reasoning_content" field
// and removes "role" from non-first chunks to comply with OpenAI streaming standard.
func convertClineReasoningToOpenAI(payload []byte, isFirstChunk bool) []byte {
	if bytes.Contains(payload, []byte(`"reasoning":`)) && !bytes.Contains(payload, []byte(`"reasoning_content":`)) {
		payload = bytes.ReplaceAll(payload, []byte(`"reasoning":`), []byte(`"reasoning_content":`))
	}

	if !isFirstChunk && bytes.Contains(payload, []byte(`"role":"assistant"`)) {
		payload = bytes.ReplaceAll(payload, []byte(`"role":"assistant",`), []byte{})
		payload = bytes.ReplaceAll(payload, []byte(`,"role":"assistant"`), []byte{})
	}

	return payload
}

// shouldSkipEmptyContentChunk determines if a chunk with empty/null content should be skipped.
func shouldSkipEmptyContentChunk(payload []byte) bool {
	hasEmptyContent := bytes.Contains(payload, []byte(`"content":""`)) ||
		bytes.Contains(payload, []byte(`"content":null`))

	if !hasEmptyContent {
		return false
	}

	if bytes.Contains(payload, []byte(`"tool_calls"`)) {
		return false
	}

	if bytes.Contains(payload, []byte(`"finish_reason"`)) {
		return false
	}

	if bytes.Contains(payload, []byte(`"usage"`)) {
		return false
	}

	return true
}

// CountTokens counts tokens in the request for Cline models.
func (e *ClineExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	body := sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), false)

	modelName := req.Model
	if bodyModel := string(body); strings.Contains(bodyModel, `"model"`) {
		start := strings.Index(bodyModel, `"model"`)
		if start != -1 {
			start = strings.Index(bodyModel[start:], `":`) + start + 2
			end := strings.Index(bodyModel[start:], `"`) + start
			if end > start {
				modelName = strings.Trim(bodyModel[start:end], `"`)
			}
		}
	}

	enc, err := tokenizerForModel(modelName)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("cline executor: tokenizer init failed: %w", err)
	}

	count, err := countOpenAIChatTokens(enc, body)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("cline executor: token counting failed: %w", err)
	}

	usageJSON := buildOpenAIUsageJSON(count)
	translated := sdktranslator.TranslateTokenCount(ctx, to, from, count, usageJSON)
	return cliproxyexecutor.Response{Payload: []byte(translated)}, nil
}

// Refresh refreshes the Cline authentication tokens.
func (e *ClineExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("cline executor: refresh called")
	if auth == nil {
		return nil, fmt.Errorf("cline executor: auth is nil")
	}
	var refreshToken string
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["refresh_token"].(string); ok && v != "" {
			refreshToken = v
		}
	}
	if refreshToken == "" {
		return auth, nil
	}
	svc := clineauth.NewClineAuth(e.cfg)
	td, err := svc.RefreshTokens(ctx, refreshToken)
	if err != nil {
		return nil, err
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["access_token"] = td.AccessToken
	if td.RefreshToken != "" {
		auth.Metadata["refresh_token"] = td.RefreshToken
	}
	if td.Email != "" {
		auth.Metadata["email"] = td.Email
	}
	auth.Metadata["expired"] = td.Expire
	auth.Metadata["type"] = "cline"
	now := time.Now().Format(time.RFC3339)
	auth.Metadata["last_refresh"] = now
	return auth, nil
}
