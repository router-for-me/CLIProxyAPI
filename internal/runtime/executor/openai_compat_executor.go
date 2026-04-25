package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// OpenAICompatExecutor implements a stateless executor for OpenAI-compatible providers.
// It performs request/response translation and executes against the provider base URL
// using per-auth credentials (API key) and per-auth HTTP transport (proxy) from context.
type OpenAICompatExecutor struct {
	provider string
	cfg      *config.Config
}

const defaultOpenAICompatCircuitBreakerRecoveryTimeoutSec = 60

type openAICompatUpstreamRequest struct {
	URL     string
	Method  string
	Headers http.Header
	Body    []byte
}

// NewOpenAICompatExecutor creates an executor bound to a provider key (e.g., "openrouter").
func NewOpenAICompatExecutor(provider string, cfg *config.Config) *OpenAICompatExecutor {
	return &OpenAICompatExecutor{provider: provider, cfg: cfg}
}

// Identifier implements cliproxyauth.ProviderExecutor.
func (e *OpenAICompatExecutor) Identifier() string { return e.provider }

// PrepareRequest injects OpenAI-compatible credentials into the outgoing HTTP request.
func (e *OpenAICompatExecutor) PrepareRequest(req *http.Request, auth *cliproxyauth.Auth) error {
	if req == nil {
		return nil
	}
	_, apiKey := e.resolveCredentials(auth)
	if strings.TrimSpace(apiKey) != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(req, attrs)
	return nil
}

// HttpRequest injects OpenAI-compatible credentials into the request and executes it.
func (e *OpenAICompatExecutor) HttpRequest(ctx context.Context, auth *cliproxyauth.Auth, req *http.Request) (*http.Response, error) {
	if req == nil {
		return nil, fmt.Errorf("openai compat executor: request is nil")
	}
	if ctx == nil {
		ctx = req.Context()
	}
	httpReq := req.WithContext(ctx)
	if err := e.PrepareRequest(httpReq, auth); err != nil {
		return nil, err
	}
	httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}

func (e *OpenAICompatExecutor) doRequestWithNetworkRetry(ctx context.Context, auth *cliproxyauth.Auth, req openAICompatUpstreamRequest) (*http.Response, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	retries := 0
	baseBackoff := 500 * time.Millisecond
	if e.cfg != nil {
		retries = e.cfg.OpenAICompatNetworkRetry
		if retries < 0 {
			retries = 0
		}
		if e.cfg.OpenAICompatNetworkRetryBackoffMS >= 0 {
			baseBackoff = time.Duration(e.cfg.OpenAICompatNetworkRetryBackoffMS) * time.Millisecond
		}
	}
	attempts := retries + 1

	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}

	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		httpReq, err := http.NewRequestWithContext(ctx, req.Method, req.URL, bytes.NewReader(req.Body))
		if err != nil {
			return nil, err
		}
		httpReq.Header = req.Headers.Clone()

		recordAPIRequest(ctx, e.cfg, upstreamRequestLog{
			URL:       req.URL,
			Method:    req.Method,
			Headers:   httpReq.Header.Clone(),
			Body:      req.Body,
			Provider:  e.Identifier(),
			AuthID:    authID,
			AuthLabel: authLabel,
			AuthType:  authType,
			AuthValue: authValue,
		})

		httpClient := newProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
		httpResp, err := httpClient.Do(httpReq)
		if err == nil {
			return httpResp, nil
		}

		lastErr = err
		recordAPIResponseError(ctx, e.cfg, err)
		if attempt >= retries || !isOpenAICompatRetryableNetworkError(err) {
			return nil, err
		}

		delay := openAICompatNetworkRetryDelay(baseBackoff, attempt)
		logWithRequestID(ctx).Warnf("openai compat executor: transient upstream network error on attempt %d/%d: %v; retrying in %s", attempt+1, attempts, err, delay)
		if err := waitOpenAICompatRetryBackoff(ctx, delay); err != nil {
			return nil, err
		}
	}

	return nil, lastErr
}

