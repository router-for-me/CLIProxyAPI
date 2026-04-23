// Package middleware provides Gin HTTP middleware for the CLI Proxy API server.
// It includes a sophisticated response writer wrapper designed to capture and log request and response data,
// including support for streaming responses, without impacting latency.
package middleware

import (
	"bytes"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/logging"
)

const requestBodyOverrideContextKey = "REQUEST_BODY_OVERRIDE"
const responseBodyOverrideContextKey = "RESPONSE_BODY_OVERRIDE"
const websocketTimelineOverrideContextKey = "WEBSOCKET_TIMELINE_OVERRIDE"

// RequestInfo holds essential details of an incoming HTTP request for logging purposes.
type RequestInfo struct {
	URL       string              // URL is the request URL.
	Method    string              // Method is the HTTP method (e.g., GET, POST).
	Headers   map[string][]string // Headers contains the request headers.
	Body      []byte              // Body is the raw request body.
	RequestID string              // RequestID is the unique identifier for the request.
	Timestamp time.Time           // Timestamp is when the request was received.
}

// ResponseWriterWrapper wraps the standard gin.ResponseWriter to intercept and log response data.
// It is designed to handle both standard and streaming responses while keeping the
// logging path off the client-facing critical path.
type ResponseWriterWrapper struct {
	gin.ResponseWriter
	body                bytes.Buffer               // body stores the response body for non-streaming responses.
	isStreaming         bool                       // isStreaming indicates whether the response is a streaming type (e.g., text/event-stream).
	streamWriter        logging.StreamingLogWriter // streamWriter is a writer for handling streaming log entries.
	logger              logging.RequestLogger      // logger is the instance of the request logger service.
	requestInfo         *RequestInfo               // requestInfo holds the details of the original request.
	statusCode          int                        // statusCode stores the HTTP status code of the response.
	headers             map[string][]string        // headers stores the response headers.
	headersCaptured     bool                       // headersCaptured indicates whether headers have been snapshotted for this response.
	streamStatePrepared bool                       // streamStatePrepared indicates whether streaming detection/init already ran.
	logOnErrorOnly      bool                       // logOnErrorOnly enables logging only when an error response is detected.
	firstChunkTimestamp time.Time                  // firstChunkTimestamp captures TTFB for streaming responses.
}

// NewResponseWriterWrapper creates and initializes a new ResponseWriterWrapper.
// It takes the original gin.ResponseWriter, a logger instance, and request information.
//
// Parameters:
//   - w: The original gin.ResponseWriter to wrap.
//   - logger: The logging service to use for recording requests.
//   - requestInfo: The pre-captured information about the incoming request.
//
// Returns:
//   - A pointer to a new ResponseWriterWrapper.
func NewResponseWriterWrapper(w gin.ResponseWriter, logger logging.RequestLogger, requestInfo *RequestInfo) *ResponseWriterWrapper {
	return &ResponseWriterWrapper{
		ResponseWriter: w,
		logger:         logger,
		requestInfo:    requestInfo,
		headers:        make(map[string][]string),
	}
}

// Write wraps the underlying ResponseWriter's Write method to capture response data.
// For non-streaming responses, it writes to an internal buffer. For streaming responses,
// it sends data chunks to a non-blocking channel for asynchronous logging.
// CRITICAL: This method prioritizes writing to the client to ensure zero latency,
// handling logging operations subsequently.
func (w *ResponseWriterWrapper) Write(data []byte) (int, error) {
	w.prepareWrite(http.StatusOK)

	// CRITICAL: Write to client first (zero latency)
	n, err := w.ResponseWriter.Write(data)

	// THEN: Handle logging based on response type
	if w.isStreaming && w.streamWriter != nil {
		// Capture TTFB on first chunk (synchronous, before async channel send)
		if w.firstChunkTimestamp.IsZero() {
			w.firstChunkTimestamp = time.Now()
		}
		w.streamWriter.WriteChunkAsync(bytes.Clone(data))
		return n, err
	}

	if w.shouldBufferResponseBody() {
		_, _ = w.body.Write(data)
	}

	return n, err
}

