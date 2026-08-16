package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
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
	openAICompatImageHandlerType            = "openai-image"
	openAICompatImagesGenerationsPath       = "/images/generations"
	openAICompatImagesEditsPath             = "/images/edits"
	openAICompatDefaultImageEndpoint        = openAICompatImagesGenerationsPath
	openAICompatMultipartMemory       int64 = 32 << 20
)

// OpenAICompatExecutor implements a stateless executor for OpenAI-compatible providers.
// It performs request/response translation and executes against the provider base URL
// using per-auth credentials (API key) and per-auth HTTP transport (proxy) from context.
type OpenAICompatExecutor struct {
	provider string
	cfg      *config.Config
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
	httpClient := helps.NewProxyAwareHTTPClient(ctx, e.cfg, auth, 0)
	return httpClient.Do(httpReq)
}

func (e *OpenAICompatExecutor) Execute(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	if endpointPath := openAICompatImageEndpointPath(opts); endpointPath != "" {
		return e.executeImages(ctx, auth, req, opts, endpointPath)
	}

	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	baseURL, apiKey := e.resolveCredentials(auth)
	if baseURL == "" {
		err = statusErr{code: http.StatusUnauthorized, msg: "missing provider baseURL"}
		return
	}

	from := opts.SourceFormat
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	to := sdktranslator.FromString("openai")
	endpoint := "/chat/completions"
	if opts.Alt == "responses/compact" {
		to = sdktranslator.FromString("openai-response")
		endpoint = "/responses/compact"
	}
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	isCompat := helps.APIKeyModelIsCompat(req)
	originalTranslated := helps.TranslateRequestWithAPIKeyModelCompatibility(ctx, opts.Headers, e.cfg, from, to, baseModel, originalPayload, opts.Stream, isCompat)
	translated := helps.TranslateRequestWithAPIKeyModelCompatibility(ctx, opts.Headers, e.cfg, from, to, baseModel, req.Payload, opts.Stream, isCompat)

	translated, err = helps.ApplyRequestThinking(translated, req, opts, from.String(), to.String(), e.Identifier())
	if err != nil {
		return resp, err
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	translated = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), from.String(), "", translated, originalTranslated, requestedModel, requestPath, opts.Headers)
	if helps.ShouldNormalizeOpenAIToolResultsForModel(e.resolveCompatConfig(auth), baseModel, requestedModel) {
		translated = helps.NormalizeOpenAIToolResultsTextOnly(translated)
	}
	if opts.Alt != "responses/compact" {
		translated, err = e.applyPromptCacheKey(ctx, auth, from, baseModel, req, opts, translated)
		if err != nil {
			return resp, err
		}
	}
	if opts.Alt == "responses/compact" {
		if updated, errDelete := sjson.DeleteBytes(translated, "stream"); errDelete == nil {
			translated = updated
		}
		translated = sanitizeOpenAIResponsesReasoningEncryptedContent(ctx, "openai compat executor", translated)
	}
	reporter.SetTranslatedReasoningEffort(translated, to.String())

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
		Body:      translated,
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
			log.Errorf("openai compat executor: close response body error: %v", errClose)
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
	body, err := io.ReadAll(httpResp.Body)
	if err != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, err)
		return resp, err
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, body)
	reporter.Publish(ctx, helps.ParseOpenAIUsage(body))
	// Ensure we at least record the request even if upstream doesn't return usage
	reporter.EnsurePublished(ctx)
	// Translate response back to source format when needed
	var param any
	out := sdktranslator.TranslateNonStream(ctx, to, responseFormat, req.Model, opts.OriginalRequest, translated, body, &param)
	if responseFormat == sdktranslator.FormatOpenAIResponse {
		out = helps.EnsureResponsesUsageDetails(out)
	}
	resp = cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}
	return resp, nil
}

