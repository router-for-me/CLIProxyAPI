// Package executor provides runtime execution capabilities for various AI service providers.
// It includes stateless executors that handle API requests, streaming responses,
// token counting, and authentication refresh for different AI service providers.
package executor

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	// glEndpoint is the base URL for the Google Generative Language API.
	glEndpoint = "https://generativelanguage.googleapis.com"

	// glAPIVersion is the API version used for Gemini requests.
	glAPIVersion = "v1beta"

	// streamScannerBuffer is the buffer size for SSE stream scanning.
	streamScannerBuffer = 52_428_800

	// geminiInteractionsAPIRevision is the default API revision for native Interactions requests.
	geminiInteractionsAPIRevision = "2026-05-20"
)

// GeminiExecutor is a stateless executor for the official Gemini API using API keys.
// It supports regular and streaming requests to the Google Generative Language API.
type GeminiExecutor struct {
	// cfg holds the application configuration.
	cfg        *config.Config
	identifier string
}

// NewGeminiExecutor creates a new Gemini executor instance.
//
// Parameters:
//   - cfg: The application configuration
//
// Returns:
//   - *GeminiExecutor: A new Gemini executor instance
func NewGeminiExecutor(cfg *config.Config) *GeminiExecutor {
	return &GeminiExecutor{cfg: cfg, identifier: "gemini"}
}

// NewGeminiInteractionsExecutor creates a Gemini executor bound to the native Interactions provider.
func NewGeminiInteractionsExecutor(cfg *config.Config) *GeminiExecutor {
	return &GeminiExecutor{cfg: cfg, identifier: "gemini-interactions"}
}

// Identifier returns the executor identifier.
func (e *GeminiExecutor) Identifier() string {
	if e == nil || strings.TrimSpace(e.identifier) == "" {
		return "gemini"
	}
	return e.identifier
}

// RequestToFormat reports the upstream request format used after auth selection.
func (e *GeminiExecutor) RequestToFormat(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) sdktranslator.Format {
	if strings.EqualFold(strings.TrimSpace(e.Identifier()), "gemini-interactions") && nativeInteractionsSourceFormat(opts.SourceFormat) {
		return sdktranslator.FormatInteractions
	}
	return sdktranslator.FormatGemini
}

// PrepareRequest injects Gemini credentials into the outgoing HTTP request.
func (e *GeminiExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	apiKey := geminiAPIKey(auth)
	if apiKey != "" {
		req.Header.Set("x-goog-api-key", apiKey)
		req.Header.Del("Authorization")
	}
	applyGeminiHeaders(req, auth)
	return nil
}

// HttpRequest injects Gemini credentials into the request and executes it.
func (e *GeminiExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("gemini executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return nil, err
	}
	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}