func (w *ResponseWriterWrapper) shouldBufferResponseBody() bool {
	if w.logger != nil && w.logger.IsEnabled() {
		return true
	}
	if !w.logOnErrorOnly {
		return false
	}
	status := w.statusCode
	if status == 0 {
		if statusWriter, ok := w.ResponseWriter.(interface{ Status() int }); ok && statusWriter != nil {
			status = statusWriter.Status()
		} else {
			status = http.StatusOK
		}
	}
	return status >= http.StatusBadRequest
}

// WriteString wraps the underlying ResponseWriter's WriteString method to capture response data.
// Some handlers (and fmt/io helpers) write via io.StringWriter; without this override, those writes
// bypass Write() and would be missing from request logs.
func (w *ResponseWriterWrapper) WriteString(data string) (int, error) {
	w.prepareWrite(http.StatusOK)

	// CRITICAL: Write to client first (zero latency)
	n, err := w.ResponseWriter.WriteString(data)

	// THEN: Capture for logging
	if w.isStreaming && w.streamWriter != nil {
		// Capture TTFB on first chunk (synchronous, before async channel send)
		if w.firstChunkTimestamp.IsZero() {
			w.firstChunkTimestamp = time.Now()
		}
		w.streamWriter.WriteChunkAsync([]byte(data))
		return n, err
	}

	if w.shouldBufferResponseBody() {
		_, _ = w.body.WriteString(data)
	}
	return n, err
}

// WriteHeader wraps the underlying ResponseWriter's WriteHeader method.
// It captures the status code, detects if the response is streaming based on the Content-Type header,
// and initializes the appropriate logging mechanism (standard or streaming).
func (w *ResponseWriterWrapper) WriteHeader(statusCode int) {
	w.prepareWrite(statusCode)

	// Call original WriteHeader
	w.ResponseWriter.WriteHeader(statusCode)
}

func (w *ResponseWriterWrapper) prepareWrite(statusCode int) {
	if statusCode > 0 && w.statusCode == 0 {
		w.statusCode = statusCode
	}
	if !w.headersCaptured {
		w.captureCurrentHeaders()
		w.headersCaptured = true
	}
	if w.streamStatePrepared {
		return
	}
	w.streamStatePrepared = true
	contentType := w.ResponseWriter.Header().Get("Content-Type")
	w.isStreaming = w.detectStreaming(contentType)
	if !w.isStreaming || w.logger == nil || !w.logger.IsEnabled() || w.requestInfo == nil {
		return
	}

	streamWriter, err := w.logger.LogStreamingRequest(
		w.requestInfo.URL,
		w.requestInfo.Method,
		w.requestInfo.Headers,
		w.requestInfo.Body,
		w.requestInfo.RequestID,
	)
	if err != nil {
		return
	}
	w.streamWriter = streamWriter
	_ = streamWriter.WriteStatus(w.statusCode, w.headers)
}

// captureCurrentHeaders reads all headers from the underlying ResponseWriter and stores them
// in the wrapper's headers map. It creates copies of the header values to prevent race conditions.
func (w *ResponseWriterWrapper) captureCurrentHeaders() {
	// Initialize headers map if needed
	if w.headers == nil {
		w.headers = make(map[string][]string)
	} else {
		clear(w.headers)
	}

	// Capture all current headers from the underlying ResponseWriter
	for key, values := range w.ResponseWriter.Header() {
		// Make a copy of the values slice to avoid reference issues
		headerValues := make([]string, len(values))
		copy(headerValues, values)
		w.headers[key] = headerValues
	}
}

// detectStreaming determines if a response should be treated as a streaming response.
// It checks for a "text/event-stream" Content-Type or a '"stream": true'
// field in the original request body.
func (w *ResponseWriterWrapper) detectStreaming(contentType string) bool {
	// Check Content-Type for Server-Sent Events
	if strings.Contains(contentType, "text/event-stream") {
		return true
	}

	// If a concrete Content-Type is already set (e.g., application/json for error responses),
	// treat it as non-streaming instead of inferring from the request payload.
	if strings.TrimSpace(contentType) != "" {
		return false
	}

	// Only fall back to request payload hints when Content-Type is not set yet.
	if w.requestInfo != nil && len(w.requestInfo.Body) > 0 {
		return bytes.Contains(w.requestInfo.Body, []byte(`"stream": true`)) ||
			bytes.Contains(w.requestInfo.Body, []byte(`"stream":true`))
	}

	return false
}