func (e *OpenAICompatExecutor) executeImages(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, endpointPath string) (resp cliproxyexecutor.Response, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	baseURL, apiKey := e.resolveCredentials(auth)
	if baseURL == "" {
		err = statusErr{code: http.StatusUnauthorized, msg: "missing provider baseURL"}
		return resp, err
	}

	payload, contentType, errPrepare := prepareOpenAICompatImagesPayload(req.Payload, baseModel, opts.Headers.Get("Content-Type"), false)
	if errPrepare != nil {
		err = errPrepare
		return resp, err
	}
	if contentType == "" {
		contentType = "application/json"
	}
	reporter.SetTranslatedReasoningEffort(payload, "openai")

	url := strings.TrimSuffix(baseURL, "/") + endpointPath
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return resp, err
	}
	httpReq.Header.Set("Content-Type", contentType)
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	httpReq.Header.Set("User-Agent", "cli-proxy-openai-compat")
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs)
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
		Body:      payload,
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
			log.Errorf("openai compat executor: close response body error: %v", errClose)
		}
	}()
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())

	body, errRead := io.ReadAll(httpResp.Body)
	if errRead != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errRead)
		err = errRead
		return resp, err
	}
	helps.AppendAPIResponseChunk(ctx, e.cfg, body)

	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), body))
		err = statusErr{code: httpResp.StatusCode, msg: string(body)}
		return resp, err
	}

	reporter.Publish(ctx, helps.ParseOpenAIUsage(body))
	reporter.EnsurePublished(ctx)
	resp = cliproxyexecutor.Response{Payload: body, Headers: httpResp.Header.Clone()}
	return resp, nil
}