// Execute performs a non-streaming request to the Gemini API.
// It translates the request to Gemini format, sends it to the API, and translates
// the response back to the requested format.
//
// Parameters:
//   - ctx: The context for the request
//   - auth: The authentication information
//   - req: The request to execute
//   - opts: Additional execution options
//
// Returns:
//   - cliproxyexecutor.Response: The response from the API
//   - error: An error if the request fails
func (e *GeminiExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	if opts.Alt == "responses/compact" {
		return resp, statusErr{code: http.StatusNotImplemented, msg: "/responses/compact not supported"}
	}
	if shouldExecuteNativeInteractions(auth, opts) {
		return e.executeInteractions(ctx, auth, req, opts)
	}
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	apiKey := geminiAPIKey(auth)

	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	// Official Gemini API via API key.
	from := opts.SourceFormat
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	to := sdktranslator.FromString("gemini")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	originalTranslated := helps.TranslateRequestWithCodexMultiAgentV2(ctx, opts.Headers, e.cfg, from, to, baseModel, originalPayload, false)
	body := helps.TranslateRequestWithCodexMultiAgentV2(ctx, opts.Headers, e.cfg, from, to, baseModel, req.Payload, false)

	body, err = thinking.ApplyThinking(body, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return resp, err
	}

	body = fixGeminiImageAspectRatio(baseModel, body)
	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	body = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), from.String(), "", body, originalTranslated, requestedModel, requestPath, opts.Headers)
	body = helps.SetStringIfDifferent(body, "model", baseModel)
	body = capGeminiMaxOutputTokens(body, baseModel)

	action := "generateContent"
	if req.Metadata != nil {
		if a, _ := req.Metadata["action"].(string); a == "countTokens" {
			action = "countTokens"
		}
	}
	baseURL := resolveGeminiBaseURL(auth)
	url := fmt.Sprintf("%s/%s/models/%s:%s", baseURL, glAPIVersion, baseModel, action)
	if opts.Alt != "" && action != "countTokens" {
		url = url + fmt.Sprintf("?$alt=%s", opts.Alt)
	}

	body, _ = sjson.DeleteBytes(body, "session_id")
	reporter.SetTranslatedReasoningEffort(body, to.String())

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return resp, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		httpReq.Header.Set("x-goog-api-key", apiKey)
	}
	applyGeminiHeaders(httpReq, auth)
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
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

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("gemini executor: close response body error: %v", errClose)
		}
	}()
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return resp, err
	}
	data, err := io.ReadAll(httpResp.Body)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, data)
	reporter.Publish(ctx, helps.ParseGeminiUsage(data))
	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, responseFormat, req.Model, opts.OriginalRequest, body, data, &param)
	resp = cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}
	return resp, nil
}

// ExecuteStream performs a streaming request to the Gemini API.
func (e *GeminiExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	if opts.Alt == "responses/compact" {
		return nil, statusErr{code: http.StatusNotImplemented, msg: "/responses/compact not supported"}
	}
	if shouldExecuteNativeInteractions(auth, opts) {
		return e.executeInteractionsStream(ctx, auth, req, opts)
	}
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	apiKey := geminiAPIKey(auth)

	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	from := opts.SourceFormat
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	to := sdktranslator.FromString("gemini")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	originalTranslated := helps.TranslateRequestWithCodexMultiAgentV2(ctx, opts.Headers, e.cfg, from, to, baseModel, originalPayload, true)
	body := helps.TranslateRequestWithCodexMultiAgentV2(ctx, opts.Headers, e.cfg, from, to, baseModel, req.Payload, true)

	body, err = thinking.ApplyThinking(body, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return nil, err
	}

	body = fixGeminiImageAspectRatio(baseModel, body)
	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	body = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), from.String(), "", body, originalTranslated, requestedModel, requestPath, opts.Headers)
	body = helps.SetStringIfDifferent(body, "model", baseModel)
	body = capGeminiMaxOutputTokens(body, baseModel)

	baseURL := resolveGeminiBaseURL(auth)
	url := fmt.Sprintf("%s/%s/models/%s:%s", baseURL, glAPIVersion, baseModel, "streamGenerateContent")
	if opts.Alt == "" {
		url = url + "?alt=sse"
	} else {
		url = url + fmt.Sprintf("?$alt=%s", opts.Alt)
	}

	body, _ = sjson.DeleteBytes(body, "session_id")
	reporter.SetTranslatedReasoningEffort(body, to.String())

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		httpReq.Header.Set("x-goog-api-key", apiKey)
	}
	applyGeminiHeaders(httpReq, auth)
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
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

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	httpClient = reporter.TrackHTTPClient(httpClient)
	httpResp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		helps.AppendAPIResponseChunk(ctx, e.cfg, b)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), b))
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("gemini executor: close response body error: %v", errClose)
		}
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return nil, err
	}
	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("gemini executor: close response body error: %v", errClose)
			}
		}()
		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, streamScannerBuffer)
		claudeInputTokens := helps.NewClaudeInputTokenState(from, to, responseFormat, originalPayload)
		var param any
		for scanner.Scan() {
			line := scanner.Bytes()
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
			filtered := helps.FilterSSEUsageMetadata(line)
			payload := helps.JSONPayload(filtered)
			if len(payload) == 0 {
				continue
			}
			if detail, ok := helps.ParseGeminiStreamUsage(payload); ok {
				reporter.Publish(ctx, detail)
			}
			lines := helps.TranslateStreamWithClaudeInputTokens(ctx, to, responseFormat, req.Model, opts.OriginalRequest, body, bytes.Clone(payload), &param, claudeInputTokens)
			for i := range lines {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: lines[i]}:
				case <-ctx.Done():
					return
				}
			}
		}
		lines := helps.TranslateStreamWithClaudeInputTokens(ctx, to, responseFormat, req.Model, opts.OriginalRequest, body, []byte("[DONE]"), &param, claudeInputTokens)
		for i := range lines {
			select {
			case out <- cliproxyexecutor.StreamChunk{Payload: lines[i]}:
			case <-ctx.Done():
				return
			}
		}
		if errScan := scanner.Err(); errScan != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errScan)
			reporter.PublishFailure(ctx, errScan)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: errScan}:
			case <-ctx.Done():
			}
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

