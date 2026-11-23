package helps

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	log "github.com/sirupsen/logrus"
)

const (
	apiAttemptsKey                       = "API_UPSTREAM_ATTEMPTS"
	apiRequestKey                        = "API_REQUEST"
	apiResponseKey                       = "API_RESPONSE"
	apiWebsocketTimelineKey              = "API_WEBSOCKET_TIMELINE"
	deferredAPIRequestBytesKey           = "DEFERRED_API_REQUEST_BYTES"
	creditsUsedKey                       = "__antigravity_credits_used__"
	maxDeferredAPIRequestBodyBytes       = logging.MaxDeferredAPIRequestBodyBytes
	maxDeferredAPIResponseBodyBytes      = logging.MaxDeferredAPIResponseBodyBytes
	maxTotalDeferredAPIResponseBodyBytes = logging.MaxTotalDeferredAPIResponseBodyBytes
)

// UpstreamRequestLog captures the outbound upstream request details for logging.
type UpstreamRequestLog struct {
	URL       string
	Method    string
	Headers   http.Header
	Body      []byte
	Provider  string
	AuthID    string
	AuthLabel string
	AuthType  string
	AuthValue string
}

type upstreamAttempt struct {
	mu                   sync.Mutex
	index                int
	request              string
	response             *strings.Builder
	responseSource       *logging.FileBodySource
	responseIntroWritten bool
	statusWritten        bool
	headersWritten       bool
	bodyStarted          bool
	bodyHasContent       bool
	prevWasSSEEvent      bool
	trailingNewlines     int
}

func requestLogCaptureEnabled(cfg *config.Config) bool {
	return cfg != nil && cfg.RequestLog && !cfg.CommercialMode
}

// RecordAPIRequest stores the upstream request metadata in Gin context for request logging.
func RecordAPIRequest(ctx context.Context, cfg *config.Config, info UpstreamRequestLog) {
	if cfg == nil || cfg.CommercialMode {
		return
	}
	// Check the immutable policy and request-scoped capture before accessing the pooled Gin context.
	if !cfg.RequestLog {
		if logging.DeferredAPIRequestCaptureDisabled(ctx) {
			return
		}
		if capture := logging.DeferredAPICaptureFromContext(ctx); capture != nil {
			deferAPIRequestToCapture(capture, info)
			return
		}
		ginCtx := ginContextFrom(ctx)
		if ginCtx == nil {
			return
		}
		deferAPIRequest(ginCtx, info)
		return
	}
	ginCtx := ginContextFrom(ctx)
	if ginCtx == nil {
		return
	}

	attempts := getAttempts(ginCtx)
	index := len(attempts) + 1
	builder := newAPIRequestLogBuilder(index, info, time.Now())

	requestText := ""
	if source, ok := apiRequestSource(ginCtx); ok {
		if errWrite := source.AppendBytes([]byte(builder.String())); errWrite == nil {
			if len(info.Body) > 0 {
				if errBody := source.AppendBytes(info.Body); errBody != nil {
					log.WithError(errBody).Warn("failed to append api request body log part")
				}
			} else if errEmpty := source.AppendBytes([]byte("<empty>")); errEmpty != nil {
				log.WithError(errEmpty).Warn("failed to append empty api request log part")
			}
			if errEnd := source.AppendBytes([]byte("\n\n")); errEnd != nil {
				log.WithError(errEnd).Warn("failed to append api request log terminator")
			}
		} else {
			log.WithError(errWrite).Warn("failed to append api request log part")
			if len(info.Body) > 0 {
				builder.WriteString(string(info.Body))
			} else {
				builder.WriteString("<empty>")
			}
			builder.WriteString("\n\n")
			requestText = builder.String()
		}
	} else {
		if len(info.Body) > 0 {
			builder.WriteString(string(info.Body))
		} else {
			builder.WriteString("<empty>")
		}
		builder.WriteString("\n\n")
		requestText = builder.String()
	}

	attempt := &upstreamAttempt{
		index:          index,
		request:        requestText,
		response:       &strings.Builder{},
		responseSource: apiResponseSourceOrNil(ginCtx),
	}
	attempts = append(attempts, attempt)
	ginCtx.Set(apiAttemptsKey, attempts)
	if mem := memoryResponseCaptureFrom(ginCtx); mem != nil {
		mem.setAttempts(attempts)
	}
	if requestText != "" {
		updateAggregatedRequest(ginCtx, attempts)
	}
}

func buildDeferredAPIRequestCallback(capturedInfo UpstreamRequestLog, index int, capturedAt time.Time, capturedBody []byte, bodyEmpty, bodyTruncated bool) logging.DeferredAPIRequest {
	reqInfo := capturedInfo
	reqInfo.Body = capturedBody
	return func() []byte {
		builder := newAPIRequestLogBuilder(index, reqInfo, capturedAt)
		if bodyEmpty {
			builder.WriteString("<empty>")
		} else {
			builder.Write(reqInfo.Body)
			if bodyTruncated {
				builder.WriteString(fmt.Sprintf("\n[API REQUEST BODY TRUNCATED: captured first %d bytes]", len(reqInfo.Body)))
			}
		}
		builder.WriteString("\n\n")
		return []byte(builder.String())
	}
}