// Finalize completes the logging process for the request and response.
// For streaming responses, it flushes metadata to the streaming log writer and closes it.
// For non-streaming responses, it logs the complete request and response details,
// including any API-specific request/response data stored in the Gin context.
func (w *ResponseWriterWrapper) Finalize(c *gin.Context) error {
	if w.logger == nil {
		return nil
	}

	finalStatusCode := w.statusCode
	if finalStatusCode == 0 {
		if statusWriter, ok := w.ResponseWriter.(interface{ Status() int }); ok {
			finalStatusCode = statusWriter.Status()
		} else {
			finalStatusCode = 200
		}
	}

	var slicesAPIResponseError []*interfaces.ErrorMessage
	apiResponseError, isExist := c.Get("API_RESPONSE_ERROR")
	if isExist {
		if apiErrors, ok := apiResponseError.([]*interfaces.ErrorMessage); ok {
			slicesAPIResponseError = apiErrors
		}
	}

	hasAPIError := len(slicesAPIResponseError) > 0 || finalStatusCode >= http.StatusBadRequest
	forceLog := w.logOnErrorOnly && hasAPIError && !w.logger.IsEnabled()
	if !w.logger.IsEnabled() && !forceLog {
		return nil
	}

	if w.requestInfo != nil && w.requestInfo.Headers == nil {
		w.requestInfo.Headers = cloneHeaderValues(c.Request.Header)
	}

	if w.isStreaming && w.streamWriter != nil {
		w.streamWriter.SetFirstChunkTimestamp(w.firstChunkTimestamp)

		// Write API Request and Response to the streaming log before closing
		apiRequest := w.extractAPIRequest(c)
		if len(apiRequest) > 0 {
			_ = w.streamWriter.WriteAPIRequest(apiRequest)
		}
		apiResponse := w.extractAPIResponse(c)
		if len(apiResponse) > 0 {
			_ = w.streamWriter.WriteAPIResponse(apiResponse)
		}
		apiWebsocketTimeline := w.extractAPIWebsocketTimeline(c)
		if len(apiWebsocketTimeline) > 0 {
			_ = w.streamWriter.WriteAPIWebsocketTimeline(apiWebsocketTimeline)
		}
		if err := w.streamWriter.Close(); err != nil {
			w.streamWriter = nil
			return err
		}
		w.streamWriter = nil
		return nil
	}

	return w.logRequest(w.extractRequestBody(c), finalStatusCode, w.cloneHeaders(), w.extractResponseBody(c), w.extractWebsocketTimeline(c), w.extractAPIRequest(c), w.extractAPIResponse(c), w.extractAPIWebsocketTimeline(c), w.extractAPIResponseTimestamp(c), slicesAPIResponseError, forceLog)
}

func (w *ResponseWriterWrapper) cloneHeaders() map[string][]string {
	if !w.headersCaptured {
		w.captureCurrentHeaders()
		w.headersCaptured = true
	}

	finalHeaders := make(map[string][]string, len(w.headers))
	for key, values := range w.headers {
		headerValues := make([]string, len(values))
		copy(headerValues, values)
		finalHeaders[key] = headerValues
	}

	return finalHeaders
}

func (w *ResponseWriterWrapper) extractAPIRequest(c *gin.Context) []byte {
	apiRequest, isExist := c.Get("API_REQUEST")
	if !isExist {
		return nil
	}
	switch value := apiRequest.(type) {
	case []byte:
		if len(value) == 0 {
			return nil
		}
		return value
	case string:
		if len(value) == 0 {
			return nil
		}
		return []byte(value)
	case *strings.Builder:
		if value == nil || value.Len() == 0 {
			return nil
		}
		return []byte(value.String())
	}
	return nil
}

