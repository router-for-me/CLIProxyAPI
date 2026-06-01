package executor

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/executor/helps"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/proxyutil"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v7/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	codexOpenAIImageSourceFormat = "openai-image"
	codexImagesGenerationsPath   = "/v1/images/generations"
	codexImagesEditsPath         = "/v1/images/edits"
	codexOpenAIImagesMainModel   = "gpt-5.4-mini"

	codexImageSSEMissingCompleted = "missing_response_completed"
	codexImageSSEStreamClosed     = "upstream_stream_closed"
	codexImageSSEReadError        = "read_error"
	codexImageSSEH2Reset          = "h2_stream_reset"
	codexImageSSEContextTimeout   = "context_timeout"
	codexImageSSEContextCanceled  = "context_canceled"
	codexImageSSEUpstreamError    = "upstream_error_event"

	codexStatusClientClosedRequest = 499
)

type codexOpenAIImagePreparedRequest struct {
	Body           []byte
	ResponseFormat string
	StreamPrefix   string
}

type codexImageCallResult struct {
	Result        string
	RevisedPrompt string
	OutputFormat  string
	Size          string
	Background    string
	Quality       string
}

func isCodexOpenAIImageRequest(opts cliproxyexecutor.Options) bool {
	if !strings.EqualFold(strings.TrimSpace(opts.SourceFormat.String()), codexOpenAIImageSourceFormat) {
		return false
	}
	return codexIsImagesEndpointPath(helps.PayloadRequestPath(opts))
}

func codexIsImagesEndpointPath(path string) bool {
	path = strings.TrimSpace(path)
	if path == codexImagesGenerationsPath || path == codexImagesEditsPath {
		return true
	}
	return strings.HasSuffix(path, codexImagesGenerationsPath) || strings.HasSuffix(path, codexImagesEditsPath)
}

func (e *CodexExecutor) resolveGPTImage2BaseModel() string {
	if e == nil || e.cfg == nil {
		return codexOpenAIImagesMainModel
	}
	model := strings.TrimSpace(e.cfg.GPTImage2BaseModel)
	if model == "" {
		return codexOpenAIImagesMainModel
	}
	if strings.HasPrefix(strings.ToLower(model), "gpt-") {
		return model
	}
	return codexOpenAIImagesMainModel
}

func (e *CodexExecutor) executeOpenAIImage(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (resp cliproxyexecutor.Response, err error) {
	prepared, errPrepare := codexPrepareOpenAIImageRequest(req, opts)
	if errPrepare != nil {
		return resp, errPrepare
	}

	apiKey, baseURL := codexCreds(auth)
	if baseURL == "" {
		baseURL = "https://chatgpt.com/backend-api/codex"
	}

	mainModel := e.resolveGPTImage2BaseModel()
	reporter := helps.NewUsageReporter(ctx, e.Identifier(), mainModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	body, errBuild := e.prepareCodexOpenAIImageBody(prepared.Body, req, opts, mainModel)
	if errBuild != nil {
		return resp, errBuild
	}
	reporter.SetTranslatedReasoningEffort(body, "codex")

	url := strings.TrimSuffix(baseURL, "/") + "/responses"
	httpReq, errCache := e.cacheHelper(ctx, sdktranslator.FromString(codexOpenAIImageSourceFormat), url, req, body)
	if errCache != nil {
		return resp, errCache
	}
	applyCodexHeaders(httpReq, auth, apiKey, true, e.cfg)
	recordCodexOpenAIImageRequest(ctx, e.cfg, e.Identifier(), auth, url, httpReq.Header.Clone(), body)

	httpClient := e.newCodexOpenAIImageHTTPClient(ctx, auth)
	httpClient = reporter.TrackHTTPClient(httpClient)
	httpResp, errDo := httpClient.Do(httpReq)
	if errDo != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errDo)
		return resp, errDo
	}
	defer func() {
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("codex executor: close response body error: %v", errClose)
		}
	}()

	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		data, errRead := io.ReadAll(httpResp.Body)
		if errRead != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errRead)
			return resp, errRead
		}
		helps.AppendAPIResponseChunk(ctx, e.cfg, data)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
		err = newCodexStatusErr(httpResp.StatusCode, data)
		return resp, err
	}

	outputItemsByIndex := make(map[int64][]byte)
	var outputItemsFallback [][]byte
	eventData, errStream := codexReadOpenAIImageResponsesSSEWithHeaders(ctx, httpResp.Body, httpResp.Header, outputItemsByIndex, &outputItemsFallback)
	if errStream != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errStream)
		return resp, errStream
	}
	if detail, ok := helps.ParseCodexUsage(eventData); ok {
		reporter.Publish(ctx, detail)
	}
	publishCodexImageToolUsage(ctx, reporter, body, eventData)
	completedData := patchCodexCompletedOutput(eventData, outputItemsByIndex, outputItemsFallback)
	results, createdAt, usageRaw, firstMeta, errExtract := codexExtractImagesFromResponsesCompleted(completedData)
	if errExtract != nil {
		return resp, errExtract
	}
	if len(results) == 0 {
		reason := codexImageCompletedWithoutOutputReason(completedData)
		logCodexImageCompletedWithoutOutput(ctx, completedData, reason)
		return resp, statusErr{code: http.StatusBadGateway, msg: "upstream completed without image output: " + reason}
	}
	out, errOutput := codexBuildImagesAPIResponse(results, createdAt, usageRaw, firstMeta, prepared.ResponseFormat)
	if errOutput != nil {
		return resp, errOutput
	}
	return cliproxyexecutor.Response{Payload: out, Headers: httpResp.Header.Clone()}, nil
}

type codexImageSSEStats struct {
	startedAt            time.Time
	sawResponseCompleted bool
	sawFirstEvent        bool
	sawErrorEvent        bool
	lastEventType        string
	lastDataType         string
	eventCount           int
	dataCount            int
	imageCount           int
	partialImageCount    int
	streamEndReason      string
	readErrorType        string
	upstream             codexImageSSEUpstreamErrorSummary
}

type codexImageSSEEvent struct {
	eventType string
	dataLines [][]byte
	hasEvent  bool
}

type codexImageSSEUpstreamErrorSummary struct {
	eventType        string
	dataType         string
	errorType        string
	errorCode        string
	errorStatus      string
	errorParam       string
	errorReason      string
	incompleteReason string
	failedReason     string
	upstreamResponse string
	upstreamRequest  string
	retryAfter       string
	responseID       string
	errorCategory    string
}