func deferAPIRequestToCapture(capture *logging.DeferredAPICapture, info UpstreamRequestLog) (logging.DeferredAPIRequest, int, bool) {
	capturedInfo := info
	return capture.RecordRequest(info.Body, func(index int, capturedAt time.Time, capturedBody []byte, bodyEmpty, bodyTruncated bool) logging.DeferredAPIRequest {
		return buildDeferredAPIRequestCallback(capturedInfo, index, capturedAt, capturedBody, bodyEmpty, bodyTruncated)
	})
}

func deferOrReplacePendingAPIRequest(ctx context.Context, cfg *config.Config, info UpstreamRequestLog) {
	if !deferredResponseCaptureEnabled(ctx, cfg) {
		return
	}
	capturedInfo := info
	if capture := logging.DeferredAPICaptureFromContext(ctx); capture != nil {
		capture.RecordOrReplacePendingRequest(info.Body, func(index int, capturedAt time.Time, capturedBody []byte, bodyEmpty, bodyTruncated bool) logging.DeferredAPIRequest {
			return buildDeferredAPIRequestCallback(capturedInfo, index, capturedAt, capturedBody, bodyEmpty, bodyTruncated)
		})
		return
	}
	ginCtx := ginContextFrom(ctx)
	if ginCtx == nil {
		return
	}
	capture := deferredResponseCaptureFrom(ginCtx)
	fn, bytesUsed, replaced, ok := capture.RecordOrReplacePendingRequest(info.Body, func(index int, capturedAt time.Time, capturedBody []byte, bodyEmpty, bodyTruncated bool) logging.DeferredAPIRequest {
		return buildDeferredAPIRequestCallback(capturedInfo, index, capturedAt, capturedBody, bodyEmpty, bodyTruncated)
	})
	if !ok {
		return
	}
	var requests []logging.DeferredAPIRequest
	if value, exists := ginCtx.Get(logging.DeferredAPIRequestContextKey); exists {
		requests, _ = value.([]logging.DeferredAPIRequest)
	}
	if replaced && len(requests) > 0 {
		requests[len(requests)-1] = fn
	} else {
		requests = append(requests, fn)
	}
	ginCtx.Set(deferredAPIRequestBytesKey, bytesUsed)
	ginCtx.Set(logging.DeferredAPIRequestContextKey, requests)
}

func deferAPIRequest(ginCtx *gin.Context, info UpstreamRequestLog) {
	if ginCtx == nil {
		return
	}
	capture := deferredResponseCaptureFrom(ginCtx)
	fn, bytesUsed, ok := deferAPIRequestToCapture(capture, info)
	if !ok {
		return
	}
	var requests []logging.DeferredAPIRequest
	if value, exists := ginCtx.Get(logging.DeferredAPIRequestContextKey); exists {
		requests, _ = value.([]logging.DeferredAPIRequest)
	}
	requests = append(requests, fn)
	ginCtx.Set(deferredAPIRequestBytesKey, bytesUsed)
	ginCtx.Set(logging.DeferredAPIRequestContextKey, requests)
}

func deferredResponseCaptureEnabled(ctx context.Context, cfg *config.Config) bool {
	if cfg == nil || cfg.CommercialMode || cfg.RequestLog {
		return false
	}
	return !logging.DeferredAPIRequestCaptureDisabled(ctx)
}

func resolveDeferredAPICapture(ctx context.Context, cfg *config.Config) *logging.DeferredAPICapture {
	// Check the immutable policy and request-scoped capture before accessing the pooled Gin context.
	if !deferredResponseCaptureEnabled(ctx, cfg) {
		return nil
	}
	if capture := logging.DeferredAPICaptureFromContext(ctx); capture != nil {
		return capture
	}
	ginCtx := ginContextFrom(ctx)
	if ginCtx == nil {
		return nil
	}
	return deferredResponseCaptureFrom(ginCtx)
}

func deferredResponseCaptureFrom(ginCtx *gin.Context) *logging.DeferredAPICapture {
	if value, exists := ginCtx.Get(logging.DeferredAPIResponseContextKey); exists {
		if capture, ok := value.(*logging.DeferredAPICapture); ok && capture != nil {
			return capture
		}
	}
	capture := logging.NewDeferredAPICapture()
	ginCtx.Set(logging.DeferredAPIResponseContextKey, capture)
	return capture
}

func recordDeferredResponseMetadata(ctx context.Context, cfg *config.Config, status int, headers http.Header) {
	if capture := resolveDeferredAPICapture(ctx, cfg); capture != nil {
		capture.RecordResponseMetadata(status, headers)
	}
}

func recordDeferredResponseError(ctx context.Context, cfg *config.Config, err error) {
	if capture := resolveDeferredAPICapture(ctx, cfg); capture != nil {
		capture.RecordResponseError(err)
	}
}

func appendDeferredResponseChunk(ctx context.Context, cfg *config.Config, chunk []byte) {
	if capture := resolveDeferredAPICapture(ctx, cfg); capture != nil {
		capture.AppendResponseChunk(chunk)
	}
}

