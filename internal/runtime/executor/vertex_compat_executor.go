// Package executor provides runtime execution capabilities for various AI service providers.
package executor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	// vertexCompatAPIVersion is the API version for Vertex-compatible endpoints.
	vertexCompatAPIVersion = "v1"
)

// VertexCompatExecutor is a stateless executor for Vertex AI-compatible APIs
// that use Vertex-style paths (/publishers/google/models/{model}:action)
// but authenticate with simple API keys instead of Google Cloud service accounts.
//
// This executor supports third-party providers like zenmux.ai that mimic
// Vertex AI's URL structure while using simpler authentication mechanisms.
type VertexCompatExecutor struct {
	cfg *config.Config
}

// NewVertexCompatExecutor creates a new Vertex-compatible executor instance.
func NewVertexCompatExecutor(cfg *config.Config) *VertexCompatExecutor {
	return &VertexCompatExecutor{cfg: cfg}
}

// Identifier returns the executor identifier for routing.
func (e *VertexCompatExecutor) Identifier() string { return "vertex-compat" }

// PrepareRequest is a no-op for API key based authentication.
func (e *VertexCompatExecutor) PrepareRequest(_ *http.Request, _ *cliproxyauth.Auth) error {
	return nil
}

// Execute performs a non-streaming request to the Vertex-compatible API.
func (e *VertexCompatExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	apiKey, baseURL := vertexCompatCreds(auth)

	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("gemini")
	body := sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), false)

	// Apply thinking config if supported
	if budgetOverride, includeOverride, ok := util.GeminiThinkingFromMetadata(req.Metadata); ok && util.ModelSupportsThinking(req.Model) {
		if budgetOverride != nil {
			norm := util.NormalizeThinkingBudget(req.Model, *budgetOverride)
			budgetOverride = &norm
		}
		body = util.ApplyGeminiThinkingConfig(body, budgetOverride, includeOverride)
	}
	body = util.StripThinkingConfigIfUnsupported(req.Model, body)
	body = applyPayloadConfig(e.cfg, req.Model, body)

	action := "generateContent"
	if req.Metadata != nil {
		if a, _ := req.Metadata["action"].(string); a == "countTokens" {
			action = "countTokens"
		}
	}

	// Construct Vertex-style URL: {baseURL}/v1/publishers/google/models/{model}:action
	url := fmt.Sprintf("%s/%s/publishers/google/models/%s:%s", baseURL, vertexCompatAPIVersion, req.Model, action)
	if opts.Alt != "" && action != "countTokens" {
		url = url + fmt.Sprintf("?$alt=%s", opts.Alt)
	}
	body, _ = sjson.DeleteBytes(body, "session_id")

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return resp, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		httpReq.Header.Set("x-goog-api-key", apiKey)
	}
	applyVertexCompatHeaders(httpReq, auth)

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
			log.Errorf("vertex-compat executor: close response body error: %v", errClose)
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
	reporter.publish(ctx, parseGeminiUsage(data))

	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, bytes.Clone(opts.OriginalRequest), body, data, &param)
	resp = cliproxyexecutor.Response{Payload: []byte(out)}
	return resp, nil
}

// ExecuteStream handles SSE streaming for Vertex-compatible APIs.
func (e *VertexCompatExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (stream <-chan cliproxyexecutor.StreamChunk, err error) {
	apiKey, baseURL := vertexCompatCreds(auth)

	reporter := newUsageReporter(ctx, e.Identifier(), req.Model, auth)
	defer reporter.trackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("gemini")
	body := sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), true)

	// Apply thinking config if supported
	if budgetOverride, includeOverride, ok := util.GeminiThinkingFromMetadata(req.Metadata); ok && util.ModelSupportsThinking(req.Model) {
		if budgetOverride != nil {
			norm := util.NormalizeThinkingBudget(req.Model, *budgetOverride)
			budgetOverride = &norm
		}
		body = util.ApplyGeminiThinkingConfig(body, budgetOverride, includeOverride)
	}
	body = util.StripThinkingConfigIfUnsupported(req.Model, body)
	body = applyPayloadConfig(e.cfg, req.Model, body)

	// Construct Vertex-style streaming URL
	url := fmt.Sprintf("%s/%s/publishers/google/models/%s:%s", baseURL, vertexCompatAPIVersion, req.Model, "streamGenerateContent")
	if opts.Alt == "" {
		url = url + "?alt=sse"
	} else {
		url = url + fmt.Sprintf("?$alt=%s", opts.Alt)
	}
	body, _ = sjson.DeleteBytes(body, "session_id")

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		httpReq.Header.Set("x-goog-api-key", apiKey)
	}
	applyVertexCompatHeaders(httpReq, auth)

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
			log.Errorf("vertex-compat executor: close response body error: %v", errClose)
		}
		return nil, statusErr{code: httpResp.StatusCode, msg: string(b)}
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	stream = out
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("vertex-compat executor: close response body error: %v", errClose)
			}
		}()
		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 20_971_520)
		var param any
		for scanner.Scan() {
			line := scanner.Bytes()
			appendAPIResponseChunk(ctx, e.cfg, line)
			filtered := FilterSSEUsageMetadata(line)
			payload := jsonPayload(filtered)
			if len(payload) == 0 {
				continue
			}
			if detail, ok := parseGeminiStreamUsage(payload); ok {
				reporter.publish(ctx, detail)
			}
			lines := sdktranslator.TranslateStream(ctx, to, from, req.Model, bytes.Clone(opts.OriginalRequest), body, bytes.Clone(payload), &param)
			for i := range lines {
				out <- cliproxyexecutor.StreamChunk{Payload: []byte(lines[i])}
			}
		}
		lines := sdktranslator.TranslateStream(ctx, to, from, req.Model, bytes.Clone(opts.OriginalRequest), body, bytes.Clone([]byte("[DONE]")), &param)
		for i := range lines {
			out <- cliproxyexecutor.StreamChunk{Payload: []byte(lines[i])}
		}
		if errScan := scanner.Err(); errScan != nil {
			recordAPIResponseError(ctx, e.cfg, errScan)
			reporter.publishFailure(ctx)
			out <- cliproxyexecutor.StreamChunk{Err: errScan}
		}
	}()
	return stream, nil
}