func (e *OpenAICompatExecutor) ExecuteStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	if endpointPath := openAICompatImageEndpointPath(opts); endpointPath != "" {
		return e.executeImagesStream(ctx, auth, req, opts, endpointPath)
	}

	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	baseURL, apiKey := e.resolveCredentials(auth)
	if baseURL == "" {
		err = statusErr{code: http.StatusUnauthorized, msg: "missing provider baseURL"}
		return nil, err
	}

	from := opts.SourceFormat
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	to := sdktranslator.FromString("openai")
	originalPayloadSource := req.Payload
	if len(opts.OriginalRequest) > 0 {
		originalPayloadSource = opts.OriginalRequest
	}
	originalPayload := originalPayloadSource
	isCompat := helps.APIKeyModelIsCompat(req)
	originalTranslated := helps.TranslateRequestWithAPIKeyModelCompatibility(ctx, opts.Headers, e.cfg, from, to, baseModel, originalPayload, true, isCompat)
	translated := helps.TranslateRequestWithAPIKeyModelCompatibility(ctx, opts.Headers, e.cfg, from, to, baseModel, req.Payload, true, isCompat)

	translated, err = helps.ApplyRequestThinking(translated, req, opts, from.String(), to.String(), e.Identifier())
	if err != nil {
		return nil, err
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	translated = helps.ApplyPayloadConfigWithRequest(e.cfg, baseModel, to.String(), from.String(), "", translated, originalTranslated, requestedModel, requestPath, opts.Headers)
	if helps.ShouldNormalizeOpenAIToolResultsForModel(e.resolveCompatConfig(auth), baseModel, requestedModel) {
		translated = helps.NormalizeOpenAIToolResultsTextOnly(translated)
	}
	if opts.Alt != "responses/compact" {
		translated, err = e.applyPromptCacheKey(ctx, auth, from, baseModel, req, opts, translated)
		if err != nil {
			return nil, err
		}
	}

	// Request usage data in the final streaming chunk so that token statistics
	// are captured even when the upstream is an OpenAI-compatible provider.
	translated = helps.SetBoolIfDifferent(translated, "stream_options.include_usage", true)
	reporter.SetTranslatedReasoningEffort(translated, to.String())

	url := strings.TrimSuffix(baseURL, "/") + "/chat/completions"
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
		Body:      translated,
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
			log.Errorf("openai compat executor: close response body error: %v", errClose)
		}
		err = statusErr{code: httpResp.StatusCode, msg: string(b)}
		return nil, err
	}
	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("openai compat executor: close response body error: %v", errClose)
			}
		}()
		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 52_428_800) // 50MB
		claudeInputTokens := helps.NewClaudeInputTokenState(from, to, responseFormat, originalPayload)
		var param any
		var streamUsage helps.StreamUsageBuffer
		var seenDone bool
		var streamFailed bool
		var streamAborted bool
		var upstreamEvent string
		var frameData [][]byte
		defer streamUsage.Publish(ctx, reporter)

		publishStreamError := func(streamErr statusErr, containsPayload bool) {
			loggedErr := streamErr
			if containsPayload {
				loggedErr = statusErr{code: streamErr.code, msg: "upstream stream returned an error payload"}
			}
			helps.RecordAPIResponseError(ctx, e.cfg, loggedErr)
			reporter.PublishFailure(ctx, loggedErr)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: streamErr}:
			case <-ctx.Done():
			}
			streamFailed = true
		}

		processFrame := func() bool {
			eventName := upstreamEvent
			upstreamEvent = ""
			dataLines := frameData
			frameData = nil
			if len(dataLines) == 0 {
				if openAICompatErrorEvent(eventName) {
					publishStreamError(statusErr{code: http.StatusBadGateway, msg: "upstream error event ended without data"}, false)
					return true
				}
				return false
			}

			if len(dataLines) > 1 {
				for _, dataLine := range dataLines {
					if bytes.Equal(bytes.TrimSpace(dataLine), []byte("[DONE]")) {
						publishStreamError(statusErr{code: http.StatusBadGateway, msg: "upstream stream ended with incomplete data before [DONE]"}, false)
						return true
					}
				}
			}
			dataPayload := bytes.TrimSpace(bytes.Join(dataLines, []byte("\n")))
			isDone := bytes.Equal(dataPayload, []byte("[DONE]"))
			if isDone && openAICompatErrorEvent(eventName) {
				publishStreamError(statusErr{code: http.StatusBadGateway, msg: "upstream error event ended before [DONE]"}, false)
				return true
			}
			if !isDone && !json.Valid(dataPayload) {
				publishStreamError(statusErr{code: http.StatusBadGateway, msg: "upstream stream ended with incomplete SSE data frame"}, false)
				return true
			}
			if !isDone {
				if streamErr, isError := openAICompatStreamDataError(dataPayload, eventName); isError {
					publishStreamError(streamErr, true)
					return true
				}
			}

			streamLine := append([]byte("data: "), dataPayload...)
			chunks := helps.TranslateStreamWithClaudeInputTokens(ctx, to, responseFormat, req.Model, opts.OriginalRequest, translated, streamLine, &param, claudeInputTokens)
			for i := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
				case <-ctx.Done():
					streamAborted = true
					return true
				}
			}
			if isDone {
				seenDone = true
				return true
			}
			return false
		}

	scanLoop:
		for scanner.Scan() {
			line := scanner.Bytes()
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
			streamUsage.ObserveOpenAIStream(line)
			trimmedLine := bytes.TrimSpace(line)
			if len(trimmedLine) == 0 {
				if processFrame() {
					break scanLoop
				}
				continue
			}
			if bytes.HasPrefix(trimmedLine, []byte("data:")) {
				frameData = append(frameData, bytes.Clone(bytes.TrimSpace(trimmedLine[len("data:"):])))
				continue
			}
			if bytes.HasPrefix(trimmedLine, []byte("event:")) {
				upstreamEvent = strings.TrimSpace(string(trimmedLine[len("event:"):]))
				continue
			}
			if bytes.HasPrefix(trimmedLine, []byte(":")) || bytes.HasPrefix(trimmedLine, []byte("id:")) || bytes.HasPrefix(trimmedLine, []byte("retry:")) {
				continue
			}
			if bytes.HasPrefix(trimmedLine, []byte("{")) || bytes.HasPrefix(trimmedLine, []byte("[")) {
				publishStreamError(statusErr{code: http.StatusBadGateway, msg: string(trimmedLine)}, true)
				break
			}
		}
		errScan := scanner.Err()
		if errScan == nil && !seenDone && !streamFailed && !streamAborted && len(frameData) > 0 {
			_ = processFrame()
		}
		if streamFailed || streamAborted {
			return
		}
		if errScan != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errScan)
			reporter.PublishFailure(ctx, errScan)
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: errScan}:
			case <-ctx.Done():
			}
		} else if !seenDone {
			// Responses clients require an explicit terminal event. Treat a clean
			// upstream EOF without [DONE] as a failed stream instead of completing it.
			if responseFormat == sdktranslator.FormatOpenAIResponse {
				streamErr := statusErr{code: http.StatusBadGateway, msg: "upstream stream closed before [DONE]"}
				helps.RecordAPIResponseError(ctx, e.cfg, streamErr)
				reporter.PublishFailure(ctx, streamErr)
				select {
				case out <- cliproxyexecutor.StreamChunk{Err: streamErr}:
				case <-ctx.Done():
				}
				return
			}

			// Other protocols retain compatibility with providers that omit [DONE].
			chunks := helps.TranslateStreamWithClaudeInputTokens(ctx, to, responseFormat, req.Model, opts.OriginalRequest, translated, []byte("data: [DONE]"), &param, claudeInputTokens)
			for i := range chunks {
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunks[i]}:
				case <-ctx.Done():
					return
				}
			}
		}
		// Ensure we record the request if no usage chunk was ever seen.
		streamUsage.Publish(ctx, reporter)
		reporter.EnsurePublished(ctx)
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