func newAPIRequestLogBuilder(index int, info UpstreamRequestLog, timestamp time.Time) *strings.Builder {
	builder := &strings.Builder{}
	builder.WriteString(fmt.Sprintf("=== API REQUEST %d ===\n", index))
	builder.WriteString(fmt.Sprintf("Timestamp: %s\n", timestamp.Format(time.RFC3339Nano)))
	if info.URL != "" {
		builder.WriteString(fmt.Sprintf("Upstream URL: %s\n", info.URL))
	} else {
		builder.WriteString("Upstream URL: <unknown>\n")
	}
	if info.Method != "" {
		builder.WriteString(fmt.Sprintf("HTTP Method: %s\n", info.Method))
	}
	if auth := formatAuthInfo(info); auth != "" {
		builder.WriteString(fmt.Sprintf("Auth: %s\n", auth))
	}
	builder.WriteString("\nHeaders:\n")
	writeHeaders(builder, info.Headers)
	builder.WriteString("\nBody:\n")
	return builder
}

// RecordAPIResponseMetadata captures upstream response status/header information for the latest attempt.
func RecordAPIResponseMetadata(ctx context.Context, cfg *config.Config, status int, headers http.Header) {
	logging.SetResponseHeaders(ctx, headers)
	if !requestLogCaptureEnabled(cfg) {
		recordDeferredResponseMetadata(ctx, cfg, status, headers)
		return
	}
	ginCtx := ginContextFrom(ctx)
	if ginCtx == nil {
		return
	}
	mem := memoryResponseCaptureFrom(ginCtx)
	if mem != nil {
		mem.mu.Lock()
		defer mem.mu.Unlock()
	}
	attempts, attempt := ensureAttempt(ginCtx)
	ensureResponseIntro(ginCtx, attempt)

	if status > 0 && !attempt.statusWritten {
		writeAttemptResponse(ginCtx, attempt, []byte(fmt.Sprintf("Status: %d\n", status)))
		attempt.statusWritten = true
	}
	if !attempt.headersWritten {
		builder := &strings.Builder{}
		builder.WriteString("Headers:\n")
		writeHeaders(builder, headers)
		writeAttemptResponse(ginCtx, attempt, []byte(builder.String()))
		attempt.headersWritten = true
		writeAttemptResponse(ginCtx, attempt, []byte("\n"))
	}
	if mem != nil && !mem.closed {
		mem.attempts = append(mem.attempts[:0], attempts...)
	}

	updateAggregatedResponseIfMemoryBacked(ginCtx, attempts)
}

// RecordAPIResponseError retains local failures for the downstream RESPONSE log section.
// API response capture contains upstream data only.
func RecordAPIResponseError(ctx context.Context, cfg *config.Config, err error) {
	if err == nil {
		return
	}
	if !requestLogCaptureEnabled(cfg) {
		recordDeferredResponseError(ctx, cfg, err)
		return
	}
	ginCtx := ginContextFrom(ctx)
	if ginCtx == nil {
		return
	}
	capture := logging.DeferredAPICaptureFromContext(ctx)
	if capture == nil {
		capture = deferredResponseCaptureFrom(ginCtx)
	}
	capture.RecordResponseError(err)
	// Prevent handler fallback payloads from fabricating an upstream response on dial errors.
	ginCtx.Set(logging.APIResponseCapturedContextKey, true)
}

// apiResponseMemoryCapture synchronizes in-memory upstream response attempts for
// a single HTTP request so asynchronous stream workers write only to request-owned
// buffers and response finalization snapshots and closes them without touching a
// reused pooled Gin context.
type apiResponseMemoryCapture struct {
	mu       sync.Mutex
	closed   bool
	attempts []*upstreamAttempt
}

func (c *apiResponseMemoryCapture) setAttempts(attempts []*upstreamAttempt) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return
	}
	c.attempts = append(c.attempts[:0], attempts...)
}

func (c *apiResponseMemoryCapture) SnapshotAPIResponse() []byte {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil
	}
	return renderAggregatedResponse(c.attempts)
}

func (c *apiResponseMemoryCapture) Close() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	c.attempts = nil
}

func memoryResponseCaptureFrom(ginCtx *gin.Context) *apiResponseMemoryCapture {
	if ginCtx == nil {
		return nil
	}
	value, exists := ginCtx.Get(logging.APIResponseSnapshotterContextKey)
	if !exists {
		return nil
	}
	capture, _ := value.(*apiResponseMemoryCapture)
	return capture
}

// APIResponseStreamLogger binds upstream response chunk and error logging to the
// current HTTP request and attempt before an asynchronous stream goroutine starts.
// It retains no *gin.Context reference, so late callbacks after request cancellation
// or Gin context reuse can never race with or pollute a subsequent request.
type APIResponseStreamLogger struct {
	mu             sync.Mutex
	ctx            context.Context
	capture        *logging.DeferredAPICapture
	deferredOnly   bool
	attempt        *upstreamAttempt
	responseSource *logging.FileBodySource
	memoryCapture  *apiResponseMemoryCapture
}