func openAICompatNetworkRetryDelay(base time.Duration, attempt int) time.Duration {
	if base <= 0 {
		return 0
	}
	if attempt < 0 {
		attempt = 0
	}
	return base * time.Duration(1<<attempt)
}

func waitOpenAICompatRetryBackoff(ctx context.Context, delay time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func isOpenAICompatRetryableNetworkError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	for _, target := range []error{syscall.ECONNRESET, syscall.ECONNABORTED, syscall.EPIPE} {
		if errors.Is(err, target) {
			return true
		}
	}

	msg := strings.ToLower(err.Error())
	for _, needle := range []string{
		"tls handshake timeout",
		"unexpected eof",
		"connection reset by peer",
		"connection reset",
		"connection closed",
		"use of closed network connection",
		"server closed idle connection",
		"broken pipe",
		"connection aborted",
	} {
		if strings.Contains(msg, needle) {
			return true
		}
	}
	return false
}

func (e *OpenAICompatExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	circuitModel := circuitBreakerModelID(opts, req.Model)

	reporter := newUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.trackFailure(ctx, &err)

	baseURL, apiKey := e.resolveCredentials(auth)
	if baseURL == "" {
		err = statusErr{code: http.StatusUnauthorized, msg: "missing provider baseURL"}
		return
	}

	from := opts.SourceFormat
	isResponseFormat := from == sdktranslator.FormatOpenAIResponse

	// Resolve Responses capability mode for openai-response format (non-compact).
	var responsesMode ResponsesMode
	if isResponseFormat && opts.Alt != "responses/compact" {
		responsesMode = globalResponsesCapabilityResolver.Resolve(auth.ID)
	}

	// Determine target format and endpoint based on mode.
	to := sdktranslator.FromString("openai")
	endpoint := "/chat/completions"
	if opts.Alt == "responses/compact" {
		// Compact endpoint: use native /responses/compact unless explicitly known as chat_fallback.
		compactMode := globalResponsesCapabilityResolver.Resolve(auth.ID)
		if compactMode != ResponsesModeChatFallback {
			// unknown or native: try /responses/compact directly.
			to = sdktranslator.FormatOpenAIResponse
			endpoint = "/responses/compact"
		}
		// chat_fallback: to=openai, endpoint=/chat/completions (translate via chat)
	} else if isResponseFormat && (responsesMode == ResponsesModeNative || responsesMode == ResponsesModeUnknown) {
		to = sdktranslator.FormatOpenAIResponse
		endpoint = "/responses"
	}
	// Default (chat_fallback or non-response-format): to=openai, endpoint=/chat/completions

	// Handle previous_response_id for chat_fallback mode.
	if isResponseFormat && responsesMode == ResponsesModeChatFallback {
		prevID := strings.TrimSpace(gjson.GetBytes(req.Payload, "previous_response_id").String())
		if prevID != "" {
			snapshot, ok := globalResponsesStateStore.Get(prevID)
			if !ok {
				err = statusErr{
					code: http.StatusBadRequest,
					msg:  fmt.Sprintf("chat-only fallback mode: previous_response_id %q not found or expired; incremental tool turns require prior response state", prevID),
				}
				return
			}
			// Rebuild full transcript from snapshot + current request.
			merged, mergeErr := MergeResponsesTranscript(req.Payload, snapshot)
			if mergeErr != nil {
				err = statusErr{code: http.StatusBadRequest, msg: fmt.Sprintf("failed to rebuild transcript: %v", mergeErr)}
				return
			}
			req.Payload = merged
		}
	}

	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayload, opts.Stream)
	translated := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, opts.Stream)
	requestedModel := payloadRequestedModel(opts, req.Model)
	translated = applyPayloadConfigWithRoot(e.cfg, baseModel, to.String(), "", translated, originalTranslated, requestedModel)
	if opts.Alt == "responses/compact" && endpoint == "/responses/compact" {
		if updated, errDelete := sjson.DeleteBytes(translated, "stream"); errDelete == nil {
			translated = updated
		}
	}

	translated, err = thinking.ApplyThinking(translated, req.Model, from.String(), to.String(), e.Identifier(), defaultReasoningEffortOnMissing(e.cfg, e.Identifier(), to.String()))
	if err != nil {
		return resp, err
	}

	url := strings.TrimSuffix(baseURL, "/") + endpoint
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(translated))
	if err != nil {
		return resp, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	httpReq.Header.Set("User-Agent", "cli-proxy-openai-compat")
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs)
	var authID string
	if auth != nil {
		authID = auth.ID
	}
	httpResp, err := e.doRequestWithNetworkRetry(ctx, auth, openAICompatUpstreamRequest{
		URL:     url,
		Method:  http.MethodPost,
		Headers: httpReq.Header.Clone(),
		Body:    translated,
	})
	if err != nil {
		e.recordOpenAICompatFailure(auth, circuitModel)
		return resp, err
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("openai compat executor: close response body error: %v", errClose)
		}
	}()
	recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		appendAPIResponseChunk(ctx, e.cfg, b)
		logWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, summarizeErrorBody(httpResp.Header.Get("Content-Type"), b))

		// Unknown mode: if the error is a capability error, cache as chat_fallback and retry.
		if isResponseFormat && responsesMode == ResponsesModeUnknown && isCapabilityError(httpResp.StatusCode, b) {
			log.Infof("openai compat executor: /responses capability error (status=%d), caching auth=%s as chat_fallback and retrying", httpResp.StatusCode, authID)
			globalResponsesCapabilityResolver.Set(authID, ResponsesModeChatFallback)
			// Retry with chat_fallback mode (do not record circuit breaker for capability errors).
			return e.executeWithResponsesMode(ctx, auth, req, opts, ResponsesModeChatFallback)
		}

		// Record circuit breaker failure only for non-capability errors.
		if !(isResponseFormat && responsesMode == ResponsesModeUnknown && isCapabilityError(httpResp.StatusCode, b)) {
			e.recordOpenAICompatFailure(auth, circuitModel)
		}

		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return resp, err
	}
	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		recordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	appendAPIResponseChunk(ctx, e.cfg, body)
	reporter.publish(ctx, parseOpenAIUsage(body))
	// Ensure we at least record the request even if upstream doesn't return usage
	reporter.ensurePublished(ctx)

	// Cache native mode on first success for unknown.
	if isResponseFormat && responsesMode == ResponsesModeUnknown {
		globalResponsesCapabilityResolver.Set(authID, ResponsesModeNative)
	}

	// Translate response back to source format when needed
	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, body, &param)
	resp = cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}

	// Store response state for chat_fallback mode.
	if isResponseFormat && (responsesMode == ResponsesModeChatFallback || (responsesMode == ResponsesModeUnknown && endpoint == "/chat/completions")) {
		responseID := gjson.GetBytes(out, "id").String()
		if responseID != "" {
			snapshot := ResponsesSnapshot{
				Model:        gjson.GetBytes(out, "model").String(),
				Instructions: gjson.GetBytes(translated, "instructions").String(),
				Input:        json.RawMessage(gjson.GetBytes(translated, "input").Raw),
				Output:       json.RawMessage(gjson.GetBytes(out, "output").Raw),
				CreatedAt:    gjson.GetBytes(out, "created_at").Int(),
			}
			globalResponsesStateStore.Put(responseID, snapshot)
		}
	}

	e.recordOpenAICompatSuccess(auth, circuitModel)
	return resp, nil
}