func (w *ResponseWriterWrapper) extractAPIResponse(c *gin.Context) []byte {
	apiResponse, isExist := c.Get("API_RESPONSE")
	if !isExist {
		return nil
	}
	switch value := apiResponse.(type) {
	case []byte:
		if len(value) == 0 {
			return nil
		}
		return value
	case string:
		if len(value) == 0 {
			return nil
		}
		return []byte(value)
	case *strings.Builder:
		if value == nil || value.Len() == 0 {
			return nil
		}
		return []byte(value.String())
	}
	return nil
}

func (w *ResponseWriterWrapper) extractAPIWebsocketTimeline(c *gin.Context) []byte {
	apiTimeline, isExist := c.Get("API_WEBSOCKET_TIMELINE")
	if !isExist {
		return nil
	}
	data, ok := apiTimeline.([]byte)
	if !ok || len(data) == 0 {
		return nil
	}
	return bytes.Clone(data)
}

func (w *ResponseWriterWrapper) extractAPIResponseTimestamp(c *gin.Context) time.Time {
	ts, isExist := c.Get("API_RESPONSE_TIMESTAMP")
	if !isExist {
		return time.Time{}
	}
	if t, ok := ts.(time.Time); ok {
		return t
	}
	return time.Time{}
}

func (w *ResponseWriterWrapper) extractRequestBody(c *gin.Context) []byte {
	if body := extractBodyOverride(c, requestBodyOverrideContextKey); len(body) > 0 {
		return body
	}
	if w.requestInfo != nil && len(w.requestInfo.Body) > 0 {
		return w.requestInfo.Body
	}
	return nil
}

func (w *ResponseWriterWrapper) extractResponseBody(c *gin.Context) []byte {
	if body := extractBodyOverride(c, responseBodyOverrideContextKey); len(body) > 0 {
		return body
	}
	if w.body.Len() == 0 {
		return nil
	}
	return bytes.Clone(w.body.Bytes())
}

func (w *ResponseWriterWrapper) extractWebsocketTimeline(c *gin.Context) []byte {
	return extractBodyOverride(c, websocketTimelineOverrideContextKey)
}

func extractBodyOverride(c *gin.Context, key string) []byte {
	if c == nil {
		return nil
	}
	bodyOverride, isExist := c.Get(key)
	if !isExist {
		return nil
	}
	switch value := bodyOverride.(type) {
	case []byte:
		if len(value) > 0 {
			return bytes.Clone(value)
		}
	case string:
		if strings.TrimSpace(value) != "" {
			return []byte(value)
		}
	}
	return nil
}

func (w *ResponseWriterWrapper) logRequest(requestBody []byte, statusCode int, headers map[string][]string, body, websocketTimeline, apiRequestBody, apiResponseBody, apiWebsocketTimeline []byte, apiResponseTimestamp time.Time, apiResponseErrors []*interfaces.ErrorMessage, forceLog bool) error {
	if w.requestInfo == nil {
		return nil
	}

	if loggerWithOptions, ok := w.logger.(interface {
		LogRequestWithOptions(string, string, map[string][]string, []byte, int, map[string][]string, []byte, []byte, []byte, []byte, []byte, []*interfaces.ErrorMessage, bool, string, time.Time, time.Time) error
	}); ok {
		return loggerWithOptions.LogRequestWithOptions(
			w.requestInfo.URL,
			w.requestInfo.Method,
			w.requestInfo.Headers,
			requestBody,
			statusCode,
			headers,
			body,
			websocketTimeline,
			apiRequestBody,
			apiResponseBody,
			apiWebsocketTimeline,
			apiResponseErrors,
			forceLog,
			w.requestInfo.RequestID,
			w.requestInfo.Timestamp,
			apiResponseTimestamp,
		)
	}

	return w.logger.LogRequest(
		w.requestInfo.URL,
		w.requestInfo.Method,
		w.requestInfo.Headers,
		requestBody,
		statusCode,
		headers,
		body,
		websocketTimeline,
		apiRequestBody,
		apiResponseBody,
		apiWebsocketTimeline,
		apiResponseErrors,
		w.requestInfo.RequestID,
		w.requestInfo.Timestamp,
		apiResponseTimestamp,
	)
}