func codexReadOpenAIImageResponsesSSE(ctx context.Context, r io.Reader, outputItemsByIndex map[int64][]byte, outputItemsFallback *[][]byte) ([]byte, error) {
	return codexReadOpenAIImageResponsesSSEWithHeaders(ctx, r, nil, outputItemsByIndex, outputItemsFallback)
}

func (e *CodexExecutor) newCodexOpenAIImageHTTPClient(ctx context.Context, auth *cliproxyauth.Auth) *http.Client {
	var cfg *config.Config
	if e != nil {
		cfg = e.cfg
	}
	httpClient := helps.NewProxyAwareHTTPClient(ctx, cfg, auth, 0)
	if cfg == nil || !cfg.Codex.DisableHTTP2 {
		return httpClient
	}
	if httpClient.Transport == nil {
		if transport, ok := http.DefaultTransport.(*http.Transport); ok && transport != nil {
			httpClient.Transport = proxyutil.CloneTransportWithHTTP11(transport)
		} else {
			transport := &http.Transport{}
			proxyutil.DisableHTTP2ForTransport(transport)
			httpClient.Transport = transport
		}
		return httpClient
	}
	if transport, ok := httpClient.Transport.(*http.Transport); ok && transport != nil {
		httpClient.Transport = proxyutil.CloneTransportWithHTTP11(transport)
	}
	return httpClient
}

func codexReadOpenAIImageResponsesSSEWithHeaders(ctx context.Context, r io.Reader, headers http.Header, outputItemsByIndex map[int64][]byte, outputItemsFallback *[][]byte) ([]byte, error) {
	stats := &codexImageSSEStats{startedAt: time.Now()}
	stats.captureSafeHeaders(headers)
	reader := bufio.NewReader(r)
	var event codexImageSSEEvent

	dispatch := func() ([]byte, bool, error) {
		if !event.hasPending() {
			event.reset()
			return nil, false, nil
		}
		stats.sawFirstEvent = true
		stats.eventCount++
		stats.lastEventType = codexImageSafeSummaryValue(event.eventType)

		eventData := bytes.TrimSpace(event.data())
		dataType := ""
		if len(eventData) > 0 {
			dataType = strings.TrimSpace(gjson.GetBytes(eventData, "type").String())
			stats.lastDataType = codexImageSafeSummaryValue(dataType)
		}
		if codexIsImageSSEUpstreamError(event.eventType, dataType) {
			stats.sawErrorEvent = true
			stats.streamEndReason = codexImageSSEUpstreamError
			stats.captureUpstreamError(event.eventType, dataType, eventData)
			return nil, false, stats.statusErr(codexImageSSEUpstreamError, http.StatusBadGateway)
		}
		if len(eventData) > 0 {
			switch dataType {
			case "response.output_item.done":
				stats.captureImageOutput(eventData)
				collectCodexOutputItemDone(eventData, outputItemsByIndex, outputItemsFallback)
			case "response.image_generation_call.partial_image":
				stats.partialImageCount++
			case "response.completed":
				stats.sawResponseCompleted = true
				return eventData, true, nil
			}
		}
		event.reset()
		return nil, false, nil
	}

	for {
		line, errRead := reader.ReadBytes('\n')
		if len(line) > 0 {
			codexConsumeImageSSELine(line, &event, stats)
			if codexIsBlankSSELine(line) {
				completedData, done, errDispatch := dispatch()
				if done || errDispatch != nil {
					return completedData, errDispatch
				}
			}
		}
		if errRead == nil {
			continue
		}
		if errors.Is(errRead, io.EOF) {
			completedData, done, errDispatch := dispatch()
			if done || errDispatch != nil {
				return completedData, errDispatch
			}
			if classification, code, streamEndReason, readErrorType, ok := codexClassifyImageStreamContext(ctx, nil); ok {
				stats.streamEndReason = streamEndReason
				stats.readErrorType = readErrorType
				return nil, stats.statusErr(classification, code)
			}
			stats.streamEndReason = "eof"
			return nil, stats.statusErr(codexImageSSEMissingCompleted, http.StatusBadGateway)
		}

		completedData, done, errDispatch := dispatch()
		if done || errDispatch != nil {
			return completedData, errDispatch
		}
		classification, code, streamEndReason, readErrorType := codexClassifyImageStreamReadError(ctx, errRead)
		stats.streamEndReason = streamEndReason
		stats.readErrorType = readErrorType
		return nil, stats.statusErr(classification, code)
	}
}

func (s *codexImageSSEStats) captureSafeHeaders(headers http.Header) {
	if s == nil || headers == nil {
		return
	}
	s.upstream.retryAfter = codexImageSafeSummaryValue(headers.Get("Retry-After"))
	s.upstream.upstreamRequest = codexImageFirstSafeHeader(headers,
		"X-Request-Id",
		"Openai-Request-Id",
		"X-Openai-Request-Id",
		"Cf-Ray",
	)
}

func codexImageFirstSafeHeader(headers http.Header, names ...string) string {
	for _, name := range names {
		if value := codexImageSafeSummaryValue(headers.Get(name)); value != "" {
			return value
		}
	}
	return ""
}

func (s *codexImageSSEStats) captureImageOutput(payload []byte) {
	if s == nil || len(payload) == 0 {
		return
	}
	item := gjson.GetBytes(payload, "item")
	if item.Get("type").String() == "image_generation_call" {
		s.imageCount++
		return
	}
	if gjson.GetBytes(payload, "type").String() == "image_generation_call" {
		s.imageCount++
	}
}

func (s *codexImageSSEStats) captureUpstreamError(eventType string, dataType string, payload []byte) {
	if s == nil {
		return
	}
	summary := &s.upstream
	summary.eventType = codexImageSafeSummaryValue(eventType)
	summary.dataType = codexImageSafeSummaryValue(dataType)
	summary.errorType = codexImagePayloadFirstSafeValue(payload,
		"error.type",
		"response.error.type",
	)
	summary.errorCode = codexImagePayloadFirstSafeValue(payload,
		"error.code",
		"response.error.code",
		"code",
	)
	summary.errorStatus = codexImagePayloadFirstSafeValue(payload,
		"error.status",
		"error.status_code",
		"response.error.status",
		"response.error.status_code",
		"status",
		"response.status",
	)
	summary.errorParam = codexImagePayloadFirstSafeValue(payload,
		"error.param",
		"response.error.param",
		"param",
	)
	summary.errorReason = codexImagePayloadFirstSafeValue(payload,
		"error.reason",
		"response.error.reason",
		"reason",
	)
	summary.incompleteReason = codexImagePayloadFirstSafeValue(payload,
		"response.incomplete_details.reason",
		"incomplete_details.reason",
		"response.incomplete_reason",
		"incomplete_reason",
	)
	summary.failedReason = codexImagePayloadFirstSafeValue(payload,
		"response.failed_reason",
		"failed_reason",
		"response.status_details.reason",
	)
	if summary.failedReason == "" && strings.EqualFold(dataType, "response.failed") {
		summary.failedReason = firstNonEmpty(summary.errorReason, summary.errorCode, summary.errorType)
	}
	if summary.incompleteReason == "" && strings.EqualFold(dataType, "response.incomplete") {
		summary.incompleteReason = firstNonEmpty(summary.errorReason, summary.errorCode, summary.errorType)
	}
	summary.upstreamResponse = codexImagePayloadFirstSafeValue(payload,
		"response.id",
		"id",
	)
	summary.responseID = summary.upstreamResponse
	summary.errorCategory = codexImageClassifyUpstreamError(eventType, dataType, payload, *summary)
}