func (e *GeminiExecutor) executeInteractions(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	targetName := thinking.ParseSuffix(req.Model).ModelName
	apiKey := geminiAPIKey(auth)
	reporter := helps.NewExecutorUsageReporter(ctx, e, targetName, auth)
	defer reporter.TrackFailure(ctx, &err)

	body := translateGeminiInteractionsRequestBody(ctx, e.cfg, targetName, req.Payload, opts, false)
	if gjson.GetBytes(body, "model").Exists() && targetName != "" {
		body = helps.SetStringIfDifferent(body, "model", targetName)
	}
	body, err = applyGeminiInteractionsThinking(body, req.Model)
	if err != nil {
		return resp, err
	}
	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	fromProtocol := opts.SourceFormat.String()
	originalTranslated := geminiInteractionsPayloadConfigSource(ctx, e.cfg, targetName, req.Payload, opts, false)
	body = helps.ApplyPayloadConfigWithRequest(e.cfg, targetName, "interactions", fromProtocol, "", body, originalTranslated, requestedModel, requestPath, opts.Headers)

	baseURL := resolveGeminiBaseURL(auth)
	url := fmt.Sprintf("%s/%s/interactions", baseURL, glAPIVersion)
	httpReq, errRequest := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if errRequest != nil {
		return resp, errRequest
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		httpReq.Header.Set("x-goog-api-key", apiKey)
	}
	applyGeminiHeaders(httpReq, auth)
	applyGeminiInteractionsRequestHeaders(httpReq, opts.Headers)
	applyGeminiInteractionsRevisionHeader(httpReq)

	authID, authLabel, authType, authValue := geminiAuthLogFields(auth)
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
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

	httpClient := reporter.TrackHTTPClient(helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0))
	httpResp, errDo := httpClient.Do(httpReq)
	if errDo != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errDo)
		return resp, errDo
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("gemini executor: close interactions response body error: %v", errClose)
		}
	}()
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	data, errRead := io.ReadAll(httpResp.Body)
	if errRead != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errRead)
		return resp, errRead
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, data)
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
		err = statusErr{code: httpResp.StatusCode, msg: string(data)}
		return resp, err
	}
	reporter.Publish(ctx, helps.ParseInteractionsUsage(data))
	var param any
	out := sdktranslator.TranslateNonStream(ctx, sdktranslator.FormatInteractions, cliproxyexecutor.ResponseFormatOrSource(opts), req.Model, opts.OriginalRequest, body, data, &param)
	return cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}, nil
}