// CountTokens calls the Vertex-compatible countTokens endpoint.
func (e *VertexCompatExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	apiKey, baseURL := vertexCompatCreds(auth)

	from := opts.SourceFormat
	to := sdktranslator.FromString("gemini")
	translatedReq := sdktranslator.TranslateRequest(from, to, req.Model, bytes.Clone(req.Payload), false)

	if budgetOverride, includeOverride, ok := util.GeminiThinkingFromMetadata(req.Metadata); ok && util.ModelSupportsThinking(req.Model) {
		if budgetOverride != nil {
			norm := util.NormalizeThinkingBudget(req.Model, *budgetOverride)
			budgetOverride = &norm
		}
		translatedReq = util.ApplyGeminiThinkingConfig(translatedReq, budgetOverride, includeOverride)
	}
	translatedReq = util.StripThinkingConfigIfUnsupported(req.Model, translatedReq)

	respCtx := ctx
	if opts.Alt != "" {
		respCtx = context.WithValue(ctx, "alt", opts.Alt)
	}
	translatedReq, _ = sjson.DeleteBytes(translatedReq, "tools")
	translatedReq, _ = sjson.DeleteBytes(translatedReq, "generationConfig")
	translatedReq, _ = sjson.DeleteBytes(translatedReq, "safetySettings")

	url := fmt.Sprintf("%s/%s/publishers/google/models/%s:%s", baseURL, vertexCompatAPIVersion, req.Model, "countTokens")

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(translatedReq))
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		httpReq.Header.Set("x-goog-api-key", apiKey)
	}
	applyVertexCompatHeaders(httpReq, auth)

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
		Body:      translatedReq,
		Provider:  e.Identifier(),
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})

	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	resp, err := httpClient.Do(httpReq)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return cliproxyexecutor.Response{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	recordAPIResponseMetadata(ctx, e.cfg, resp.StatusCode, resp.Header.Clone())

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return cliproxyexecutor.Response{}, err
	}
	appendAPIResponseChunk(ctx, e.cfg, data)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Debugf("request error, error status: %d, error body: %s", resp.StatusCode, summarizeErrorBody(resp.Header.Get("Content-Type"), data))
		return cliproxyexecutor.Response{}, statusErr{code: resp.StatusCode, msg: string(data)}
	}

	count := gjson.GetBytes(data, "totalTokens").Int()
	translated := sdktranslator.TranslateTokenCount(respCtx, to, from, count, data)
	return cliproxyexecutor.Response{Payload: []byte(translated)}, nil
}

// Refresh is a no-op for API key based authentication.
func (e *VertexCompatExecutor) Refresh(_ context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	return auth, nil
}

// vertexCompatCreds extracts API key and base URL from auth attributes.
func vertexCompatCreds(a *cliproxyauth.Auth) (apiKey, baseURL string) {
	if a == nil || a.Attributes == nil {
		return "", ""
	}
	if v := a.Attributes["api_key"]; v != "" {
		apiKey = v
	}
	if v := a.Attributes["base_url"]; v != "" {
		baseURL = strings.TrimRight(strings.TrimSpace(v), "/")
	}
	return
}

// applyVertexCompatHeaders applies custom headers from auth attributes.
func applyVertexCompatHeaders(req *http.Request, auth *cliproxyauth.Auth) {
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs)
}