func codexImagePayloadFirstSafeValue(payload []byte, paths ...string) string {
	if len(payload) == 0 {
		return ""
	}
	for _, path := range paths {
		result := gjson.GetBytes(payload, path)
		if !result.Exists() || result.Type == gjson.Null {
			continue
		}
		if value := codexImageSafeSummaryValue(result.String()); value != "" {
			return value
		}
	}
	return ""
}

func codexImagePayloadFirstRawValue(payload []byte, paths ...string) string {
	if len(payload) == 0 {
		return ""
	}
	for _, path := range paths {
		result := gjson.GetBytes(payload, path)
		if !result.Exists() || result.Type == gjson.Null {
			continue
		}
		if value := strings.TrimSpace(result.String()); value != "" {
			return value
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func codexImageClassifyUpstreamError(eventType string, dataType string, payload []byte, summary codexImageSSEUpstreamErrorSummary) string {
	status := strings.TrimSpace(summary.errorStatus)
	structuredText := strings.ToLower(strings.Join([]string{
		eventType,
		dataType,
		summary.errorType,
		summary.errorCode,
		summary.errorStatus,
		summary.errorReason,
		summary.incompleteReason,
		summary.failedReason,
	}, " "))
	messageText := strings.ToLower(codexImagePayloadFirstRawValue(payload, "error.message", "response.error.message", "message"))

	switch {
	case codexImageContainsAny(structuredText, "quota", "insufficient_quota", "usage_limit", "usage limit"):
		return "quota"
	case codexImageContainsAny(structuredText, "capacity"):
		return "capacity"
	case codexImageContainsAny(structuredText, "overloaded", "overload"):
		return "overloaded"
	case codexImageContainsAny(structuredText, "rate_limit", "rate limit", "too_many_requests", "too many requests") || status == "429":
		return "rate_limit"
	case codexImageContainsAny(structuredText, "safety", "policy", "content_filter", "content filter"):
		return "safety"
	case codexImageContainsAny(structuredText, "timeout", "deadline"):
		return "timeout"
	case codexImageContainsAny(structuredText, "unauthorized", "forbidden", "authentication", "auth") || status == "401" || status == "403":
		return "auth"
	case codexImageContainsAny(structuredText, "invalid_request", "invalid request", "bad_request", "bad request", "unprocessable", "malformed") || status == "400" || status == "422":
		return "invalid_request"
	case strings.EqualFold(dataType, "response.failed"):
		return "upstream_failed"
	case strings.EqualFold(dataType, "response.incomplete"):
		return "upstream_incomplete"
	case codexImageContainsAny(structuredText, "internal_error", "internal error", "server_error", "server error") || strings.HasPrefix(status, "5"):
		return "internal_error"
	case codexImageContainsAny(messageText, "quota", "insufficient_quota", "usage_limit", "usage limit"):
		return "quota"
	case codexImageContainsAny(messageText, "capacity"):
		return "capacity"
	case codexImageContainsAny(messageText, "overloaded", "overload"):
		return "overloaded"
	case codexImageContainsAny(messageText, "rate_limit", "rate limit", "too_many_requests", "too many requests"):
		return "rate_limit"
	case codexImageContainsAny(messageText, "safety", "policy", "content_filter", "content filter"):
		return "safety"
	case codexImageContainsAny(messageText, "timeout", "deadline"):
		return "timeout"
	case codexImageContainsAny(messageText, "unauthorized", "forbidden", "authentication"):
		return "auth"
	case codexImageContainsAny(messageText, "invalid_request", "invalid request", "bad_request", "bad request", "unprocessable", "malformed"):
		return "invalid_request"
	case codexImageContainsAny(messageText, "internal_error", "internal error", "server_error", "server error"):
		return "internal_error"
	default:
		return "unknown"
	}
}

func codexImageContainsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}

func codexConsumeImageSSELine(line []byte, event *codexImageSSEEvent, stats *codexImageSSEStats) {
	line = bytes.TrimRight(line, "\r\n")
	if len(line) == 0 || bytes.HasPrefix(line, []byte(":")) {
		return
	}

	field, value := codexSplitSSEField(line)
	switch field {
	case "event":
		event.eventType = strings.TrimSpace(string(value))
		event.hasEvent = true
	case "data":
		stats.dataCount++
		copied := make([]byte, len(value))
		copy(copied, value)
		event.dataLines = append(event.dataLines, copied)
	case "id", "retry":
		return
	default:
		return
	}
}

func codexSplitSSEField(line []byte) (string, []byte) {
	fieldBytes, value, ok := bytes.Cut(line, []byte(":"))
	if !ok {
		return string(line), nil
	}
	if len(value) > 0 && value[0] == ' ' {
		value = value[1:]
	}
	return string(fieldBytes), value
}

func codexIsBlankSSELine(line []byte) bool {
	return len(bytes.TrimSpace(line)) == 0
}

func (e *codexImageSSEEvent) hasPending() bool {
	return e != nil && (e.hasEvent || len(e.dataLines) > 0)
}

func (e *codexImageSSEEvent) data() []byte {
	if e == nil || len(e.dataLines) == 0 {
		return nil
	}
	return bytes.Join(e.dataLines, []byte("\n"))
}

func (e *codexImageSSEEvent) reset() {
	if e == nil {
		return
	}
	e.eventType = ""
	e.dataLines = nil
	e.hasEvent = false
}

func codexIsImageSSEUpstreamError(eventType string, dataType string) bool {
	if strings.EqualFold(strings.TrimSpace(eventType), "error") {
		return true
	}
	switch dataType {
	case "error", "response.failed", "response.incomplete":
		return true
	default:
		return false
	}
}

func codexClassifyImageStreamReadError(ctx context.Context, err error) (classification string, code int, streamEndReason string, readErrorType string) {
	if classification, code, streamEndReason, readErrorType, ok := codexClassifyImageStreamContext(ctx, err); ok {
		return classification, code, streamEndReason, readErrorType
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		return codexImageSSEStreamClosed, http.StatusBadGateway, "unexpected_eof", codexImageSSEStreamClosed
	}
	if codexIsHTTP2StreamResetError(err) {
		return codexImageSSEH2Reset, http.StatusBadGateway, "read_error", codexImageSSEH2Reset
	}
	return codexImageSSEReadError, http.StatusBadGateway, "read_error", codexImageSSEReadError
}

func codexClassifyImageStreamContext(ctx context.Context, err error) (classification string, code int, streamEndReason string, readErrorType string, ok bool) {
	if ctx != nil {
		ctxErr := ctx.Err()
		if errors.Is(ctxErr, context.DeadlineExceeded) {
			return codexImageSSEContextTimeout, http.StatusGatewayTimeout, codexImageSSEContextTimeout, "context_deadline_exceeded", true
		}
		if errors.Is(ctxErr, context.Canceled) {
			return codexImageSSEContextCanceled, codexStatusClientClosedRequest, codexImageSSEContextCanceled, "context_canceled", true
		}
	}
	if errors.Is(err, context.DeadlineExceeded) || codexIsTimeoutError(err) {
		return codexImageSSEContextTimeout, http.StatusGatewayTimeout, codexImageSSEContextTimeout, "context_deadline_exceeded", true
	}
	if errors.Is(err, context.Canceled) {
		return codexImageSSEContextCanceled, codexStatusClientClosedRequest, codexImageSSEContextCanceled, "context_canceled", true
	}
	return "", 0, "", "", false
}

func codexIsTimeoutError(err error) bool {
	if err == nil {
		return false
	}
	var timeout interface{ Timeout() bool }
	if errors.As(err, &timeout) && timeout.Timeout() {
		return true
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "context deadline exceeded") ||
		strings.Contains(lower, "deadline exceeded") ||
		strings.Contains(lower, "timeout")
}

func codexIsHTTP2StreamResetError(err error) bool {
	if err == nil {
		return false
	}
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "stream id") ||
		strings.Contains(lower, "internal_error") ||
		strings.Contains(lower, "received from peer") ||
		strings.Contains(lower, "http2") ||
		strings.Contains(lower, "rst_stream")
}

func (s *codexImageSSEStats) statusErr(classification string, code int) statusErr {
	if s == nil {
		return statusErr{code: code, msg: fmt.Sprintf("codex image stream error: classification=%s", classification)}
	}
	elapsedMS := int64(0)
	if !s.startedAt.IsZero() {
		elapsedMS = time.Since(s.startedAt).Milliseconds()
	}
	return statusErr{
		code: code,
		msg: fmt.Sprintf(
			"codex image stream error: classification=%s saw_response_completed=%t saw_first_event=%t saw_error_event=%t last_event_type=%s last_data_type=%s upstream_event_type=%s upstream_data_type=%s upstream_error_type=%s upstream_error_code=%s upstream_error_status=%s upstream_error_param=%s upstream_error_reason=%s upstream_incomplete_reason=%s upstream_failed_reason=%s upstream_response_id=%s upstream_request_id=%s retry_after=%s response_id=%s error_category=%s event_count=%d data_count=%d image_count=%d partial_image_count=%d elapsed_ms=%d stream_end_reason=%s read_error_type=%s",
			classification,
			s.sawResponseCompleted,
			s.sawFirstEvent,
			s.sawErrorEvent,
			codexImageSafeSummaryValue(s.lastEventType),
			codexImageSafeSummaryValue(s.lastDataType),
			codexImageSafeSummaryValue(s.upstream.eventType),
			codexImageSafeSummaryValue(s.upstream.dataType),
			codexImageSafeSummaryValue(s.upstream.errorType),
			codexImageSafeSummaryValue(s.upstream.errorCode),
			codexImageSafeSummaryValue(s.upstream.errorStatus),
			codexImageSafeSummaryValue(s.upstream.errorParam),
			codexImageSafeSummaryValue(s.upstream.errorReason),
			codexImageSafeSummaryValue(s.upstream.incompleteReason),
			codexImageSafeSummaryValue(s.upstream.failedReason),
			codexImageSafeSummaryValue(s.upstream.upstreamResponse),
			codexImageSafeSummaryValue(s.upstream.upstreamRequest),
			codexImageSafeSummaryValue(s.upstream.retryAfter),
			codexImageSafeSummaryValue(s.upstream.responseID),
			codexImageSafeSummaryValue(s.upstream.errorCategory),
			s.eventCount,
			s.dataCount,
			s.imageCount,
			s.partialImageCount,
			elapsedMS,
			codexImageSafeSummaryValue(s.streamEndReason),
			codexImageSafeSummaryValue(s.readErrorType),
		),
	}
}

func codexImageSafeSummaryValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	lower := strings.ToLower(value)
	for _, sensitive := range []string{"authorization", "cookie", "api_key", "access_token", "refresh_token", "id_token", "bearer", "sk-", "prompt", "base64", "b64"} {
		if strings.Contains(lower, sensitive) {
			return "redacted"
		}
	}
	if len(value) > 80 {
		value = value[:80]
	}
	var b strings.Builder
	b.Grow(len(value))
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.' || r == '_' || r == '-' || r == '/' || r == ':':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

func codexImageCompletedWithoutOutputReason(payload []byte) string {
	for _, path := range []string{
		"response.error.code",
		"response.error.type",
		"response.error.message",
		"response.incomplete_details.reason",
		"response.incomplete_details.type",
	} {
		if value := strings.TrimSpace(gjson.GetBytes(payload, path).String()); value != "" {
			return sanitizeCodexImageOutputReason(value)
		}
	}

	output := gjson.GetBytes(payload, "response.output")
	if output.IsArray() {
		items := output.Array()
		if len(items) == 0 {
			return "empty_response_output"
		}
		for _, item := range items {
			if item.Get("type").String() != "image_generation_call" {
				continue
			}
			if status := strings.TrimSpace(item.Get("status").String()); status != "" {
				return sanitizeCodexImageOutputReason(status)
			}
			if errCode := strings.TrimSpace(item.Get("error.code").String()); errCode != "" {
				return sanitizeCodexImageOutputReason(errCode)
			}
			if errMessage := strings.TrimSpace(item.Get("error.message").String()); errMessage != "" {
				return sanitizeCodexImageOutputReason(errMessage)
			}
			return "empty_image_generation_result"
		}
	}
	if status := strings.TrimSpace(gjson.GetBytes(payload, "response.status").String()); status != "" {
		return sanitizeCodexImageOutputReason(status)
	}
	return "empty_response_output"
}

func sanitizeCodexImageOutputReason(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return "unknown"
	}
	replacer := strings.NewReplacer("\r", " ", "\n", " ", "\t", " ")
	reason = replacer.Replace(reason)
	reason = strings.Join(strings.Fields(reason), " ")
	if len(reason) > 120 {
		reason = strings.TrimSpace(reason[:120])
	}
	return reason
}

