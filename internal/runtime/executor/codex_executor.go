package executor

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	codexauth "github.com/router-for-me/CLIProxyAPI/v6/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

var errCodexStopStream = errors.New("codex executor: stop stream after terminal event")

// CodexExecutor executes Codex requests and reuses per-proxy auth services for refresh flows.
// If api_key is unavailable on auth, it falls back to legacy via ClientAdapter.
type CodexExecutor struct {
	cfg            *config.Config
	codexAuthCache sync.Map
	responseDedupe helps.InFlightGroup[codexNonStreamHTTPResult]
}

func NewCodexExecutor(cfg *config.Config) *CodexExecutor { return &CodexExecutor{cfg: cfg} }

func (e *CodexExecutor) Identifier() string { return "codex" }

func (e *CodexExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	if opts.Alt == "responses/compact" {
		return e.executeCompact(ctx, auth, req, opts)
	}
	needResponseHeaders := needResponseHeadersFromOptions(opts)
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	apiKey, baseURL := codexCreds(auth)
	if baseURL == "" {
		baseURL = "https://chatgpt.com/backend-api/codex"
	}

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	reporter.CaptureModelReasoningEffort(opts.OriginalRequest, req.Payload)
	defer reporter.TrackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("codex")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	body, originalTranslated := helps.TranslateRequestWithOriginal(e.cfg, from, to, baseModel, req.Payload, originalPayload, false)

	body, err = thinking.ApplyThinking(body, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return resp, err
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	body = helps.ApplyPayloadConfigWithRoot(e.cfg, baseModel, to.String(), "", body, originalTranslated, requestedModel)

	url := strings.TrimSuffix(baseURL, "/") + "/responses"
	call, err := e.prepareCodexHTTPCall(ctx, auth, from, executionSessionIDFromOptions(opts), url, req, body, apiKey, true)
	if err != nil {
		return resp, err
	}
	body = call.prepared.body
	helps.RecordAPIRequest(ctx, e.cfg, call.requestLog)
	result, usageOwner, err := e.fetchCodexResponsesAggregate(ctx, auth, call.url, call.prepared, needResponseHeaders)
	if err != nil {
		return resp, err
	}
	if result.statusCode < 200 || result.statusCode >= 300 {
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", result.statusCode, helps.SummarizeErrorBody(result.headers.Get("Content-Type"), result.body))
		err = newCodexStatusErr(result.statusCode, result.body)
		return resp, err
	}
	if len(result.completedData) > 0 {
		if usageOwner {
			if detail, ok := helps.ParseCodexUsage(result.completedData); ok {
				reporter.Publish(ctx, detail)
			}
			reporter.EnsurePublished(ctx)
		}

		var param any
		out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, originalPayload, body, result.completedData, &param)
		resp = cliproxyexecutor.Response{Payload: out}
		if needResponseHeaders {
			resp.Headers = result.headers
		}
		return resp, nil
	}
	if len(result.errorBody) > 0 {
		err = newCodexStatusErr(result.errorStatus, result.errorBody)
		return resp, err
	}
	err = statusErr{code: 408, msg: "stream error: stream disconnected before completion: stream closed before response.completed"}
	return resp, err
}

func (e *CodexExecutor) executeCompact(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	needResponseHeaders := needResponseHeadersFromOptions(opts)
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	apiKey, baseURL := codexCreds(auth)
	if baseURL == "" {
		baseURL = "https://chatgpt.com/backend-api/codex"
	}

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	reporter.CaptureModelReasoningEffort(opts.OriginalRequest, req.Payload)
	defer reporter.TrackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai-response")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	body, originalTranslated := helps.TranslateRequestWithOriginal(e.cfg, from, to, baseModel, req.Payload, originalPayload, false)

	body, err = thinking.ApplyThinking(body, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return resp, err
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	body = helps.ApplyPayloadConfigWithRoot(e.cfg, baseModel, to.String(), "", body, originalTranslated, requestedModel)

	url := strings.TrimSuffix(baseURL, "/") + "/responses/compact"
	call, err := e.prepareCodexHTTPCall(ctx, auth, from, executionSessionIDFromOptions(opts), url, req, body, apiKey, false)
	if err != nil {
		return resp, err
	}
	body = call.prepared.body
	helps.RecordAPIRequest(ctx, e.cfg, call.requestLog)
	result, usageOwner, err := e.fetchCodexNonStreamResponse(ctx, auth, call.url, call.prepared, needResponseHeaders)
	if err != nil {
		return resp, err
	}
	if result.statusCode < 200 || result.statusCode >= 300 {
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", result.statusCode, helps.SummarizeErrorBody(result.headers.Get("Content-Type"), result.body))
		err = newCodexStatusErr(result.statusCode, result.body)
		return resp, err
	}
	data := result.body
	if usageOwner {
		reporter.Publish(ctx, helps.ParseOpenAIUsage(data))
		reporter.EnsurePublished(ctx)
		codexAdvanceWindowGeneration(call.prepared.httpReq.Header.Get(codexHeaderSessionID))
	}
	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, originalPayload, body, data, &param)
	resp = cliproxyexecutor.Response{Payload: out}
	if needResponseHeaders {
		resp.Headers = result.headers
	}
	return resp, nil
}