// BindAPIResponseStreamLogger captures the current request's logging targets synchronously
// so an asynchronous stream worker never reads or mutates a pooled Gin context.
func BindAPIResponseStreamLogger(ctx context.Context, cfg *config.Config) *APIResponseStreamLogger {
	if cfg == nil || cfg.CommercialMode {
		return nil
	}
	if !requestLogCaptureEnabled(cfg) {
		capture := resolveDeferredAPICapture(ctx, cfg)
		if capture == nil {
			return nil
		}
		return &APIResponseStreamLogger{
			ctx:          ctx,
			capture:      capture,
			deferredOnly: true,
		}
	}
	ginCtx := ginContextFrom(ctx)
	if ginCtx == nil {
		return nil
	}
	mem := memoryResponseCaptureFrom(ginCtx)
	if mem != nil {
		mem.mu.Lock()
	}
	attempts, attempt := ensureAttempt(ginCtx)
	ensureResponseIntro(ginCtx, attempt)
	capture := logging.DeferredAPICaptureFromContext(ctx)
	if capture == nil {
		capture = deferredResponseCaptureFrom(ginCtx)
	}
	ginCtx.Set(logging.APIResponseCapturedContextKey, true)
	responseSource := attempt.responseSource
	if responseSource == nil {
		responseSource = apiResponseSourceOrNil(ginCtx)
		attempt.responseSource = responseSource
	}
	if responseSource != nil {
		if mem != nil {
			mem.mu.Unlock()
		}
		return &APIResponseStreamLogger{
			ctx:            ctx,
			capture:        capture,
			attempt:        attempt,
			responseSource: responseSource,
		}
	}
	if mem == nil {
		mem = &apiResponseMemoryCapture{
			attempts: append([]*upstreamAttempt(nil), attempts...),
		}
		ginCtx.Set(logging.APIResponseSnapshotterContextKey, mem)
	} else {
		if !mem.closed {
			mem.attempts = append(mem.attempts[:0], attempts...)
		}
		mem.mu.Unlock()
	}
	return &APIResponseStreamLogger{
		ctx:           ctx,
		capture:       capture,
		attempt:       attempt,
		memoryCapture: mem,
	}
}

// AppendChunk appends an upstream response chunk to the bound request attempt.
func (l *APIResponseStreamLogger) AppendChunk(chunk []byte) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.capture != nil && l.capture.IsClosed() {
		return
	}
	if l.ctx != nil && l.ctx.Err() != nil {
		return
	}
	if l.deferredOnly {
		l.capture.AppendResponseChunk(chunk)
		return
	}
	data := bytes.TrimSpace(chunk)
	if len(data) == 0 {
		return
	}
	if l.responseSource != nil {
		l.attempt.mu.Lock()
		defer l.attempt.mu.Unlock()
		appendChunkToAttempt(l.attempt, data, func(payload []byte) bool {
			updateTrailingNewlines(l.attempt, payload)
			if errWrite := l.responseSource.AppendBytes(payload); errWrite != nil {
				if (l.capture == nil || !l.capture.IsClosed()) && (l.ctx == nil || l.ctx.Err() == nil) {
					log.WithError(errWrite).Warn("failed to append api response log part")
				}
				return false
			}
			return true
		})
		return
	}
	if l.memoryCapture == nil {
		return
	}
	l.memoryCapture.mu.Lock()
	defer l.memoryCapture.mu.Unlock()
	if l.memoryCapture.closed {
		return
	}
	appendChunkToAttempt(l.attempt, data, func(payload []byte) bool {
		updateTrailingNewlines(l.attempt, payload)
		if l.attempt.response == nil {
			l.attempt.response = &strings.Builder{}
		}
		l.attempt.response.Write(payload)
		return true
	})
}

// RecordError retains a local stream failure on the bound request capture.
func (l *APIResponseStreamLogger) RecordError(err error) {
	if l == nil || err == nil || l.capture == nil {
		return
	}
	l.capture.RecordResponseError(err)
}

// AppendAPIResponseChunk appends an upstream response chunk to Gin context for request logging.
func AppendAPIResponseChunk(ctx context.Context, cfg *config.Config, chunk []byte) {
	if !requestLogCaptureEnabled(cfg) {
		appendDeferredResponseChunk(ctx, cfg, chunk)
		return
	}
	data := bytes.TrimSpace(chunk)
	if len(data) == 0 {
		return
	}
	ginCtx := ginContextFrom(ctx)
	if ginCtx == nil {
		return
	}
	mem := memoryResponseCaptureFrom(ginCtx)
	if mem != nil {
		mem.mu.Lock()
		defer mem.mu.Unlock()
	}
	attempts, attempt := ensureAttempt(ginCtx)
	ensureResponseIntro(ginCtx, attempt)

	appendChunkToAttempt(attempt, data, func(payload []byte) bool {
		writeAttemptResponse(ginCtx, attempt, payload)
		return true
	})
	if mem != nil && !mem.closed {
		mem.attempts = append(mem.attempts[:0], attempts...)
	}

	updateAggregatedResponseIfMemoryBacked(ginCtx, attempts)
}