func codexImageOutputTypes(payload []byte) []string {
	output := gjson.GetBytes(payload, "response.output")
	if !output.IsArray() {
		return nil
	}
	types := make([]string, 0)
	for _, item := range output.Array() {
		if itemType := strings.TrimSpace(item.Get("type").String()); itemType != "" {
			types = append(types, itemType)
		}
	}
	return types
}

func codexImageGenerationStatuses(payload []byte) []string {
	output := gjson.GetBytes(payload, "response.output")
	if !output.IsArray() {
		return nil
	}
	statuses := make([]string, 0)
	for _, item := range output.Array() {
		if item.Get("type").String() != "image_generation_call" {
			continue
		}
		if status := strings.TrimSpace(item.Get("status").String()); status != "" {
			statuses = append(statuses, status)
		}
	}
	return statuses
}

func logCodexImageCompletedWithoutOutput(ctx context.Context, payload []byte, reason string) {
	fields := log.Fields{
		"reason":                    reason,
		"response_status":           strings.TrimSpace(gjson.GetBytes(payload, "response.status").String()),
		"response_error":            strings.TrimSpace(gjson.GetBytes(payload, "response.error").Raw),
		"response_incomplete":       strings.TrimSpace(gjson.GetBytes(payload, "response.incomplete_details").Raw),
		"response_output_types":     codexImageOutputTypes(payload),
		"image_generation_statuses": codexImageGenerationStatuses(payload),
	}
	helps.LogWithRequestID(ctx).WithFields(fields).Warn("codex openai images: upstream completed without image output")
}