func (e *CodexExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	if opts.Alt == "responses/compact" {
		return nil, statusErr{code: http.StatusBadRequest, msg: "streaming not supported for /responses/compact"}
	}
	needResponseHeaders := needResponseHeadersFromOptions(opts)
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	apiKey, baseURL := codexCreds(auth)
	if baseURL == "" {
		baseURL = "https://chatgpt.com/backend-api/codex"
	}

	reporter := helps.NewUsageReporter(ctx, e.Identifier(), baseModel, auth)
	reporter.CaptureModelReasoningEffort(opts.OriginalRequest, req.Payload)
	defer reporter.TrackFailure(ctx, &err)

	from := opts.SourceFormat
	to := sdktranslator.FromString("codex")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	body, originalTranslated := helps.TranslateRequestWithOriginal(e.cfg, from, to, baseModel, req.Payload, originalPayload, true)

	body, err = thinking.ApplyThinking(body, req.Model, from.String(), to.String(), e.Identifier())
	if err != nil {
		return nil, err
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	body = helps.ApplyPayloadConfigWithRoot(e.cfg, baseModel, to.String(), "", body, originalTranslated, requestedModel)

	url := strings.TrimSuffix(baseURL, "/") + "/responses"
	call, err := e.prepareCodexHTTPCall(ctx, auth, from, executionSessionIDFromOptions(opts), url, req, body, apiKey, true)
	if err != nil {
		return nil, err
	}
	body = call.prepared.body
	helps.RecordAPIRequest(ctx, e.cfg, call.requestLog)

	httpClient := helps.NewCodexFingerprintHTTPClient(ctx, e.cfg, auth, 0)
	httpResp, err := httpClient.Do(call.prepared.httpReq)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return nil, err
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header)
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		data, readErr := helps.ReadErrorResponseBody(httpResp.Body)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("codex executor: close response body error: %v", errClose)
		}
		if readErr != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, readErr)
			return nil, readErr
		}
		helps.AppendAPIResponseChunk(ctx, e.cfg, data)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
		err = newCodexStatusErr(httpResp.StatusCode, data)
		return nil, err
	}
	out := make(chan cliproxyexecutor.StreamChunk, helps.StreamChunkBufferSize)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("codex executor: close response body error: %v", errClose)
			}
		}()
		var param any
		streamState := newCodexStreamCompletionState()
		terminalFailure := false
		emittedPayload := false
		send := func(chunk cliproxyexecutor.StreamChunk) bool {
			if ctx == nil {
				out <- chunk
				return true
			}
			select {
			case out <- chunk:
				return true
			case <-ctx.Done():
				return false
			}
		}
		errRead := helps.ReadStreamLines(httpResp.Body, func(line []byte) error {
			if ctx != nil && ctx.Err() != nil {
				return ctx.Err()
			}
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)

			if eventData, ok := codexEventData(line); ok {
				eventType := codexEventType(eventData)
				if terminalErr, ok := parseCodexStreamTerminalError(eventType, eventData); ok {
					log.Warnf("codex stream terminated with %s: %s", eventType, terminalErr.Error())
					terminalFailure = true
					if !emittedPayload {
						return terminalErr
					}
					return errCodexStopStream
				}
				switch eventType {
				case "response.incomplete":
					// Mirror codex-rs: treat response.incomplete as a terminal
					// failure for telemetry purposes, but keep forwarding the
					// event to the downstream client so SDKs relying on it for
					// signalling (rate limits, safety stops, etc.) still work.
					reason := gjson.GetBytes(eventData, "response.incomplete_details.reason").String()
					if reason == "" {
						reason = "unknown"
					}
					log.Warnf("codex stream terminated with response.incomplete: reason=%s", reason)
					terminalFailure = true
				case "response.failed":
					message := gjson.GetBytes(eventData, "response.error.message").String()
					if message == "" {
						message = "response.failed"
					}
					log.Warnf("codex stream terminated with response.failed: %s", message)
					terminalFailure = true
				}
				if completed, isCompleted := streamState.processEventDataWithType(eventType, eventData, true); isCompleted {
					if detail, ok := helps.ParseCodexUsage(completed.data); ok {
						reporter.Publish(ctx, detail)
					}
					if completed.recoveredCount > 0 {
						log.Warnf(
							"codex stream completed with empty response.output; recovered_items=%d cached_done_items=%d cached_function_calls=%d",
							completed.recoveredCount,
							len(streamState.outputItemsByIndex)+len(streamState.outputItemsFallback),
							len(streamState.functionCallsByItem),
						)
						line = codexSSEDataLine(completed.data)
					}
				}
			}

			chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, originalPayload, body, line, &param)
			for i := range chunks {
				if !send(cliproxyexecutor.StreamChunk{Payload: chunks[i]}) {
					return ctx.Err()
				}
				if len(chunks[i]) > 0 {
					emittedPayload = true
				}
			}
			return nil
		})
		if errRead != nil {
			if errors.Is(errRead, errCodexStopStream) {
				errRead = nil
			}
		}
		if errRead != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errRead)
			reporter.PublishFailure(ctx)
			_ = send(cliproxyexecutor.StreamChunk{Err: errRead})
		} else if terminalFailure {
			reporter.PublishFailure(ctx)
		}
		reporter.EnsurePublished(ctx)
	}()
	var headers http.Header
	if needResponseHeaders {
		headers = httpResp.Header.Clone()
	}
	return &cliproxyexecutor.StreamResult{Headers: headers, Chunks: out}, nil
}