// executeWithResponsesMode executes a non-streaming request with a pre-determined ResponsesMode,
// used for retrying after capability detection.
func (e *OpenAICompatExecutor) executeWithResponsesMode(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, mode ResponsesMode) (cliproxyexecutor.Response, error) {
	// Override the mode by setting it on the resolver temporarily.
	// The Execute method will pick it up via Resolve().
	if auth != nil {
		globalResponsesCapabilityResolver.Set(auth.ID, mode)
	}
	return e.Execute(ctx, auth, req, opts)
}

func (e *OpenAICompatExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName
	circuitModel := circuitBreakerModelID(opts, req.Model)

	reporter := newUsageReporter(ctx, e.Identifier(), baseModel, auth)
	defer reporter.trackFailure(ctx, &err)

	baseURL, apiKey := e.resolveCredentials(auth)
	if baseURL == "" {
		err = statusErr{code: http.StatusUnauthorized, msg: "missing provider baseURL"}
		return nil, err
	}

	from := opts.SourceFormat
	isResponseFormat := from == sdktranslator.FormatOpenAIResponse

	// Resolve Responses capability mode for openai-response format (non-compact).
	var responsesMode ResponsesMode
	if isResponseFormat && opts.Alt != "responses/compact" {
		responsesMode = globalResponsesCapabilityResolver.Resolve(auth.ID)
	}

	// Determine target format and endpoint based on mode.
	to := sdktranslator.FromString("openai")
	endpoint := "/chat/completions"
	if isResponseFormat && (responsesMode == ResponsesModeNative || responsesMode == ResponsesModeUnknown) {
		to = sdktranslator.FormatOpenAIResponse
		endpoint = "/responses"
	}

	// Handle previous_response_id for chat_fallback mode.
	if isResponseFormat && responsesMode == ResponsesModeChatFallback {
		prevID := strings.TrimSpace(gjson.GetBytes(req.Payload, "previous_response_id").String())
		if prevID != "" {
			snapshot, ok := globalResponsesStateStore.Get(prevID)
			if !ok {
				err = statusErr{
					code: http.StatusBadRequest,
					msg:  fmt.Sprintf("chat-only fallback mode: previous_response_id %q not found or expired; incremental tool turns require prior response state", prevID),
				}
				return nil, err
			}
			merged, mergeErr := MergeResponsesTranscript(req.Payload, snapshot)
			if mergeErr != nil {
				err = statusErr{code: http.StatusBadRequest, msg: fmt.Sprintf("failed to rebuild transcript: %v", mergeErr)}
				return nil, err
			}
			req.Payload = merged
		}
	}

	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	originalTranslated := sdktranslator.TranslateRequest(from, to, baseModel, originalPayload, true)
	translated := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, true)
	requestedModel := payloadRequestedModel(opts, req.Model)
	translated = applyPayloadConfigWithRoot(e.cfg, baseModel, to.String(), "", translated, originalTranslated, requestedModel)

	translated, err = thinking.ApplyThinking(translated, req.Model, from.String(), to.String(), e.Identifier(), defaultReasoningEffortOnMissing(e.cfg, e.Identifier(), to.String()))
	if err != nil {
		return nil, err
	}

	// Request usage data in the final streaming chunk so that token statistics
	// are captured even when the upstream is an OpenAI-compatible provider.
	if to == sdktranslator.FromString("openai") {
		translated, _ = sjson.SetBytes(translated, "stream_options.include_usage", true)
	}

	url := strings.TrimSuffix(baseURL, "/") + endpoint
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(translated))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	httpReq.Header.Set("User-Agent", "cli-proxy-openai-compat")
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs)
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Cache-Control", "no-cache")
	var authID string
	if auth != nil {
		authID = auth.ID
	}
	httpResp, err := e.doRequestWithNetworkRetry(ctx, auth, openAICompatUpstreamRequest{
		URL:     url,
		Method:  http.MethodPost,
		Headers: httpReq.Header.Clone(),
		Body:    translated,
	})
	if err != nil {
		e.recordOpenAICompatFailure(auth, circuitModel)
		return nil, err
	}
	recordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		b, _ := io.ReadAll(httpResp.Body)
		appendAPIResponseChunk(ctx, e.cfg, b)
		logWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, summarizeErrorBody(httpResp.Header.Get("Content-Type"), b))

		// Unknown mode: if the error is a capability error, cache as chat_fallback and retry.
		if isResponseFormat && responsesMode == ResponsesModeUnknown && isCapabilityError(httpResp.StatusCode, b) {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("openai compat executor stream: close response body error: %v", errClose)
			}
			log.Infof("openai compat executor stream: /responses capability error (status=%d), caching auth=%s as chat_fallback and retrying", httpResp.StatusCode, authID)
			globalResponsesCapabilityResolver.Set(authID, ResponsesModeChatFallback)
			return e.executeStreamWithResponsesMode(ctx, auth, req, opts, ResponsesModeChatFallback)
		}

		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("openai compat executor stream: close response body error: %v", errClose)
		}
		e.recordOpenAICompatFailure(auth, circuitModel)
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return nil, err
	}

	// Cache native mode on first success for unknown.
	if isResponseFormat && responsesMode == ResponsesModeUnknown {
		globalResponsesCapabilityResolver.Set(authID, ResponsesModeNative)
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("openai compat executor: close response body error: %v", errClose)
			}
		}()
		var param any
		responsesOutput := from == sdktranslator.FormatOpenAIResponse
		emittedCompleted := false
		sawUpstreamDone := false
		upstreamChunkCount := 0
		upstreamBytes := 0
		toleratedTransientTermination := false

		markRetryableTermination := func(readErr error) bool {
			if !isOpenAICompatRetryableNetworkError(readErr) {
				return false
			}
			toleratedTransientTermination = true
			wrappedErr := fmt.Errorf("openai compat executor stream: tolerated transient upstream termination after %d chunks (%d bytes): %w", upstreamChunkCount, upstreamBytes, readErr)
			recordAPIResponseError(ctx, e.cfg, wrappedErr)
			logWithRequestID(ctx).Warnf("openai compat executor stream: tolerated transient upstream termination after %d chunks (%d bytes): %v", upstreamChunkCount, upstreamBytes, readErr)
			return true
		}

		// State capture for chat_fallback mode: intercept response.completed to store snapshot.
		var capturedCompletedPayload []byte
		var capturedResponseID string
		shouldCaptureState := isResponseFormat && (responsesMode == ResponsesModeChatFallback || endpoint == "/chat/completions")

		emitChunks := func(chunks [][]byte) {
			for i := range chunks {
				payload := chunks[i]
				if len(payload) == 0 {
					continue
				}
				if hasOpenAIResponsesCompletedEvent(payload) {
					emittedCompleted = true
					// Capture state from response.completed event for chat_fallback mode.
					if shouldCaptureState {
						capturedCompletedPayload = extractSSEDataPayload(payload)
						capturedResponseID = gjson.GetBytes(capturedCompletedPayload, "response.id").String()
					}
				}
				out <- cliproxyexecutor.StreamChunk{Payload: payload}
			}
		}
		if to == sdktranslator.FormatOpenAIResponse {
			reader := bufio.NewReader(httpResp.Body)
			for {
				frame, errRead := readCodexSSEFrame(reader)
				if len(frame) > 0 {
					upstreamChunkCount++
					upstreamBytes += len(frame)
					if payload := codexSSEPayload(frame); bytes.Equal(payload, []byte("[DONE]")) {
						sawUpstreamDone = true
					}
					appendAPIResponseChunk(ctx, e.cfg, frame)
					if detail, ok := parseOpenAIStreamUsage(frame); ok {
						reporter.publish(ctx, detail)
					}
					chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, bytes.Clone(frame), &param)
					emitChunks(chunks)
				}
				if errRead == nil {
					continue
				}
				if errRead == io.EOF {
					break
				}
				if markRetryableTermination(errRead) {
					break
				}
				recordAPIResponseError(ctx, e.cfg, errRead)
				e.recordOpenAICompatFailure(auth, circuitModel)
				reporter.publishFailure(ctx)
				out <- cliproxyexecutor.StreamChunk{Err: errRead}
				return
			}
		} else {
			scanner := bufio.NewScanner(httpResp.Body)
			scanner.Buffer(nil, 52_428_800) // 50MB
			for scanner.Scan() {
				line := scanner.Bytes()
				if len(line) > 0 {
					upstreamChunkCount++
					upstreamBytes += len(line)
				}
				appendAPIResponseChunk(ctx, e.cfg, line)
				if detail, ok := parseOpenAIStreamUsage(line); ok {
					reporter.publish(ctx, detail)
				}
				if len(line) == 0 {
					continue
				}

				if !bytes.HasPrefix(line, []byte("data:")) {
					continue
				}
				payload := bytes.TrimSpace(line[len("data:"):])
				if bytes.Equal(payload, []byte("[DONE]")) {
					sawUpstreamDone = true
				}

				// OpenAI-compatible streams are SSE: lines typically prefixed with "data: ".
				// Pass through translator; it yields one or more chunks for the target schema.
				chunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, bytes.Clone(line), &param)
				emitChunks(chunks)
			}
			if errScan := scanner.Err(); errScan != nil {
				if markRetryableTermination(errScan) {
					// Treat transient upstream disconnects as graceful stream end to avoid
					// surfacing unnecessary 500s to clients after partial output was delivered.
				} else {
					recordAPIResponseError(ctx, e.cfg, errScan)
					e.recordOpenAICompatFailure(auth, circuitModel)
					reporter.publishFailure(ctx)
					out <- cliproxyexecutor.StreamChunk{Err: errScan}
					return
				}
			}
		}
		if !responsesOutput && toleratedTransientTermination && !sawUpstreamDone {
			// Best-effort terminal marker for OpenAI chat streams when upstream disconnected
			// after partial output. This helps downstream clients close cleanly.
			doneChunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, []byte("data: [DONE]"), &param)
			emitChunks(doneChunks)
		}
		if responsesOutput && !emittedCompleted {
			// EOF can happen before a finish_reason chunk. Flush DONE to force a terminal
			// response.completed conversion from accumulated stream state.
			doneChunks := sdktranslator.TranslateStream(ctx, to, from, req.Model, opts.OriginalRequest, translated, []byte("data: [DONE]"), &param)
			emitChunks(doneChunks)
		}
		if responsesOutput && !emittedCompleted {
			// Last-resort protocol guard: keep responses API clients from hanging forever
			// when upstream closed cleanly but never emitted terminal events.
			emitChunks(synthesizeOpenAIResponsesCompletion(req.Model))
		}

		// Store response state for chat_fallback mode after stream completes.
		if shouldCaptureState && capturedResponseID != "" && len(capturedCompletedPayload) > 0 {
			snapshot := ResponsesSnapshot{
				Model:        gjson.GetBytes(capturedCompletedPayload, "response.model").String(),
				Instructions: gjson.GetBytes(translated, "instructions").String(),
				Input:        json.RawMessage(gjson.GetBytes(translated, "input").Raw),
				Output:       json.RawMessage(gjson.GetBytes(capturedCompletedPayload, "response.output").Raw),
				CreatedAt:    gjson.GetBytes(capturedCompletedPayload, "response.created_at").Int(),
			}
			globalResponsesStateStore.Put(capturedResponseID, snapshot)
		}

		// Ensure we record the request if no usage chunk was ever seen
		reporter.ensurePublished(ctx)
		e.recordOpenAICompatSuccess(auth, circuitModel)
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