func (e *CodexExecutor) executeOpenAIImageStream(ctx context.Context, auth *cliproxyauth.Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (_ *cliproxyexecutor.StreamResult, err error) {
	prepared, errPrepare := codexPrepareOpenAIImageRequest(req, opts)
	if errPrepare != nil {
		return nil, errPrepare
	}

	apiKey, baseURL := codexCreds(auth)
	if baseURL == "" {
		baseURL = "https://chatgpt.com/backend-api/codex"
	}

	mainModel := e.resolveGPTImage2BaseModel()
	reporter := helps.NewUsageReporter(ctx, e.Identifier(), mainModel, auth)
	defer reporter.TrackFailure(ctx, &err)

	body, errBuild := e.prepareCodexOpenAIImageBody(prepared.Body, req, opts, mainModel)
	if errBuild != nil {
		return nil, errBuild
	}
	reporter.SetTranslatedReasoningEffort(body, "codex")

	url := strings.TrimSuffix(baseURL, "/") + "/responses"
	httpReq, errCache := e.cacheHelper(ctx, sdktranslator.FromString(codexOpenAIImageSourceFormat), url, req, body)
	if errCache != nil {
		return nil, errCache
	}
	applyCodexHeaders(httpReq, auth, apiKey, true, e.cfg)
	recordCodexOpenAIImageRequest(ctx, e.cfg, e.Identifier(), auth, url, httpReq.Header.Clone(), body)

	httpClient := e.newCodexOpenAIImageHTTPClient(ctx, auth)
	httpClient = reporter.TrackHTTPClient(httpClient)
	httpResp, errDo := httpClient.Do(httpReq)
	if errDo != nil {
		helps.RecordAPIResponseError(ctx, e.cfg, errDo)
		return nil, errDo
	}
	helps.RecordAPIResponseMetadata(ctx, e.cfg, httpResp.StatusCode, httpResp.Header.Clone())
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		data, errRead := io.ReadAll(httpResp.Body)
		if errClose := httpResp.Body.Close(); errClose != nil {
			log.Errorf("codex executor: close response body error: %v", errClose)
		}
		if errRead != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errRead)
			return nil, errRead
		}
		helps.AppendAPIResponseChunk(ctx, e.cfg, data)
		helps.LogWithRequestID(ctx).Debugf("request error, error status: %d, error message: %s", httpResp.StatusCode, helps.SummarizeErrorBody(httpResp.Header.Get("Content-Type"), data))
		err = newCodexStatusErr(httpResp.StatusCode, data)
		return nil, err
	}

	out := make(chan cliproxyexecutor.StreamChunk)
	go func() {
		defer close(out)
		defer func() {
			if errClose := httpResp.Body.Close(); errClose != nil {
				log.Errorf("codex executor: close response body error: %v", errClose)
			}
		}()

		sendPayload := func(payload []byte) bool {
			select {
			case out <- cliproxyexecutor.StreamChunk{Payload: payload}:
				return true
			case <-ctx.Done():
				return false
			}
		}
		sendError := func(errSend error) bool {
			select {
			case out <- cliproxyexecutor.StreamChunk{Err: errSend}:
				return true
			case <-ctx.Done():
				return false
			}
		}

		scanner := bufio.NewScanner(httpResp.Body)
		scanner.Buffer(nil, 52_428_800) // 50MB
		outputItemsByIndex := make(map[int64][]byte)
		var outputItemsFallback [][]byte
		for scanner.Scan() {
			line := scanner.Bytes()
			helps.AppendAPIResponseChunk(ctx, e.cfg, line)
			if !bytes.HasPrefix(line, dataTag) {
				continue
			}
			eventData := bytes.TrimSpace(line[len(dataTag):])
			switch gjson.GetBytes(eventData, "type").String() {
			case "response.output_item.done":
				collectCodexOutputItemDone(eventData, outputItemsByIndex, &outputItemsFallback)
			case "response.image_generation_call.partial_image":
				frame := codexBuildImagePartialFrame(eventData, prepared.ResponseFormat, prepared.StreamPrefix)
				if len(frame) > 0 && !sendPayload(frame) {
					return
				}
			case "response.completed":
				if detail, ok := helps.ParseCodexUsage(eventData); ok {
					reporter.Publish(ctx, detail)
				}
				publishCodexImageToolUsage(ctx, reporter, body, eventData)
				completedData := patchCodexCompletedOutput(eventData, outputItemsByIndex, outputItemsFallback)
				results, _, usageRaw, _, errExtract := codexExtractImagesFromResponsesCompleted(completedData)
				if errExtract != nil {
					sendError(errExtract)
					return
				}
				if len(results) == 0 {
					reason := codexImageCompletedWithoutOutputReason(completedData)
					logCodexImageCompletedWithoutOutput(ctx, completedData, reason)
					sendError(statusErr{code: http.StatusBadGateway, msg: "upstream completed without image output: " + reason})
					return
				}
				for _, img := range results {
					frame := codexBuildImageCompletedFrame(img, usageRaw, prepared.ResponseFormat, prepared.StreamPrefix)
					if len(frame) > 0 && !sendPayload(frame) {
						return
					}
				}
				return
			}
		}
		if errScan := scanner.Err(); errScan != nil {
			helps.RecordAPIResponseError(ctx, e.cfg, errScan)
			reporter.PublishFailure(ctx, errScan)
			sendError(errScan)
		}
	}()
	return &cliproxyexecutor.StreamResult{Headers: httpResp.Header.Clone(), Chunks: out}, nil
}