func (e *OpenAICompatExecutor) executeImagesStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, endpointPath string) (_ *cliproxyexecutor.StreamResult, err error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	reporter := helps.NewExecutorUsageReporter(ctx, e, baseModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	baseURL, apiKey := e.resolveCredentials(auth)
	if baseURL == "" {
		err = statusErr{code: http.StatusUnauthorized, msg: "missing provider baseURL"}
		return nil, err
	}

	payload, contentType, errPrepare := prepareOpenAICompatImagesPayload(req.Payload, baseModel, opts.Headers.Get("Content-Type"), true)
	if errPrepare != nil {
		err = errPrepare
		return nil, err
	}
	if contentType == "" {
		contentType = "application/json"
	}
	reporter.SetTranslatedReasoningEffort(payload, "openai")

	url := strings.TrimSuffix(baseURL, "/") + endpointPath
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", contentType)
	httpReq.Header.Set("Accept", "text/event-stream")
	httpReq.Header.Set("Cache-Control", "no-cache")
	if apiKey != "" {
		httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	}
	httpReq.Header.Set("User-Agent", "cli-proxy-openai-compat")
	var attrs map[string]string
	if auth != nil {
		attrs = auth.Attributes
	}
	util.ApplyCustomHeadersFromAttrs(httpReq, attrs)
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
		Body:      payload,
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
		body, errRead := io.ReadAll(httpResp.Body)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("openai compat executor: close response body error: %v", errClose)
		}
		if errRead != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errRead)
			return nil, errRead
		}
		helps.AppendAPIResponseChunk(ctx, e.cfg, body)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), body))
		return nil, statusErr{code: httpResp.StatusCode, msg: string(body)}
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("openai compat executor: close response body error: %v", errClose)
			}
			reporter.EnsurePublished(ctx)
		}()
		buffer := make([]byte, 32*1024)
		for {
			n, errRead := httpResp.Body.Read(buffer)
			if n > 0 {
				chunk := bytes.Clone(buffer[:n])
				helps.AppendAPIResponseChunk(ctx, e.cfg, chunk)
				select {
				case out <- cliproxyexecutor.StreamChunk{Payload: chunk}:
				case <-ctx.Done():
					return
				}
			}
			if errRead != nil {
				if errRead != io.EOF {
					helps.RecordAPIResponseError(ctx, e.cfg, errRead)
					reporter.PublishFailure(ctx, errRead)
					select {
					case out <- cliproxyexecutor.StreamChunk{Err: errRead}:
					case <-ctx.Done():
					}
				}
				return
			}
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

func (e *OpenAICompatExecutor) CountTokens(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	baseModel := thinking.ParseSuffix(req.Model).ModelName

	from := opts.SourceFormat
	responseFormat := cliproxyexecutor.ResponseFormatOrSource(opts)
	to := sdktranslator.FromString("openai")
	isCompat := helps.APIKeyModelIsCompat(req)
	translated := helps.TranslateRequestWithAPIKeyModelCompatibility(ctx, opts.Headers, e.cfg, from, to, baseModel, req.Payload, false, isCompat)

	modelForCounting := baseModel

	translated, err := helps.ApplyRequestThinking(translated, req, opts, from.String(), to.String(), e.Identifier())
	if err != nil {
		return cliproxyexecutor.Response{}, err
	}

	enc, err := helps.TokenizerForModel(modelForCounting)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("openai compat executor: tokenizer init failed: %w", err)
	}

	count, err := helps.CountOpenAIChatTokens(enc, translated)
	if err != nil {
		return cliproxyexecutor.Response{}, fmt.Errorf("openai compat executor: token counting failed: %w", err)
	}

	usageJSON := helps.BuildOpenAIUsageJSON(count)
	translatedUsage := sdktranslator.TranslateTokenCount(ctx, to, responseFormat, count, usageJSON)
	return cliproxyexecutor.Response{Payload: translatedUsage}, nil
}