func (e *GeminiExecutor) executeInteractionsStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	targetName := thinking.ParseSuffix(req.Model).ModelName
	apiKey := geminiAPIKey(auth)
	reporter := helps.NewExecutorUsageReporter(ctx, e, targetName, auth)
	defer reporter.TrackFailure(ctx, &err)

	body := translateGeminiInteractionsRequestBody(ctx, e.cfg, targetName, req.Payload, opts, true)
	if gjson.GetBytes(body, "model").Exists() && targetName != "" {
		body = helps.SetStringIfDifferent(body, "model", targetName)
	}
	body, err = applyGeminiInteractionsThinking(body, req.Model)
	if err != nil {
		return nil, err
	}
	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	fromProtocol := opts.SourceFormat.String()
	originalTranslated := geminiInteractionsPayloadConfigSource(ctx, e.cfg, targetName, req.Payload, opts, true)
	body = helps.ApplyPayloadConfigWithRequest(e.cfg, targetName, "interactions", fromProtocol, "", body, originalTranslated, requestedModel, requestPath, opts.Headers)
	body = helps.SetBoolIfDifferent(body, "stream", true)
	baseURL := resolveGeminiBaseURL(auth)
	url := fmt.Sprintf("%s/%s/interactions", baseURL, glAPIVersion)
	httpReq, errRequest := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if errRequest != nil {
		return nil, errRequest
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		httpReq.Header.Set("x-goog-api-key", apiKey)
	}
	applyGeminiHeaders(httpReq, auth)
	applyGeminiInteractionsRequestHeaders(httpReq, opts.Headers)
	applyGeminiInteractionsRevisionHeader(httpReq)

	authID, authLabel, authType, authValue := geminiAuthLogFields(auth)
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
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

	httpClient := reporter.TrackHTTPClient(helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0))
	httpResp, errDo := httpClient.Do(httpReq)
	if errDo != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errDo)
		return nil, errDo
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		data, _ := io.ReadAll(httpResp.Body)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("gemini executor: close interactions error response body error: %v", errClose)
		}
		helps.AppendAPIResponseChunk(ctx, e.cfg, data)
		return nil, statusErr{code: httpResp.StatusCode, msg: string(data)}
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("gemini executor: close interactions stream body error: %v", errClose)
			}
		}()
		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, streamScannerBuffer)
		originalRequest := opts.OriginalRequest
		if len(originalRequest) == 0 {
			originalRequest = req.Payload
		}
		claudeInputTokens := helps.NewClaudeInputTokenState(opts.SourceFormat, sdktranslator.FormatInteractions, responseFormat, originalRequest)
		var param any
		var frame []byte
		emitFrame := func() bool {
			rawFrame := bytes.Clone(frame)
			trimmed := bytes.TrimSpace(rawFrame)
			frame = frame[:0]
			if len(trimmed) == 0 {
				return true
			}
			payload := geminiInteractionsSSEPayload(rawFrame)
			if len(payload) == 0 && geminiInteractionsSSEDone(rawFrame) {
				payload = []byte("[DONE]")
			}
			if len(payload) == 0 && len(trimmed) > 0 && trimmed[0] == '{' {
				payload = trimmed
			}
			if len(payload) > 0 {
				if detail, ok := helps.ParseInteractionsStreamUsage(payload); ok {
					reporter.Publish(ctx, detail)
				}
			}
			if responseFormat == sdktranslator.FormatInteractions {
				visibleFrame := append(bytes.TrimRight(rawFrame, "\r\n"), '\n', '\n')
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: visibleFrame}:
				case <-ctx.Done():
					return false
				}
				return true
			}
			if len(payload) == 0 {
				return true
			}
			var lines [][]byte
			lines = helps.TranslateStreamWithClaudeInputTokens(ctx, sdktranslator.FormatInteractions, responseFormat, req.Model, opts.OriginalRequest, body, payload, &param, claudeInputTokens)
			for i := range lines {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: lines[i]}:
				case <-ctx.Done():
					return false
				}
			}
			return true
		}
		for scanner.Scan() {
			line := bytes.Clone(scanner.Bytes())
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
			trimmed := bytes.TrimSpace(line)
			if len(trimmed) == 0 {
				if !emitFrame() {
					return
				}
				continue
			}
			if len(frame) > 0 {
				frame = append(frame, '\n')
			}
			frame = append(frame, line...)
		}
		if !emitFrame() {
			return
		}
		if errScan := scanner.Err(); errScan != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errScan)
			reporter.PublishFailure(ctx, errScan)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: errScan}:
			case <-ctx.Done():
			}
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