func appendChunkToAttempt(attempt *upstreamAttempt, data []byte, write func([]byte) bool) {
	if attempt == nil || len(data) == 0 {
		return
	}
	if !attempt.headersWritten {
		builder := &strings.Builder{}
		builder.WriteString("Headers:\n")
		writeHeaders(builder, nil)
		if !write([]byte(builder.String())) {
			return
		}
		attempt.headersWritten = true
		if !write([]byte("\n")) {
			return
		}
	}
	if !attempt.bodyStarted {
		if !write([]byte("Body:\n")) {
			return
		}
		attempt.bodyStarted = true
	}
	currentChunkIsSSEEvent := bytes.HasPrefix(data, []byte("event:"))
	currentChunkIsSSEData := bytes.HasPrefix(data, []byte("data:"))
	if attempt.bodyHasContent {
		separator := "\n\n"
		if attempt.prevWasSSEEvent && currentChunkIsSSEData {
			separator = "\n"
		}
		if !write([]byte(separator)) {
			return
		}
	}
	if !write(data) {
		return
	}
	attempt.bodyHasContent = true
	attempt.prevWasSSEEvent = currentChunkIsSSEEvent
}

// RecordAPIWebsocketRequest stores an upstream websocket request event in Gin context.
func RecordAPIWebsocketRequest(ctx context.Context, cfg *config.Config, info UpstreamRequestLog) {
	if cfg == nil || cfg.CommercialMode {
		return
	}
	if !cfg.RequestLog {
		RecordAPIRequest(ctx, cfg, info)
		return
	}
	ginCtx := ginContextFrom(ctx)
	if ginCtx == nil {
		return
	}

	builder := &strings.Builder{}
	builder.WriteString(fmt.Sprintf("Timestamp: %s\n", time.Now().Format(time.RFC3339Nano)))
	builder.WriteString("Event: api.websocket.request\n")
	if info.URL != "" {
		builder.WriteString(fmt.Sprintf("Upstream URL: %s\n", info.URL))
	}
	if auth := formatAuthInfo(info); auth != "" {
		builder.WriteString(fmt.Sprintf("Auth: %s\n", auth))
	}
	builder.WriteString("Headers:\n")
	writeHeaders(builder, info.Headers)
	builder.WriteString("\nBody:\n")
	if len(info.Body) > 0 {
		builder.Write(info.Body)
	} else {
		builder.WriteString("<empty>")
	}
	builder.WriteString("\n")

	appendAPIWebsocketTimeline(ginCtx, []byte(builder.String()))
}

// RecordAPIWebsocketHandshake stores the upstream websocket handshake response metadata.
func RecordAPIWebsocketHandshake(ctx context.Context, cfg *config.Config, status int, headers http.Header) {
	logging.SetResponseHeaders(ctx, headers)
	if !requestLogCaptureEnabled(cfg) {
		recordDeferredResponseMetadata(ctx, cfg, status, headers)
		return
	}
	ginCtx := ginContextFrom(ctx)
	if ginCtx == nil {
		return
	}

	builder := &strings.Builder{}
	builder.WriteString(fmt.Sprintf("Timestamp: %s\n", time.Now().Format(time.RFC3339Nano)))
	builder.WriteString("Event: api.websocket.handshake\n")
	if status > 0 {
		builder.WriteString(fmt.Sprintf("Status: %d\n", status))
	}
	builder.WriteString("Headers:\n")
	writeHeaders(builder, headers)
	builder.WriteString("\n")

	appendAPIWebsocketTimeline(ginCtx, []byte(builder.String()))
}

// RecordAPIWebsocketUpgradeRejection stores a rejected websocket upgrade as an HTTP attempt.
func RecordAPIWebsocketUpgradeRejection(ctx context.Context, cfg *config.Config, info UpstreamRequestLog, status int, headers http.Header, body []byte) {
	logging.SetResponseHeaders(ctx, headers)
	if cfg == nil || cfg.CommercialMode {
		return
	}
	// Check the immutable policy and request-scoped capture before accessing the pooled Gin context.
	if !cfg.RequestLog {
		if logging.DeferredAPIRequestCaptureDisabled(ctx) {
			return
		}
		deferOrReplacePendingAPIRequest(ctx, cfg, info)
		RecordAPIResponseMetadata(ctx, cfg, status, headers)
		AppendAPIResponseChunk(ctx, cfg, body)
		return
	}
	ginCtx := ginContextFrom(ctx)
	if ginCtx == nil {
		return
	}

	RecordAPIRequest(ctx, cfg, info)
	RecordAPIResponseMetadata(ctx, cfg, status, headers)
	AppendAPIResponseChunk(ctx, cfg, body)
}

// WebsocketUpgradeRequestURL converts a websocket URL back to its HTTP handshake URL for logging.
func WebsocketUpgradeRequestURL(rawURL string) string {
	trimmedURL := strings.TrimSpace(rawURL)
	if trimmedURL == "" {
		return ""
	}
	parsed, err := url.Parse(trimmedURL)
	if err != nil {
		return trimmedURL
	}
	switch strings.ToLower(parsed.Scheme) {
	case "ws":
		parsed.Scheme = "http"
	case "wss":
		parsed.Scheme = "https"
	}
	return parsed.String()
}