// Refresh is a no-op for API-key based compatibility providers.
// OAuth-style credentials with a refresh token cannot be rotated here; callers
// that need plugin/Home refresh must bind a refresh-capable executor instead.
func (e *OpenAICompatExecutor) Refresh(ctx context.Context, auth *cliproxyauth.Auth) (*cliproxyauth.Auth, error) {
	log.Debugf("openai compat executor: refresh called")
	if refreshed, handled, err := helps.RefreshAuthViaHome(ctx, e.cfg, auth); handled {
		return refreshed, err
	}
	if openAICompatAuthHasRefreshToken(auth) {
		provider := ""
		if e != nil {
			provider = e.Identifier()
		}
		if provider == "" && auth != nil {
			provider = strings.TrimSpace(auth.Provider)
		}
		return nil, fmt.Errorf("openai compat executor cannot refresh oauth credentials for provider %s", provider)
	}
	return auth, nil
}

func openAICompatAuthHasRefreshToken(auth *cliproxyauth.Auth) bool {
	if auth == nil || auth.Metadata == nil {
		return false
	}
	if token, _ := auth.Metadata["refresh_token"].(string); strings.TrimSpace(token) != "" {
		return true
	}
	if token, _ := auth.Metadata["refreshToken"].(string); strings.TrimSpace(token) != "" {
		return true
	}
	return false
}

func openAICompatImageEndpointPath(opts cliproxyexecutor.Options) string {
	if opts.SourceFormat.String() != openAICompatImageHandlerType {
		return ""
	}
	path := helps.PayloadRequestPath(opts)
	if strings.HasSuffix(path, "/images/edits") {
		return openAICompatImagesEditsPath
	}
	if strings.HasSuffix(path, "/images/generations") {
		return openAICompatImagesGenerationsPath
	}
	return openAICompatDefaultImageEndpoint
}

func prepareOpenAICompatImagesPayload(payload []byte, model string, contentType string, stream bool) ([]byte, string, error) {
	model = strings.TrimSpace(model)
	contentType = strings.TrimSpace(contentType)
	if json.Valid(payload) {
		if model != "" {
			payload = helps.SetStringIfDifferent(payload, "model", model)
		}
		if stream {
			payload = helps.SetBoolIfDifferent(payload, "stream", true)
		} else {
			payload, _ = sjson.DeleteBytes(payload, "stream")
		}
		return payload, "application/json", nil
	}

	mediaType, params, errParse := mime.ParseMediaType(contentType)
	if errParse != nil || !strings.HasPrefix(strings.ToLower(strings.TrimSpace(mediaType)), "multipart/") {
		return payload, contentType, nil
	}
	boundary := strings.TrimSpace(params["boundary"])
	if boundary == "" {
		return nil, "", fmt.Errorf("multipart boundary is missing")
	}
	return rewriteOpenAICompatImagesMultipartPayload(payload, model, boundary, stream)
}