// CountTokens counts tokens for the given request using the Gemini API.
func (e *GeminiExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	apiKey := geminiAPIKey(auth)

	from := opts.SourceFormat
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	to := sdktranslator.FromString("gemini")
	translatedReq := helps.TranslateRequestWithCodexMultiAgentV2(ctx, opts.Headers, e.cfg, from, to, baseModel, req.Payload, false)

	translatedReq, err := thinking.ApplyThinking(translatedReq, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}

	translatedReq = fixGeminiImageAspectRatio(baseModel, translatedReq)
	respCtx := context.WithValue(ctx, "alt", opts.Alt)
	translatedReq, _ = sjson.DeleteBytes(translatedReq, "tools")
	translatedReq, _ = sjson.DeleteBytes(translatedReq, "generationConfig")
	translatedReq, _ = sjson.DeleteBytes(translatedReq, "safetySettings")
	translatedReq = helps.SetStringIfDifferent(translatedReq, "model", baseModel)

	baseURL := resolveGeminiBaseURL(auth)
	url := fmt.Sprintf("%s/%s/models/%s:%s", baseURL, glAPIVersion, baseModel, "countTokens")

	requestBody := bytes.NewReader(translatedReq)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, requestBody)
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		httpReq.Header.Set("x-goog-api-key", apiKey)
	}
	applyGeminiHeaders(httpReq, auth)
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, e.cfg, helps.UpstreamRequestLog{
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

	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	resp, err := httpClient.Do(httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return cliproxyexecutor.Response{}, err
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			helps.LogWithRequestID(ctx).Errorf("response body close error: %v", errClose)
		}
	}()
	helps.RecordAPIResponseMetadata(ctx, e.cfg, resp.StatusCode, resp.Header.Clone())

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return cliproxyexecutor.Response{}, err
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, data)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", resp.StatusCode, helps.SummarizeErrorBody(resp.Header.Get("Content-Type"), data))
		return cliproxyexecutor.Response{}, statusErr{code: resp.StatusCode, msg: string(data)}
	}

	count := gjson.GetBytes(data, "totalTokens").Int()
	translated := sdktranslator.TranslateTokenCount(respCtx, to, responseFormat, count, data)
	return cliproxyexecutor.Response{Payload: translated, Headers: resp.Header.Clone()}, nil
}

// Refresh refreshes the authentication credentials (no-op for Gemini API key).
func (e *GeminiExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, err
	}
	return auth, nil
}

func geminiAPIKey(a *cliproxyauth.Auth) string {
	if a == nil {
		return ""
	}
	if a.Attributes != nil {
		if v := a.Attributes["api_key"]; v != "" {
			return v
		}
	}
	return ""
}

func resolveGeminiBaseURL(auth *cliproxyauth.Auth) string {
	base := glEndpoint
	if auth != nil && auth.Attributes != nil {
		if custom := strings.TrimSpace(auth.Attributes["base_url"]); custom != "" {
			base = strings.TrimRight(custom, "/")
		}
	}
	if base == "" {
		return glEndpoint
	}
	return base
}

func (e *GeminiExecutor) resolveGeminiConfig(auth *cliproxyauth.Auth) *config.GeminiKey {
	if auth == nil || e.cfg == nil {
		return nil
	}
	var attrKey, attrBase string
	if auth.Attributes != nil {
		attrKey = strings.TrimSpace(auth.Attributes["api_key"])
		attrBase = strings.TrimSpace(auth.Attributes["base_url"])
	}
	for i := range e.cfg.GeminiKey {
		entry := &e.cfg.GeminiKey[i]
		cfgKey := strings.TrimSpace(entry.APIKey)
		cfgBase := strings.TrimSpace(entry.BaseURL)
		if attrKey != "" && attrBase != "" {
			if strings.EqualFold(cfgKey, attrKey) && strings.EqualFold(cfgBase, attrBase) {
				return entry
			}
			continue
		}
		if attrKey != "" && strings.EqualFold(cfgKey, attrKey) {
			if cfgBase == "" || strings.EqualFold(cfgBase, attrBase) {
				return entry
			}
		}
		if attrKey == "" && attrBase != "" && strings.EqualFold(cfgBase, attrBase) {
			return entry
		}
	}
	if attrKey != "" {
		for i := range e.cfg.GeminiKey {
			entry := &e.cfg.GeminiKey[i]
			if strings.EqualFold(strings.TrimSpace(entry.APIKey), attrKey) {
				return entry
			}
		}
	}
	return nil
}