// executeStreamWithResponsesMode executes a streaming request with a pre-determined ResponsesMode,
// used for retrying after capability detection.
func (e *OpenAICompatExecutor) executeStreamWithResponsesMode(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, mode ResponsesMode) (*cliproxyexecutor.StreamResult, error) {
	if auth != nil {
		globalResponsesCapabilityResolver.Set(auth.ID, mode)
	}
	return e.ExecuteStream(ctx, auth, req, opts)
}

func circuitBreakerModelID(opts cliproxyexecutor.Options, fallback string) string {
	model := payloadRequestedModel(opts, fallback)
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	parsed := thinking.ParseSuffix(model)
	baseModel := strings.TrimSpace(parsed.ModelName)
	if baseModel != "" {
		return baseModel
	}
	return model
}

func (e *OpenAICompatExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	from := opts.SourceFormat
	to := sdktranslator.FromString("openai")
	translated := sdktranslator.TranslateRequest(from, to, baseModel, req.Payload, false)

	modelForCounting := baseModel

	translated, err := thinking.ApplyThinking(translated, req.Model, from.String(), to.String(), e.Identifier(), defaultReasoningEffortOnMissing(e.cfg, e.Identifier(), to.String()))
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}

	enc, err := tokenizerForModel(modelForCounting)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("openai compat executor: tokenizer init failed: %w", err)
	}

	count, err := countOpenAIChatTokens(enc, translated)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("openai compat executor: token counting failed: %w", err)
	}

	usageJSON := buildOpenAIUsageJSON(count)
	translatedUsage := sdktranslator.TranslateTokenCount(ctx, to, from, count, usageJSON)
	return cliproxyexecutor.Response{Payload: translatedUsage}, nil
}

