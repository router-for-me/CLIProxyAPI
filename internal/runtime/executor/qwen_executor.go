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

	qwenauth "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/qwen"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/translator_new/from_ir"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/translator_new/to_ir"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const (
	qwenUserAgent           = "google-api-nodejs-client/9.15.1"
	qwenXGoogAPIClient      = "gl-node/22.17.0"
	qwenClientMetadataValue = "ideType=IDE_UNSPECIFIED,platform=PLATFORM_UNSPECIFIED,pluginType=GEMINI"
)

// QwenExecutor is a stateless executor for Qwen Code using OpenAI-compatible chat completions.
// If access token is unavailable, it falls back to legacy via ClientAdapter.
type QwenExecutor struct {
	cfg *config.Config
}

func NewQwenExecutor(cfg *config.Config) *QwenExecutor { return &QwenExecutor{cfg: cfg} }

func (e *QwenExecutor) Identifier() string { return "qwen" }

func (e *QwenExecutor) PrepareRequest(_ *http.Request, _ *cliproxyauth.Auth) error { return nil }

func (e *QwenExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	token, baseURL := qwenCreds(auth)

	if baseURL == "" {
		baseURL = "https://portal.qwen.ai/v1"
	}
	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)

	// --- CANONICAL IR TRANSLATION ---
	// 1. Parse incoming request to IR
	// Using ParseRequest with "openai" as source format (safe default for Qwen-compatible inputs)
	// If the actual source is different (e.g. from opts), ToIR will handle it if mapped, or we assume OpenAI-compatible.
	irReq, err := to_ir.ParseRequest(req.Payload, to_ir.ParserOptions{
		Format: opts.SourceFormat,
		Model:  req.Model,
	})
	if err != nil {
		return resp, fmt.Errorf("qwen executor: parse error: %w", err)
	}

	// 2. Apply metadata/config to IR
	// (Reasoning effort, model overrides, etc. should ideally be handled within ParseRequest or explicitly on IR)
	if irReq.Thinking == nil {
		// Example: Check metadata for thinking config if not already parsed
		// For now, we rely on what ParseRequest extracted.
	}

	// 3. Generate upstream request (Qwen format) from IR
	provider := from_ir.NewQwenProvider()
	body, headers, err := provider.GenerateRequest(irReq, token, false)
	if err != nil {
		return resp, fmt.Errorf("qwen executor: generate error: %w", err)
	}

	url := strings.TrimSuffix(baseURL, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return resp, err
	}
	provider.ApplyHeadersToRequest(httpReq, headers)

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
			log.Errorf("qwen executor: close response body error: %v", errClose)
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

	// 4. Parse upstream response to IR
	irResp, err := to_ir.ParseResponse(data, to_ir.ParserOptions{Format: to_ir.FormatOpenAI}) // Qwen is OpenAI-compatible
	if err != nil {
		return resp, fmt.Errorf("qwen executor: response parse error: %w", err)
	}

	// 5. Convert IR to downstream format
	// Using default behavior of converting IR to OpenAI-compatible JSON for the client
	// If the client requested something else, we might need 'from_ir' for that format.
	// For now, assuming client expects OpenAI-like JSON (standard behavior).
	// We can use from_ir.ToOpenAIChatCompletion to get bytes.
	out, err := from_ir.ToOpenAIChatCompletion(irResp.Messages, irResp.Usage, irReq.Model, irResp.ID)
	if err != nil {
		return resp, fmt.Errorf("qwen executor: response conversion error: %w", err)
	}
	
	resp = cliproxyexecutor.Response{Payload: out}
	return resp, nil
}