func shouldExecuteNativeInteractions(auth *cliproxyauth.Auth, opts cliproxyexecutor.Options) bool {
	return nativeInteractionsSourceFormat(opts.SourceFormat) && isNativeInteractionsAuth(auth)
}

func nativeInteractionsSourceFormat(format sdktranslator.Format) bool {
	switch format {
	case sdktranslator.FormatInteractions, sdktranslator.FormatOpenAI, sdktranslator.FormatOpenAIResponse, sdktranslator.FormatClaude, sdktranslator.FormatGemini:
		return true
	default:
		return false
	}
}

func translateGeminiInteractionsRequestBody(ctx context.Context, cfg *config.Config, model string, payload []byte, opts cliproxyexecutor.Options, stream bool) []byte {
	if opts.SourceFormat == "" || opts.SourceFormat == sdktranslator.FormatInteractions {
		return bytes.Clone(payload)
	}
	return helps.TranslateRequestWithCodexMultiAgentV2(ctx, opts.Headers, cfg, opts.SourceFormat, sdktranslator.FormatInteractions, model, payload, stream)
}

func geminiInteractionsPayloadConfigSource(ctx context.Context, cfg *config.Config, model string, payload []byte, opts cliproxyexecutor.Options, stream bool) []byte {
	source := opts.OriginalRequest
	if len(source) == 0 {
		source = payload
	}
	return translateGeminiInteractionsRequestBody(ctx, cfg, model, source, opts, stream)
}

func isNativeInteractionsAuth(auth *cliproxyauth.Auth) bool {
	if auth == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(auth.Provider), "gemini-interactions")
}

func applyGeminiInteractionsThinking(body []byte, model string) ([]byte, error) {
	return thinking.ApplyThinking(body, model, sdktranslator.FormatInteractions.String(), sdktranslator.FormatInteractions.String(), "gemini")
}

func applyGeminiInteractionsRevisionHeader(req *http.Request) {
	if req == nil {
		return
	}
	if req.Header.Get("Api-Revision") == "" {
		req.Header.Set("Api-Revision", geminiInteractionsAPIRevision)
	}
}

func applyGeminiInteractionsRequestHeaders(req *http.Request, headers http.Header) {
	if req == nil || headers == nil || req.Header.Get("Api-Revision") != "" {
		return
	}
	if revision := headers.Get("Api-Revision"); revision != "" {
		req.Header.Set("Api-Revision", revision)
	}
}