// Refresh is a no-op for API-key based compatibility providers.
func (e *OpenAICompatExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("openai compat executor: refresh called")
	_ = ctx
	return auth, nil
}

func (e *OpenAICompatExecutor) resolveCredentials(auth *cliproxyauth.Auth) (baseURL, apiKey string) {
	if auth == nil {
		return "", ""
	}
	if auth.Attributes != nil {
		baseURL = strings.TrimSpace(auth.Attributes["base_url"])
		apiKey = strings.TrimSpace(auth.Attributes["api_key"])
	}
	return
}

func (e *OpenAICompatExecutor) resolveCompatConfig(auth *cliproxyauth.Auth) *config.OpenAICompatibility {
	if auth == nil || e.cfg == nil {
		return nil
	}
	candidates := make([]string, 0, 3)
	if auth.Attributes != nil {
		if v := strings.TrimSpace(auth.Attributes["compat_name"]); v != "" {
			candidates = append(candidates, v)
		}
		if v := strings.TrimSpace(auth.Attributes["provider_key"]); v != "" {
			candidates = append(candidates, v)
		}
	}
	if v := strings.TrimSpace(auth.Provider); v != "" {
		candidates = append(candidates, v)
	}
	for i := range e.cfg.OpenAICompatibility {
		compat := &e.cfg.OpenAICompatibility[i]
		for _, candidate := range candidates {
			if candidate != "" && strings.EqualFold(strings.TrimSpace(candidate), compat.Name) {
				return compat
			}
		}
	}
	return nil
}