func cloneOpenAICompatMIMEHeader(src textproto.MIMEHeader) textproto.MIMEHeader {
	dst := make(textproto.MIMEHeader, len(src))
	for key, values := range src {
		dst[key] = append([]string(nil), values...)
	}
	return dst
}

func rewriteOpenAICompatImagesMultipartPayload(payload []byte, model string, boundary string, stream bool) ([]byte, string, error) {
	reader := multipart.NewReader(bytes.NewReader(payload), boundary)
	form, errRead := reader.ReadForm(openAICompatMultipartMemory)
	if errRead != nil {
		return nil, "", fmt.Errorf("read multipart form failed: %w", errRead)
	}
	defer func() {
		if errRemove := form.RemoveAll(); errRemove != nil {
			log.Errorf("openai compat executor: remove multipart form files error: %v", errRemove)
		}
	}()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if model != "" {
		if errWrite := writer.WriteField("model", model); errWrite != nil {
			return nil, "", fmt.Errorf("write model field failed: %w", errWrite)
		}
	}
	if stream {
		if errWrite := writer.WriteField("stream", "true"); errWrite != nil {
			return nil, "", fmt.Errorf("write stream field failed: %w", errWrite)
		}
	}
	for key, values := range form.Value {
		if key == "model" || key == "stream" {
			continue
		}
		for _, value := range values {
			if errWrite := writer.WriteField(key, value); errWrite != nil {
				return nil, "", fmt.Errorf("write form field %s failed: %w", key, errWrite)
			}
		}
	}
	for key, files := range form.File {
		for _, fileHeader := range files {
			if fileHeader == nil {
				continue
			}
			header := cloneOpenAICompatMIMEHeader(fileHeader.Header)
			header.Set("Content-Disposition", multipart.FileContentDisposition(key, fileHeader.Filename))
			if header.Get("Content-Type") == "" {
				header.Set("Content-Type", "application/octet-stream")
			}
			part, errCreate := writer.CreatePart(header)
			if errCreate != nil {
				return nil, "", fmt.Errorf("create file field %s failed: %w", key, errCreate)
			}
			src, errOpen := fileHeader.Open()
			if errOpen != nil {
				return nil, "", fmt.Errorf("open upload file failed: %w", errOpen)
			}
			_, errCopy := io.Copy(part, src)
			if errClose := src.Close(); errClose != nil {
				log.Errorf("openai compat executor: close upload file error: %v", errClose)
				if errCopy == nil {
					errCopy = errClose
				}
			}
			if errCopy != nil {
				return nil, "", fmt.Errorf("copy upload file failed: %w", errCopy)
			}
		}
	}
	if errClose := writer.Close(); errClose != nil {
		return nil, "", fmt.Errorf("close multipart writer failed: %w", errClose)
	}
	return body.Bytes(), writer.FormDataContentType(), nil
}