func (e *QwenExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (stream <-chan cliproxyexecutor.StreamChunk, err error) {
	token, baseURL := qwenCreds(auth)

	if baseURL == "" {
		baseURL = "https://portal.qwen.ai/v1"
	}
	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)

	// --- CANONICAL IR TRANSLATION ---
	irReq, err := to_ir.ParseRequest(req.Payload, to_ir.ParserOptions{
		Format: opts.SourceFormat,
		Model:  req.Model,
	})
	if err != nil {
		return nil, fmt.Errorf("qwen executor: parse error: %w", err)
	}

	provider := from_ir.NewQwenProvider()
	body, headers, err := provider.GenerateRequest(irReq, token, true)
	if err != nil {
		return nil, fmt.Errorf("qwen executor: generate error: %w", err)
	}

	url := strings.TrimSuffix(baseURL, "/") + "/chat/completions"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	provider.ApplyHeadersToRequest(httpReq, headers)

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
			log.Errorf("qwen executor: close response body error: %v", errClose)
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
				log.Errorf("qwen executor: close response body error: %v", errClose)
			}
		}()
		var parser *to_ir.StreamParser
		var eventIndex int

		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 52_428_800) // 50MB
		
		for scanner.Scan() {
			line := scanner.Bytes()
			appendAPIResponseChunk(ctx, e.cfg, line)
			
			// Handle keep-alive or empty lines
			if len(line) == 0 {
				continue
			}

			// Parse upstream SSE event
			// Qwen sends standard OpenAI SSE events
			events, err := to_ir.ParseSSELine(line, to_ir.ParserOptions{Format: to_ir.FormatOpenAI})
			if err != nil {
				// Log error but continue scanning
				log.Debugf("qwen executor: sse parse error: %v", err)
				continue
			}

			// Translate events to downstream format
			for _, ev := range events {
				// Initialize parser on first event to track state if needed, 
				// or just convert directly if stateless.
				// to_ir.ParseSSELine returns UnifiedEvent list.
				
				// Convert UnifiedEvent to downstream chunk (OpenAI SSE)
				chunk, err := from_ir.ToOpenAIChunk(ev, req.Model, "chatcmpl-"+ev.ID, eventIndex) // Using event ID or gen ID
				if err != nil {
					log.Debugf("qwen executor: chunk conversion error: %v", err)
					continue
				}
				if chunk != nil {
					// SSE formatting: "data: <json>\n\n"
					payload := fmt.Sprintf("data: %s\n\n", chunk)
					out <- cliproxyexecutor.StreamChunk{Payload: []byte(payload)}
				}
				
				if ev.Type == from_ir.EventTypeFinish { // Using alias from from_ir if exported, or checking IR type
					// IR types are in 'ir' package
				}
				eventIndex++
			}
		}
		// Send [DONE]
		out <- cliproxyexecutor.StreamChunk{Payload: []byte("data: [DONE]\n\n")}

		if errScan := scanner.Err(); errScan != nil {
			recordAPIResponseError(ctx, e.cfg, errScan)
			reporter.publishFailure(ctx)
			out <- cliproxyexecutor.StreamChunk{Err: errScan}
		}
	}()
	return stream, nil
}

func (e *QwenExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	// IR Translation for token counting
	irReq, err := to_ir.ParseRequest(req.Payload, to_ir.ParserOptions{
		Format: opts.SourceFormat,
		Model:  req.Model,
	})
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("qwen executor: parse error: %w", err)
	}

	// Generate temp body to count tokens
	body, err := from_ir.ToOpenAIRequest(irReq)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}

	modelName := gjson.GetBytes(body, "model").String()
	if strings.TrimSpace(modelName) == "" {
		modelName = req.Model
	}

	enc, err := tokenizerForModel(modelName)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("qwen executor: tokenizer init failed: %w", err)
	}

	count, err := countOpenAIChatTokens(enc, body)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("qwen executor: token counting failed: %w", err)
	}

	// Create result using standard OpenAI format or existing helper
	usageJSON := buildOpenAIUsageJSON(count)
	// We return raw JSON as the response payload
	return cliproxyexecutor.Response{Payload: []byte(usageJSON)}, nil
}

func (e *QwenExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("qwen executor: refresh called")
	if auth == nil {
		return nil, fmt.Errorf("qwen executor: auth is nil")
	}
	// Expect refresh_token in metadata for OAuth-based accounts
	var refreshToken string
	if auth.Metadata != nil {
		if v, ok := auth.Metadata["refresh_token"].(string); ok && strings.TrimSpace(v) != "" {
			refreshToken = v
		}
	}
	if strings.TrimSpace(refreshToken) == "" {
		// Nothing to refresh
		return auth, nil
	}

	svc := qwenauth.NewQwenAuth(e.cfg)
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
	if td.ResourceURL != "" {
		auth.Metadata["resource_url"] = td.ResourceURL
	}
	// Use "expired" for consistency with existing file format
	auth.Metadata["expired"] = td.Expire
	auth.Metadata["type"] = "qwen"
	now := time.Now().Format(time.RFC3339)
	auth.Metadata["last_refresh"] = now
	return auth, nil
}

func qwenCreds(a *cliproxyauth.Auth) (token, baseURL string) {
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
		if v, ok := a.Metadata["resource_url"].(string); ok {
			baseURL = fmt.Sprintf("https://%s/v1", v)
		}
	}
	return
}