func (e *OpenAICompatExecutor) openAICompatCircuitBreakerSettings(auth *cliproxyauth.Auth) (int, int) {
	threshold := registry.DefaultCircuitBreakerFailureThreshold
	timeoutSec := defaultOpenAICompatCircuitBreakerRecoveryTimeoutSec
	if cfg := e.resolveCompatConfig(auth); cfg != nil {
		if cfg.CircuitBreakerFailureThreshold > 0 {
			threshold = cfg.CircuitBreakerFailureThreshold
		}
		if cfg.CircuitBreakerRecoveryTimeout > 0 {
			timeoutSec = cfg.CircuitBreakerRecoveryTimeout
		}
	}
	return threshold, timeoutSec
}

func (e *OpenAICompatExecutor) recordOpenAICompatFailure(auth *cliproxyauth.Auth, model string) {
	if auth == nil || auth.ID == "" || strings.TrimSpace(model) == "" {
		return
	}
	threshold, timeoutSec := e.openAICompatCircuitBreakerSettings(auth)
	registry.GetGlobalRegistry().RecordFailure(auth.ID, model, threshold, timeoutSec)
}

func (e *OpenAICompatExecutor) recordOpenAICompatSuccess(auth *cliproxyauth.Auth, model string) {
	if auth == nil || auth.ID == "" || strings.TrimSpace(model) == "" {
		return
	}
	registry.GetGlobalRegistry().RecordSuccess(auth.ID, model)
}