func (e *CodexExecutor) prepareCodexOpenAIImageBody(body []byte, req cliproxyexecutor.Request, opts cliproxyexecutor.Options, mainModel string) ([]byte, error) {
	out := body
	mainModel = strings.TrimSpace(mainModel)
	if mainModel == "" {
		mainModel = codexOpenAIImagesMainModel
	}
	var errThinking error
	out, errThinking = thinking.ApplyThinking(out, mainModel, codexOpenAIImageSourceFormat, "codex", e.Identifier())
	if errThinking != nil {
		return nil, errThinking
	}

	requestedModel := helps.PayloadRequestedModel(opts, req.Model)
	requestPath := helps.PayloadRequestPath(opts)
	out = helps.ApplyPayloadConfigWithRequest(e.cfg, mainModel, "codex", codexOpenAIImageSourceFormat, "", out, body, requestedModel, requestPath, opts.Headers)
	out, _ = sjson.SetBytes(out, "model", mainModel)
	out, _ = sjson.SetBytes(out, "stream", true)
	out, _ = sjson.DeleteBytes(out, "previous_response_id")
	out, _ = sjson.DeleteBytes(out, "prompt_cache_retention")
	out, _ = sjson.DeleteBytes(out, "safety_identifier")
	out, _ = sjson.DeleteBytes(out, "stream_options")
	return normalizeCodexInstructions(out), nil
}

func recordCodexOpenAIImageRequest(ctx context.Context, cfg *config.Config, provider string, auth *cliproxyauth.Auth, url string, headers http.Header, body []byte) {
	var authID, authLabel, authType, authValue string
	if auth != nil {
		authID = auth.ID
		authLabel = auth.Label
		authType, authValue = auth.AccountInfo()
	}
	helps.RecordAPIRequest(ctx, cfg, helps.UpstreamRequestLog{
		URL:       url,
		Method:    http.MethodPost,
		Headers:   headers,
		Body:      body,
		Provider:  provider,
		AuthID:    authID,
		AuthLabel: authLabel,
		AuthType:  authType,
		AuthValue: authValue,
	})
}