func (e *OpenAICompatExecutor) applyPromptCacheKey(ctx context.Context, auth *cliproxyauth.Auth, from sdktranslator.Format, baseModel string, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, translated []byte) ([]byte, error) {
	compat := e.resolveCompatConfig(auth)
	if compat == nil || !compat.SupportPromptCacheKey {
		return translated, nil
	}

	for _, payload := range [][]byte{req.Payload, opts.OriginalRequest, translated} {
		if promptCacheKey := strings.TrimSpace(gjson.GetBytes(payload, "prompt_cache_key").String()); promptCacheKey != "" {
			return helps.SetStringIfDifferent(translated, "prompt_cache_key", promptCacheKey), nil
		}
	}

	modelName := strings.TrimSpace(gjson.GetBytes(translated, "model").String())
	if modelName == "" {
		modelName = baseModel
	}
	if sourceFormatEqual(from, sdktranslator.FormatClaude) {
		cached, ok, errCache := helps.ClaudeCodePromptCache(ctx, modelName, req.Payload, opts.Headers)
		if errCache != nil {
			return translated, errCache
		}
		if ok {
			return helps.SetStringIfDifferent(translated, "prompt_cache_key", cached.ID), nil
		}
	}

	sessionID := helps.ProviderSessionUUID(e.provider, opts.Metadata, req.Metadata)
	if sessionID == "" {
		return translated, nil
	}
	provider := strings.TrimSpace(e.provider)
	if provider == "" {
		provider = strings.TrimSpace(compat.Name)
	}
	identity := strings.Join([]string{
		"cli-proxy-api:openai-compat:prompt-cache",
		strings.ToLower(provider),
		strings.ToLower(modelName),
		strings.ToLower(strings.TrimSpace(from.String())),
		sessionID,
	}, "\x00")
	promptCacheKey := uuid.NewSHA1(uuid.NameSpaceOID, []byte(identity)).String()
	return helps.SetStringIfDifferent(translated, "prompt_cache_key", promptCacheKey), nil
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
	if auth.AuthSourceKind() == cliproxyauth.AuthSourceConfig && auth.Attributes != nil {
		if rawIndex := strings.TrimSpace(auth.Attributes["config_index"]); rawIndex != "" {
			configIndex, errIndex := strconv.Atoi(rawIndex)
			if errIndex == nil && configIndex >= 0 && configIndex < len(e.cfg.OpenAICompatibility) {
				compat := &e.cfg.OpenAICompatibility[configIndex]
				if !compat.Disabled {
					return compat
				}
			}
		}
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
		if compat.Disabled {
			continue
		}
		for _, candidate := range candidates {
			if candidate != "" && strings.EqualFold(strings.TrimSpace(candidate), compat.Name) {
				return compat
			}
		}
	}
	return nil
}

func (e *OpenAICompatExecutor) overrideModel(payload []byte, model string) []byte {
	if len(payload) == 0 || model == "" {
		return payload
	}
	return helps.SetStringIfDifferent(payload, "model", model)
}

func openAICompatErrorEvent(eventName string) bool {
	return strings.EqualFold(eventName, "error") || strings.EqualFold(eventName, "response.error") || strings.EqualFold(eventName, "response.failed")
}

func openAICompatStreamDataError(payload []byte, eventName string) (statusErr, bool) {
	if len(payload) == 0 || !json.Valid(payload) {
		return statusErr{}, false
	}
	payloadType := gjson.GetBytes(payload, "type").String()
	hasError := false
	for _, path := range []string{"error", "response.error"} {
		errorNode := gjson.GetBytes(payload, path)
		if errorNode.Exists() && errorNode.Raw != "null" {
			hasError = true
			break
		}
	}
	hasTopLevelErrorFields := gjson.GetBytes(payload, "code").Exists() && gjson.GetBytes(payload, "message").Exists()
	if !hasError && !strings.EqualFold(payloadType, "error") && !strings.EqualFold(payloadType, "response.error") && !strings.EqualFold(payloadType, "response.failed") &&
		!openAICompatErrorEvent(eventName) && !hasTopLevelErrorFields {
		return statusErr{}, false
	}

	status := 0
	for _, path := range []string{"status", "status_code", "error.status", "error.status_code", "response.error.status", "response.error.status_code"} {
		status = int(gjson.GetBytes(payload, path).Int())
		if status >= http.StatusBadRequest && status <= 599 {
			break
		}
	}
	if status < http.StatusBadRequest || status > 599 {
		status = http.StatusBadGateway
	}
	return statusErr{code: status, msg: string(payload)}, true
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