func (e *OpenAICompatExecutor) overrideModel(payload []byte, model string) []byte {
	if len(payload) == 0 || model == "" {
		return payload
	}
	payload, _ = sjson.SetBytes(payload, "model", model)
	return payload
}

func hasOpenAIResponsesCreatedEvent(chunk []byte) bool {
	return hasOpenAIResponsesEventType(chunk, "response.created")
}

func hasOpenAIResponsesCompletedEvent(chunk []byte) bool {
	return hasOpenAIResponsesEventType(chunk, "response.completed")
}

// extractSSEDataPayload extracts the JSON payload from an SSE frame like:
//
//	event: response.completed\n
//	data: {"type":"response.completed",...}\n
//
// It returns just the JSON object after "data: ".
func extractSSEDataPayload(sseFrame []byte) []byte {
	lines := bytes.Split(sseFrame, []byte("\n"))
	for _, line := range lines {
		line = bytes.TrimSpace(line)
		if bytes.HasPrefix(line, []byte("data:")) {
			payload := bytes.TrimSpace(line[len("data:"):])
			if len(payload) > 0 && !bytes.Equal(payload, []byte("[DONE]")) {
				return payload
			}
		}
	}
	// If no SSE framing, return as-is (might be raw JSON).
	return bytes.TrimSpace(sseFrame)
}