func codexPrepareOpenAIImageRequest(req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (codexOpenAIImagePreparedRequest, error) {
	path := helps.PayloadRequestPath(opts)
	if strings.HasSuffix(path, codexImagesGenerationsPath) {
		return codexPrepareOpenAIImageGenerationJSON(req.Payload, req.Model)
	}
	if !strings.HasSuffix(path, codexImagesEditsPath) {
		return codexOpenAIImagePreparedRequest{}, fmt.Errorf("unsupported OpenAI image endpoint path %q", path)
	}

	contentType := codexImageContentType(opts.Headers)
	mediaType, _, _ := mime.ParseMediaType(contentType)
	if strings.HasPrefix(strings.ToLower(mediaType), "multipart/") {
		return codexPrepareOpenAIImageEditMultipart(req.Payload, req.Model, contentType)
	}
	return codexPrepareOpenAIImageEditJSON(req.Payload, req.Model)
}

func codexPrepareOpenAIImageGenerationJSON(rawJSON []byte, routeModel string) (codexOpenAIImagePreparedRequest, error) {
	if !json.Valid(rawJSON) {
		return codexOpenAIImagePreparedRequest{}, fmt.Errorf("invalid OpenAI image generation request JSON")
	}
	prompt := strings.TrimSpace(gjson.GetBytes(rawJSON, "prompt").String())
	tool := codexBuildOpenAIImageTool(rawJSON, routeModel, "generate", []string{"size", "quality", "background", "output_format", "moderation"}, []string{"output_compression", "partial_images"})
	body := codexBuildImagesResponsesRequest(prompt, nil, tool)
	return codexOpenAIImagePreparedRequest{
		Body:           body,
		ResponseFormat: codexOpenAIImageResponseFormatFromJSON(rawJSON),
		StreamPrefix:   "image_generation",
	}, nil
}

func codexPrepareOpenAIImageEditJSON(rawJSON []byte, routeModel string) (codexOpenAIImagePreparedRequest, error) {
	if !json.Valid(rawJSON) {
		return codexOpenAIImagePreparedRequest{}, fmt.Errorf("invalid OpenAI image edit request JSON")
	}
	prompt := strings.TrimSpace(gjson.GetBytes(rawJSON, "prompt").String())
	images := make([]string, 0)
	if imagesResult := gjson.GetBytes(rawJSON, "images"); imagesResult.IsArray() {
		for _, img := range imagesResult.Array() {
			url := strings.TrimSpace(img.Get("image_url").String())
			if url != "" {
				images = append(images, url)
			}
		}
	}
	tool := codexBuildOpenAIImageTool(rawJSON, routeModel, "edit", []string{"size", "quality", "background", "output_format", "input_fidelity", "moderation"}, []string{"output_compression", "partial_images"})
	if mask := strings.TrimSpace(gjson.GetBytes(rawJSON, "mask.image_url").String()); mask != "" {
		tool, _ = sjson.SetBytes(tool, "input_image_mask.image_url", mask)
	}
	body := codexBuildImagesResponsesRequest(prompt, images, tool)
	return codexOpenAIImagePreparedRequest{
		Body:           body,
		ResponseFormat: codexOpenAIImageResponseFormatFromJSON(rawJSON),
		StreamPrefix:   "image_edit",
	}, nil
}

func codexPrepareOpenAIImageEditMultipart(rawBody []byte, routeModel string, contentType string) (codexOpenAIImagePreparedRequest, error) {
	_, params, errMedia := mime.ParseMediaType(contentType)
	if errMedia != nil {
		return codexOpenAIImagePreparedRequest{}, fmt.Errorf("parse multipart content type failed: %w", errMedia)
	}
	boundary := strings.TrimSpace(params["boundary"])
	if boundary == "" {
		return codexOpenAIImagePreparedRequest{}, fmt.Errorf("multipart boundary is required")
	}
	reader := multipart.NewReader(bytes.NewReader(rawBody), boundary)
	form, errForm := reader.ReadForm(32 << 20)
	if errForm != nil {
		return codexOpenAIImagePreparedRequest{}, fmt.Errorf("parse multipart form failed: %w", errForm)
	}
	defer func() {
		if errRemove := form.RemoveAll(); errRemove != nil {
			log.Errorf("codex openai images: remove multipart temp files error: %v", errRemove)
		}
	}()

	prompt := strings.TrimSpace(codexFormValue(form, "prompt"))
	responseFormat := codexNormalizeImageResponseFormat(codexFormValue(form, "response_format"))
	tool := []byte(`{"type":"image_generation","action":"edit"}`)
	tool, _ = sjson.SetBytes(tool, "model", codexOpenAIImageToolModel(codexFormValue(form, "model"), routeModel))
	for _, field := range []string{"size", "quality", "background", "output_format", "input_fidelity", "moderation"} {
		if value := strings.TrimSpace(codexFormValue(form, field)); value != "" {
			tool, _ = sjson.SetBytes(tool, field, value)
		}
	}
	for _, field := range []string{"output_compression", "partial_images"} {
		if value := strings.TrimSpace(codexFormValue(form, field)); value != "" {
			if parsed, errParse := strconv.ParseInt(value, 10, 64); errParse == nil {
				tool, _ = sjson.SetBytes(tool, field, parsed)
			}
		}
	}

	images := make([]string, 0)
	for _, fh := range codexMultipartImageFiles(form) {
		dataURL, errData := codexMultipartFileToDataURL(fh)
		if errData != nil {
			return codexOpenAIImagePreparedRequest{}, errData
		}
		images = append(images, dataURL)
	}
	if maskFiles := form.File["mask"]; len(maskFiles) > 0 && maskFiles[0] != nil {
		dataURL, errData := codexMultipartFileToDataURL(maskFiles[0])
		if errData != nil {
			return codexOpenAIImagePreparedRequest{}, errData
		}
		tool, _ = sjson.SetBytes(tool, "input_image_mask.image_url", dataURL)
	}

	body := codexBuildImagesResponsesRequest(prompt, images, tool)
	return codexOpenAIImagePreparedRequest{
		Body:           body,
		ResponseFormat: responseFormat,
		StreamPrefix:   "image_edit",
	}, nil
}

func codexImageContentType(headers http.Header) string {
	if headers == nil {
		return ""
	}
	return strings.TrimSpace(headers.Get("Content-Type"))
}

func codexOpenAIImageResponseFormatFromJSON(rawJSON []byte) string {
	return codexNormalizeImageResponseFormat(gjson.GetBytes(rawJSON, "response_format").String())
}

func codexNormalizeImageResponseFormat(responseFormat string) string {
	if strings.EqualFold(strings.TrimSpace(responseFormat), "url") {
		return "url"
	}
	return "b64_json"
}

func codexOpenAIImageToolModel(requestModel string, routeModel string) string {
	model := strings.TrimSpace(requestModel)
	if model == "" {
		model = strings.TrimSpace(routeModel)
	}
	if model == "" {
		model = codexDefaultImageToolModel
	}
	return model
}

func codexBuildOpenAIImageTool(rawJSON []byte, routeModel string, action string, stringFields []string, numberFields []string) []byte {
	tool := []byte(`{"type":"image_generation","action":""}`)
	tool, _ = sjson.SetBytes(tool, "action", action)
	tool, _ = sjson.SetBytes(tool, "model", codexOpenAIImageToolModel(gjson.GetBytes(rawJSON, "model").String(), routeModel))
	for _, field := range stringFields {
		if value := strings.TrimSpace(gjson.GetBytes(rawJSON, field).String()); value != "" {
			tool, _ = sjson.SetBytes(tool, field, value)
		}
	}
	for _, field := range numberFields {
		if value := gjson.GetBytes(rawJSON, field); value.Exists() && value.Type == gjson.Number {
			tool, _ = sjson.SetBytes(tool, field, value.Int())
		}
	}
	return tool
}

func codexBuildImagesResponsesRequest(prompt string, images []string, toolJSON []byte) []byte {
	req := []byte(`{"instructions":"","stream":true,"reasoning":{"effort":"medium","summary":"auto"},"parallel_tool_calls":true,"include":["reasoning.encrypted_content"],"model":"","store":false,"tool_choice":{"type":"image_generation"}}`)
	req, _ = sjson.SetBytes(req, "model", codexOpenAIImagesMainModel)

	input := []byte(`[{"type":"message","role":"user","content":[{"type":"input_text","text":""}]}]`)
	input, _ = sjson.SetBytes(input, "0.content.0.text", prompt)
	contentIndex := 1
	for _, img := range images {
		if strings.TrimSpace(img) == "" {
			continue
		}
		part := []byte(`{"type":"input_image","image_url":""}`)
		part, _ = sjson.SetBytes(part, "image_url", img)
		input, _ = sjson.SetRawBytes(input, fmt.Sprintf("0.content.%d", contentIndex), part)
		contentIndex++
	}
	req, _ = sjson.SetRawBytes(req, "input", input)

	req, _ = sjson.SetRawBytes(req, "tools", []byte(`[]`))
	if len(toolJSON) > 0 && json.Valid(toolJSON) {
		req, _ = sjson.SetRawBytes(req, "tools.-1", toolJSON)
	}
	return req
}

func codexFormValue(form *multipart.Form, key string) string {
	if form == nil || len(form.Value[key]) == 0 {
		return ""
	}
	return strings.TrimSpace(form.Value[key][0])
}

func codexMultipartImageFiles(form *multipart.Form) []*multipart.FileHeader {
	if form == nil {
		return nil
	}
	if files := form.File["image[]"]; len(files) > 0 {
		return files
	}
	return form.File["image"]
}

func codexMultipartFileToDataURL(fileHeader *multipart.FileHeader) (string, error) {
	if fileHeader == nil {
		return "", fmt.Errorf("upload file is nil")
	}
	f, errOpen := fileHeader.Open()
	if errOpen != nil {
		return "", fmt.Errorf("open upload file failed: %w", errOpen)
	}
	defer func() {
		if errClose := f.Close(); errClose != nil {
			log.Errorf("codex openai images: close upload file error: %v", errClose)
		}
	}()

	data, errRead := io.ReadAll(f)
	if errRead != nil {
		return "", fmt.Errorf("read upload file failed: %w", errRead)
	}
	mediaType := strings.TrimSpace(fileHeader.Header.Get("Content-Type"))
	if mediaType == "" {
		mediaType = http.DetectContentType(data)
	}
	return "data:" + mediaType + ";base64," + base64.StdEncoding.EncodeToString(data), nil
}

func codexExtractImagesFromResponsesCompleted(payload []byte) (results []codexImageCallResult, createdAt int64, usageRaw []byte, firstMeta codexImageCallResult, err error) {
	if gjson.GetBytes(payload, "type").String() != "response.completed" {
		return nil, 0, nil, codexImageCallResult{}, fmt.Errorf("unexpected event type")
	}
	createdAt = gjson.GetBytes(payload, "response.created_at").Int()
	if createdAt <= 0 {
		createdAt = time.Now().Unix()
	}
	output := gjson.GetBytes(payload, "response.output")
	if output.IsArray() {
		for _, item := range output.Array() {
			if item.Get("type").String() != "image_generation_call" {
				continue
			}
			res := strings.TrimSpace(item.Get("result").String())
			if res == "" {
				continue
			}
			entry := codexImageCallResult{
				Result:        res,
				RevisedPrompt: strings.TrimSpace(item.Get("revised_prompt").String()),
				OutputFormat:  strings.TrimSpace(item.Get("output_format").String()),
				Size:          strings.TrimSpace(item.Get("size").String()),
				Background:    strings.TrimSpace(item.Get("background").String()),
				Quality:       strings.TrimSpace(item.Get("quality").String()),
			}
			if len(results) == 0 {
				firstMeta = entry
			}
			results = append(results, entry)
		}
	}
	if usage := gjson.GetBytes(payload, "response.tool_usage.image_gen"); usage.Exists() && usage.IsObject() {
		usageRaw = []byte(usage.Raw)
	}
	return results, createdAt, usageRaw, firstMeta, nil
}

func codexBuildImagesAPIResponse(results []codexImageCallResult, createdAt int64, usageRaw []byte, firstMeta codexImageCallResult, responseFormat string) ([]byte, error) {
	out := []byte(`{"created":0,"data":[]}`)
	out, _ = sjson.SetBytes(out, "created", createdAt)
	responseFormat = codexNormalizeImageResponseFormat(responseFormat)
	for _, img := range results {
		item := []byte(`{}`)
		if responseFormat == "url" {
			item, _ = sjson.SetBytes(item, "url", "data:"+codexMimeTypeFromOutputFormat(img.OutputFormat)+";base64,"+img.Result)
		} else {
			item, _ = sjson.SetBytes(item, "b64_json", img.Result)
		}
		if img.RevisedPrompt != "" {
			item, _ = sjson.SetBytes(item, "revised_prompt", img.RevisedPrompt)
		}
		out, _ = sjson.SetRawBytes(out, "data.-1", item)
	}
	if firstMeta.Background != "" {
		out, _ = sjson.SetBytes(out, "background", firstMeta.Background)
	}
	if firstMeta.OutputFormat != "" {
		out, _ = sjson.SetBytes(out, "output_format", firstMeta.OutputFormat)
	}
	if firstMeta.Quality != "" {
		out, _ = sjson.SetBytes(out, "quality", firstMeta.Quality)
	}
	if firstMeta.Size != "" {
		out, _ = sjson.SetBytes(out, "size", firstMeta.Size)
	}
	if len(usageRaw) > 0 && json.Valid(usageRaw) {
		out, _ = sjson.SetRawBytes(out, "usage", usageRaw)
	}
	return out, nil
}

func codexBuildImagePartialFrame(payload []byte, responseFormat string, streamPrefix string) []byte {
	b64 := strings.TrimSpace(gjson.GetBytes(payload, "partial_image_b64").String())
	if b64 == "" {
		return nil
	}
	outputFormat := strings.TrimSpace(gjson.GetBytes(payload, "output_format").String())
	eventName := strings.TrimSpace(streamPrefix) + ".partial_image"
	data := []byte(`{"type":"","partial_image_index":0}`)
	data, _ = sjson.SetBytes(data, "type", eventName)
	data, _ = sjson.SetBytes(data, "partial_image_index", gjson.GetBytes(payload, "partial_image_index").Int())
	if codexNormalizeImageResponseFormat(responseFormat) == "url" {
		data, _ = sjson.SetBytes(data, "url", "data:"+codexMimeTypeFromOutputFormat(outputFormat)+";base64,"+b64)
	} else {
		data, _ = sjson.SetBytes(data, "b64_json", b64)
	}
	return codexBuildSSEFrame(eventName, data)
}

func codexBuildImageCompletedFrame(img codexImageCallResult, usageRaw []byte, responseFormat string, streamPrefix string) []byte {
	eventName := strings.TrimSpace(streamPrefix) + ".completed"
	data := []byte(`{"type":""}`)
	data, _ = sjson.SetBytes(data, "type", eventName)
	if codexNormalizeImageResponseFormat(responseFormat) == "url" {
		data, _ = sjson.SetBytes(data, "url", "data:"+codexMimeTypeFromOutputFormat(img.OutputFormat)+";base64,"+img.Result)
	} else {
		data, _ = sjson.SetBytes(data, "b64_json", img.Result)
	}
	if len(usageRaw) > 0 && json.Valid(usageRaw) {
		data, _ = sjson.SetRawBytes(data, "usage", usageRaw)
	}
	if img.RevisedPrompt != "" {
		data, _ = sjson.SetBytes(data, "revised_prompt", img.RevisedPrompt)
	}
	if img.OutputFormat != "" {
		data, _ = sjson.SetBytes(data, "output_format", img.OutputFormat)
	}
	if img.Background != "" {
		data, _ = sjson.SetBytes(data, "background", img.Background)
	}
	if img.Quality != "" {
		data, _ = sjson.SetBytes(data, "quality", img.Quality)
	}
	if img.Size != "" {
		data, _ = sjson.SetBytes(data, "size", img.Size)
	}
	return codexBuildSSEFrame(eventName, data)
}

func codexBuildSSEFrame(eventName string, data []byte) []byte {
	var buf bytes.Buffer
	if strings.TrimSpace(eventName) != "" {
		buf.WriteString("event: ")
		buf.WriteString(eventName)
		buf.WriteString("\n")
	}
	buf.WriteString("data: ")
	buf.Write(data)
	buf.WriteString("\n\n")
	return buf.Bytes()
}

func codexMimeTypeFromOutputFormat(outputFormat string) string {
	switch strings.ToLower(strings.TrimSpace(outputFormat)) {
	case "jpg", "jpeg":
		return "image/jpeg"
	case "webp":
		return "image/webp"
	default:
		return "image/png"
	}
}