func geminiInteractionsSSEPayload(frame []byte) []byte {
	trimmed := bytes.TrimSpace(frame)
	if len(trimmed) == 0 {
		return nil
	}
	if bytes.HasPrefix(trimmed, []byte("{")) {
		return trimmed
	}
	lines := bytes.Split(frame, []byte{'\n'})
	var payload []byte
	for _, line := range lines {
		line = bytes.TrimRight(line, "\r")
		if !bytes.HasPrefix(bytes.TrimSpace(line), []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(line[bytes.Index(line, []byte("data:"))+len("data:"):])
		if len(data) == 0 || bytes.Equal(data, []byte("[DONE]")) {
			continue
		}
		if len(payload) > 0 {
			payload = append(payload, '\n')
		}
		payload = append(payload, data...)
	}
	if len(payload) == 0 {
		return nil
	}
	return payload
}

func geminiInteractionsSSEDone(frame []byte) bool {
	trimmed := bytes.TrimSpace(frame)
	if bytes.Equal(trimmed, []byte("[DONE]")) {
		return true
	}
	lines := bytes.Split(frame, []byte{'\n'})
	sawDoneEvent := false
	for _, line := range lines {
		line = bytes.TrimSpace(bytes.TrimRight(line, "\r"))
		if bytes.EqualFold(line, []byte("event: done")) {
			sawDoneEvent = true
			continue
		}
		if bytes.HasPrefix(line, []byte("data:")) {
			data := bytes.TrimSpace(line[len("data:"):])
			if bytes.Equal(data, []byte("[DONE]")) {
				return true
			}
		}
	}
	return sawDoneEvent
}

func geminiAuthLogFields(auth *cliproxyauth.Auth) (string, string, string, string) {
	if auth == nil {
		return "", "", "", ""
	}
	authType, authValue := auth.AccountInfo()
	return auth.ID, auth.Label, authType, authValue
}

func applyGeminiHeaders(req *http.Request, auth *cliproxyauth.Auth) {
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs)
}

func capGeminiMaxOutputTokens(body []byte, modelName string) []byte {
	maxOut := gjson.GetBytes(body, "generationConfig.maxOutputTokens")
	if !maxOut.Exists() || maxOut.Type != gjson.Number {
		return body
	}
	modelInfo := registry.LookupModelInfo(modelName, "gemini")
	if modelInfo == nil {
		return body
	}
	limit := modelInfo.OutputTokenLimit
	if limit <= 0 {
		limit = modelInfo.MaxCompletionTokens
	}
	if limit <= 0 || maxOut.Int() <= int64(limit) {
		return body
	}
	body, _ = sjson.SetBytes(body, "generationConfig.maxOutputTokens", limit)
	return body
}

func fixGeminiImageAspectRatio(modelName string, rawJSON []byte) []byte {
	if modelName == "gemini-2.5-flash-image-preview" {
		aspectRatioResult := gjson.GetBytes(rawJSON, "generationConfig.imageConfig.aspectRatio")
		if aspectRatioResult.Exists() {
			contents := gjson.GetBytes(rawJSON, "contents")
			contentArray := contents.Array()
			if len(contentArray) > 0 {
				hasInlineData := false
			loopContent:
				for i := 0; i < len(contentArray); i++ {
					parts := contentArray[i].Get("parts").Array()
					for j := 0; j < len(parts); j++ {
						if parts[j].Get("inlineData").Exists() {
							hasInlineData = true
							break loopContent
						}
					}
				}

				if !hasInlineData {
					emptyImageBase64ed, _ := util.CreateWhiteImageBase64(aspectRatioResult.String())
					emptyImagePart := []byte(`{"inlineData":{"mime_type":"image/png","data":""}}`)
					emptyImagePart, _ = sjson.SetBytes(emptyImagePart, "inlineData.data", emptyImageBase64ed)
					newPartsJson := []byte(`[]`)
					newPartsJson, _ = sjson.SetRawBytes(newPartsJson, "-1", []byte(`{"text": "Based on the following requirements, create an image within the uploaded picture. The new content *MUST* completely cover the entire area of the original picture, maintaining its exact proportions, and *NO* blank areas should appear."}`))
					newPartsJson, _ = sjson.SetRawBytes(newPartsJson, "-1", emptyImagePart)

					parts := contentArray[0].Get("parts").Array()
					for j := 0; j < len(parts); j++ {
						newPartsJson, _ = sjson.SetRawBytes(newPartsJson, "-1", []byte(parts[j].Raw))
					}

					rawJSON, _ = sjson.SetRawBytes(rawJSON, "contents.0.parts", newPartsJson)
					rawJSON, _ = sjson.SetRawBytes(rawJSON, "generationConfig.responseModalities", []byte(`["IMAGE", "TEXT"]`))
				}
			}
			rawJSON, _ = sjson.DeleteBytes(rawJSON, "generationConfig.imageConfig")
		}
	}
	return rawJSON
}