// AppendAPIWebsocketResponse stores an upstream websocket response frame in Gin context.
func AppendAPIWebsocketResponse(ctx context.Context, cfg *config.Config, payload []byte) {
	if !requestLogCaptureEnabled(cfg) {
		appendDeferredResponseChunk(ctx, cfg, payload)
		return
	}
	data := bytes.TrimSpace(payload)
	if len(data) == 0 {
		return
	}
	ginCtx := ginContextFrom(ctx)
	if ginCtx == nil {
		return
	}
	markAPIResponseTimestamp(ginCtx)

	builder := &strings.Builder{}
	builder.WriteString(fmt.Sprintf("Timestamp: %s\n", time.Now().Format(time.RFC3339Nano)))
	builder.WriteString("Event: api.websocket.response\n")
	builder.Write(data)
	builder.WriteString("\n")

	appendAPIWebsocketTimeline(ginCtx, []byte(builder.String()))
}

// AppendCodexAPIWebsocketResponse stores a codex upstream websocket response frame and merges any
// quota event headers carried by the frame into the request log.
func AppendCodexAPIWebsocketResponse(ctx context.Context, cfg *config.Config, payload []byte) {
	logging.MergeResponseHeaders(ctx, ParseCodexQuotaEventHeaders(payload))
	AppendAPIWebsocketResponse(ctx, cfg, payload)
}

// RecordAPIWebsocketError stores an upstream websocket error event in Gin context.
func RecordAPIWebsocketError(ctx context.Context, cfg *config.Config, stage string, err error) {
	if err == nil {
		return
	}
	if !requestLogCaptureEnabled(cfg) {
		stageErr := err
		if trimmed := strings.TrimSpace(stage); trimmed != "" {
			stageErr = fmt.Errorf("%s: %w", trimmed, err)
		}
		recordDeferredResponseError(ctx, cfg, stageErr)
		return
	}
	ginCtx := ginContextFrom(ctx)
	if ginCtx == nil {
		return
	}
	markAPIResponseTimestamp(ginCtx)

	builder := &strings.Builder{}
	builder.WriteString(fmt.Sprintf("Timestamp: %s\n", time.Now().Format(time.RFC3339Nano)))
	builder.WriteString("Event: api.websocket.error\n")
	if trimmed := strings.TrimSpace(stage); trimmed != "" {
		builder.WriteString(fmt.Sprintf("Stage: %s\n", trimmed))
	}
	builder.WriteString(fmt.Sprintf("Error: %s\n", err.Error()))

	appendAPIWebsocketTimeline(ginCtx, []byte(builder.String()))
}

func ginContextFrom(ctx context.Context) *gin.Context {
	ginCtx, _ := ctx.Value("gin").(*gin.Context)
	return ginCtx
}

func getAttempts(ginCtx *gin.Context) []*upstreamAttempt {
	if ginCtx == nil {
		return nil
	}
	if value, exists := ginCtx.Get(apiAttemptsKey); exists {
		if attempts, ok := value.([]*upstreamAttempt); ok {
			return attempts
		}
	}
	return nil
}

func ensureAttempt(ginCtx *gin.Context) ([]*upstreamAttempt, *upstreamAttempt) {
	attempts := getAttempts(ginCtx)
	if len(attempts) == 0 {
		attempt := &upstreamAttempt{
			index:          1,
			response:       &strings.Builder{},
			responseSource: apiResponseSourceOrNil(ginCtx),
		}
		if source, ok := apiRequestSource(ginCtx); ok {
			if errWrite := source.AppendBytes([]byte("=== API REQUEST 1 ===\n<missing>\n\n")); errWrite != nil {
				log.WithError(errWrite).Warn("failed to append missing api request log part")
				attempt.request = "=== API REQUEST 1 ===\n<missing>\n\n"
			}
		} else {
			attempt.request = "=== API REQUEST 1 ===\n<missing>\n\n"
		}
		attempts = []*upstreamAttempt{attempt}
		ginCtx.Set(apiAttemptsKey, attempts)
		if attempt.request != "" {
			updateAggregatedRequest(ginCtx, attempts)
		}
	}
	return attempts, attempts[len(attempts)-1]
}

func ensureResponseIntro(ginCtx *gin.Context, attempt *upstreamAttempt) {
	if attempt == nil || attempt.response == nil || attempt.responseIntroWritten {
		return
	}
	attempts := getAttempts(ginCtx)
	for i := len(attempts) - 1; i >= 0; i-- {
		previousAttempt := attempts[i]
		if previousAttempt == nil || previousAttempt == attempt {
			continue
		}
		previousAttempt.mu.Lock()
		introWritten := previousAttempt.responseIntroWritten
		trailingNewlines := previousAttempt.trailingNewlines
		previousAttempt.mu.Unlock()
		if !introWritten {
			continue
		}
		if missingNewlines := 2 - trailingNewlines; missingNewlines > 0 {
			writeAttemptResponse(ginCtx, attempt, []byte(strings.Repeat("\n", missingNewlines)))
		}
		break
	}
	writeAttemptResponse(ginCtx, attempt, []byte(fmt.Sprintf("=== API RESPONSE %d ===\n", attempt.index)))
	writeAttemptResponse(ginCtx, attempt, []byte(fmt.Sprintf("Timestamp: %s\n", time.Now().Format(time.RFC3339Nano))))
	writeAttemptResponse(ginCtx, attempt, []byte("\n"))
	attempt.responseIntroWritten = true
}