func hasOpenAIResponsesEventType(chunk []byte, eventType string) bool {
	if strings.TrimSpace(eventType) == "" {
		return false
	}
	trimmed := bytes.TrimSpace(chunk)
	if len(trimmed) == 0 {
		return false
	}
	lines := bytes.Split(trimmed, []byte("\n"))
	for _, line := range lines {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		if bytes.HasPrefix(line, []byte("event:")) {
			sseEventType := strings.TrimSpace(string(bytes.TrimSpace(line[len("event:"):])))
			if sseEventType == eventType {
				return true
			}
			continue
		}
		if bytes.HasPrefix(line, []byte("data:")) {
			payload := bytes.TrimSpace(line[len("data:"):])
			if len(payload) == 0 || bytes.Equal(payload, []byte("[DONE]")) {
				continue
			}
			if gjson.GetBytes(payload, "type").String() == eventType {
				return true
			}
		}
	}
	if gjson.GetBytes(trimmed, "type").String() == eventType {
		return true
	}
	return false
}

func synthesizeOpenAIResponsesCompletion(modelName string) [][]byte {
	responseID := fmt.Sprintf("resp_synth_%d", time.Now().UnixNano())
	createdAt := time.Now().Unix()
	created := []byte(`{"type":"response.created","sequence_number":1,"response":{"id":"","object":"response","created_at":0,"status":"in_progress","background":false,"error":null,"output":[]}}`)
	completed := []byte(`{"type":"response.completed","sequence_number":2,"response":{"id":"","object":"response","created_at":0,"status":"completed","background":false,"error":null,"output":[],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}}`)
	created, _ = sjson.SetBytes(created, "response.id", responseID)
	completed, _ = sjson.SetBytes(completed, "response.id", responseID)
	created, _ = sjson.SetBytes(created, "response.created_at", createdAt)
	completed, _ = sjson.SetBytes(completed, "response.created_at", createdAt)
	if strings.TrimSpace(modelName) != "" {
		created, _ = sjson.SetBytes(created, "response.model", modelName)
		completed, _ = sjson.SetBytes(completed, "response.model", modelName)
	}
	createdChunk := make([]byte, 0, len(created)+32)
	createdChunk = append(createdChunk, "event: response.created\n"...)
	createdChunk = append(createdChunk, "data: "...)
	createdChunk = append(createdChunk, created...)
	createdChunk = append(createdChunk, '\n', '\n')
	completedChunk := make([]byte, 0, len(completed)+36)
	completedChunk = append(completedChunk, "event: response.completed\n"...)
	completedChunk = append(completedChunk, "data: "...)
	completedChunk = append(completedChunk, completed...)
	completedChunk = append(completedChunk, '\n', '\n')
	return [][]byte{createdChunk, completedChunk}
}

type statusErr struct {
	code       int
	msg        string
	retryAfter *time.Duration
}

func (e statusErr) Error() string {
	if e.msg != "" {
		return e.msg
	}
	return fmt.Sprintf("status %d", e.code)
}
func (e statusErr) StatusCode() int            { return e.code }
func (e statusErr) RetryAfter() *time.Duration { return e.retryAfter }