func (e *CodexExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("codex executor: refresh called")
	if auth == nil {
		return nil, statusErr{code: 500, msg: "codex executor: auth is nil"}
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
	svc := e.codexAuthService(auth)
	td, err := svc.RefreshTokensWithRetry(ctx, refreshToken, 3)
	if err != nil {
		return nil, err
	}
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["id_token"] = td.IDToken
	auth.Metadata["access_token"] = td.AccessToken
	if td.RefreshToken != "" {
		auth.Metadata["refresh_token"] = td.RefreshToken
	}
	if td.AccountID != "" {
		auth.Metadata["account_id"] = td.AccountID
	}
	auth.Metadata["email"] = td.Email
	// Use unified key in files
	auth.Metadata["expired"] = td.Expire
	auth.Metadata["type"] = "codex"
	now := time.Now().Format(time.RFC3339)
	auth.Metadata["last_refresh"] = now
	return auth, nil
}

func (e *CodexExecutor) codexAuthService(auth *cliproxyauth.Auth) *codexauth.CodexAuth {
	proxyURL := e.codexAuthProxyURL(auth)
	if cached, ok := e.codexAuthCache.Load(proxyURL); ok {
		if svc, okSvc := cached.(*codexauth.CodexAuth); okSvc {
			return svc
		}
	}

	svc := codexauth.NewCodexAuthWithProxyURL(e.cfg, proxyURL)
	actual, _ := e.codexAuthCache.LoadOrStore(proxyURL, svc)
	if cached, ok := actual.(*codexauth.CodexAuth); ok {
		return cached
	}
	return svc
}

func (e *CodexExecutor) codexAuthProxyURL(auth *cliproxyauth.Auth) string {
	if auth != nil {
		if proxyURL := strings.TrimSpace(auth.ProxyURL); proxyURL != "" {
			return proxyURL
		}
	}
	if e.cfg == nil {
		return ""
	}
	return strings.TrimSpace(e.cfg.ProxyURL)
}

func codexCreds(a *cliproxyauth.Auth) (apiKey, baseURL string) {
	if a == nil {
		return "", ""
	}
	if a.Attributes != nil {
		apiKey = a.Attributes["api_key"]
		baseURL = a.Attributes["base_url"]
	}
	if apiKey == "" && a.Metadata != nil {
		if v, ok := a.Metadata["access_token"].(string); ok {
			apiKey = v
		}
	}
	return
}

func (e *CodexExecutor) resolveCodexConfig(auth *cliproxyauth.Auth) *config.CodexKey {
	if auth == nil || e.cfg == nil {
		return nil
	}
	var attrKey, attrBase string
	if auth.Attributes != nil {
		attrKey = strings.TrimSpace(auth.Attributes["api_key"])
		attrBase = strings.TrimSpace(auth.Attributes["base_url"])
	}
	for i := range e.cfg.CodexKey {
		entry := &e.cfg.CodexKey[i]
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
		for i := range e.cfg.CodexKey {
			entry := &e.cfg.CodexKey[i]
			if strings.EqualFold(strings.TrimSpace(entry.APIKey), attrKey) {
				return entry
			}
		}
	}
	return nil
}