func updateTrailingNewlines(attempt *upstreamAttempt, payload []byte) {
	if attempt == nil || len(payload) == 0 {
		return
	}
	trailingNewlines := 0
	for i := len(payload) - 1; i >= 0 && payload[i] == '\n'; i-- {
		trailingNewlines++
	}
	if trailingNewlines == len(payload) {
		trailingNewlines += attempt.trailingNewlines
	}
	attempt.trailingNewlines = trailingNewlines
}

func writeAttemptResponse(ginCtx *gin.Context, attempt *upstreamAttempt, payload []byte) {
	if attempt == nil || len(payload) == 0 {
		return
	}
	updateTrailingNewlines(attempt, payload)
	if attempt.responseSource == nil {
		attempt.responseSource = apiResponseSourceOrNil(ginCtx)
	}
	if attempt.responseSource != nil {
		if errWrite := attempt.responseSource.AppendBytes(payload); errWrite == nil {
			if ginCtx != nil {
				ginCtx.Set(logging.APIResponseCapturedContextKey, true)
			}
			return
		} else {
			log.WithError(errWrite).Warn("failed to append api response log part")
			attempt.responseSource = nil
		}
	}
	if attempt.response == nil {
		attempt.response = &strings.Builder{}
	}
	attempt.response.Write(payload)
	if ginCtx != nil {
		ginCtx.Set(logging.APIResponseCapturedContextKey, true)
	}
}

func updateAggregatedRequest(ginCtx *gin.Context, attempts []*upstreamAttempt) {
	if ginCtx == nil {
		return
	}
	var builder strings.Builder
	for _, attempt := range attempts {
		builder.WriteString(attempt.request)
	}
	ginCtx.Set(apiRequestKey, []byte(builder.String()))
}

func updateAggregatedResponseIfMemoryBacked(ginCtx *gin.Context, attempts []*upstreamAttempt) {
	if apiResponseSourceOrNil(ginCtx) != nil {
		return
	}
	updateAggregatedResponse(ginCtx, attempts)
}

func renderAggregatedResponse(attempts []*upstreamAttempt) []byte {
	var builder strings.Builder
	for _, attempt := range attempts {
		if attempt == nil || attempt.response == nil {
			continue
		}
		responseText := attempt.response.String()
		if responseText == "" {
			continue
		}
		builder.WriteString(responseText)
	}
	if responseText := builder.String(); responseText != "" && !strings.HasSuffix(responseText, "\n") {
		builder.WriteString("\n")
	}
	if builder.Len() == 0 {
		return nil
	}
	return []byte(builder.String())
}

func updateAggregatedResponse(ginCtx *gin.Context, attempts []*upstreamAttempt) {
	if ginCtx == nil {
		return
	}
	ginCtx.Set(apiResponseKey, renderAggregatedResponse(attempts))
}

func apiRequestSource(ginCtx *gin.Context) (*logging.FileBodySource, bool) {
	return fileBodySourceFromGin(ginCtx, logging.APIRequestSourceContextKey)
}

func apiResponseSourceOrNil(ginCtx *gin.Context) *logging.FileBodySource {
	source, ok := fileBodySourceFromGin(ginCtx, logging.APIResponseSourceContextKey)
	if !ok {
		return nil
	}
	return source
}

func appendAPIWebsocketTimeline(ginCtx *gin.Context, chunk []byte) {
	if ginCtx == nil {
		return
	}
	data := bytes.TrimSpace(chunk)
	if len(data) == 0 {
		return
	}
	if source, ok := apiWebsocketTimelineSource(ginCtx); ok {
		if errAppend := source.AppendPart(data); errAppend == nil {
			return
		} else {
			log.WithError(errAppend).Warn("failed to append api websocket timeline log part")
		}
	}
	if existing, exists := ginCtx.Get(apiWebsocketTimelineKey); exists {
		if existingBytes, ok := existing.([]byte); ok && len(existingBytes) > 0 {
			combined := make([]byte, 0, len(existingBytes)+len(data)+2)
			combined = append(combined, existingBytes...)
			if !bytes.HasSuffix(existingBytes, []byte("\n")) {
				combined = append(combined, '\n')
			}
			combined = append(combined, '\n')
			combined = append(combined, data...)
			ginCtx.Set(apiWebsocketTimelineKey, combined)
			return
		}
	}
	ginCtx.Set(apiWebsocketTimelineKey, bytes.Clone(data))
}

func apiWebsocketTimelineSource(ginCtx *gin.Context) (*logging.FileBodySource, bool) {
	return fileBodySourceFromGin(ginCtx, logging.APIWebsocketTimelineSourceContextKey)
}

func fileBodySourceFromGin(ginCtx *gin.Context, key string) (*logging.FileBodySource, bool) {
	if ginCtx == nil {
		return nil, false
	}
	value, exists := ginCtx.Get(key)
	if !exists {
		return nil, false
	}
	source, ok := value.(*logging.FileBodySource)
	return source, ok && source != nil
}

func markAPIResponseTimestamp(ginCtx *gin.Context) {
	if ginCtx == nil {
		return
	}
	if _, exists := ginCtx.Get("API_RESPONSE_TIMESTAMP"); exists {
		return
	}
	ginCtx.Set("API_RESPONSE_TIMESTAMP", time.Now())
}

func writeHeaders(builder *strings.Builder, headers http.Header) {
	if builder == nil {
		return
	}
	if len(headers) == 0 {
		builder.WriteString("<none>\n")
		return
	}
	keys := make([]string, 0, len(headers))
	for key := range headers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		values := headers[key]
		if len(values) == 0 {
			builder.WriteString(fmt.Sprintf("%s:\n", key))
			continue
		}
		for _, value := range values {
			masked := util.MaskSensitiveHeaderValue(key, value)
			builder.WriteString(fmt.Sprintf("%s: %s\n", key, masked))
		}
	}
}

func formatAuthInfo(info UpstreamRequestLog) string {
	var parts []string
	if trimmed := strings.TrimSpace(info.Provider); trimmed != "" {
		parts = append(parts, fmt.Sprintf("provider=%s", trimmed))
	}
	if trimmed := strings.TrimSpace(info.AuthID); trimmed != "" {
		parts = append(parts, fmt.Sprintf("auth_id=%s", trimmed))
	}
	if trimmed := strings.TrimSpace(info.AuthLabel); trimmed != "" {
		parts = append(parts, fmt.Sprintf("label=%s", trimmed))
	}

	authType := strings.ToLower(strings.TrimSpace(info.AuthType))
	authValue := strings.TrimSpace(info.AuthValue)
	switch authType {
	case "api_key":
		if authValue != "" {
			parts = append(parts, fmt.Sprintf("type=api_key value=%s", util.HideAPIKey(authValue)))
		} else {
			parts = append(parts, "type=api_key")
		}
	case "oauth":
		parts = append(parts, "type=oauth")
	default:
		if authType != "" {
			if authValue != "" {
				parts = append(parts, fmt.Sprintf("type=%s value=%s", authType, authValue))
			} else {
				parts = append(parts, fmt.Sprintf("type=%s", authType))
			}
		}
	}

	return strings.Join(parts, ", ")
}

func SummarizeErrorBody(contentType string, body []byte) string {
	isHTML := strings.Contains(strings.ToLower(contentType), "text/html")
	if !isHTML {
		trimmed := bytes.TrimSpace(bytes.ToLower(body))
		if bytes.HasPrefix(trimmed, []byte("<!doctype html")) || bytes.HasPrefix(trimmed, []byte("<html")) {
			isHTML = true
		}
	}
	if isHTML {
		if title := extractHTMLTitle(body); title != "" {
			return title
		}
		return "[html body omitted]"
	}

	// Try to extract error message from JSON response
	if message := extractJSONErrorMessage(body); message != "" {
		return message
	}

	return string(body)
}

func extractHTMLTitle(body []byte) string {
	lower := bytes.ToLower(body)
	start := bytes.Index(lower, []byte("<title"))
	if start == -1 {
		return ""
	}
	gt := bytes.IndexByte(lower[start:], '>')
	if gt == -1 {
		return ""
	}
	start += gt + 1
	end := bytes.Index(lower[start:], []byte("</title>"))
	if end == -1 {
		return ""
	}
	title := string(body[start : start+end])
	title = html.UnescapeString(title)
	title = strings.TrimSpace(title)
	if title == "" {
		return ""
	}
	return strings.Join(strings.Fields(title), " ")
}

// extractJSONErrorMessage attempts to extract error.message from JSON error responses
func extractJSONErrorMessage(body []byte) string {
	result := ojson.GetBytesNoCopy(body, "error.message")
	if result.Exists() && result.String() != "" {
		return strings.Clone(result.String())
	}
	return ""
}

// logWithRequestID returns a logrus Entry with request_id field populated from context.
// If no request ID is found in context, it returns the standard logger.
func LogWithRequestID(ctx context.Context) *log.Entry {
	if ctx == nil {
		return log.NewEntry(log.StandardLogger())
	}
	requestID := logging.GetRequestID(ctx)
	if requestID == "" {
		return log.NewEntry(log.StandardLogger())
	}
	return log.WithField("request_id", requestID)
}

// MarkCreditsUsed flags the request as having used AI credits for billing.
func MarkCreditsUsed(ctx context.Context) {
	ginCtx := ginContextFrom(ctx)
	if ginCtx != nil {
		ginCtx.Set(creditsUsedKey, true)
	}
}

// CreditsUsed returns true if the request used AI credits.
func CreditsUsed(ctx context.Context) bool {
	ginCtx := ginContextFrom(ctx)
	if ginCtx != nil {
		if val, exists := ginCtx.Get(creditsUsedKey); exists {
			if b, ok := val.(bool); ok {
				return b
			}
		}
	}
	return false
}
