// Package middleware provides Gin HTTP middleware for the CLI Proxy API server.
// It includes a sophisticated response writer wrapper designed to capture and log request and response data,
// including support for streaming responses, without impacting latency.
package middleware

import (
	"bytes"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/logging"
)

// RequestInfo holds essential details of an incoming HTTP request for logging purposes.
type RequestInfo struct {
	URL     string              // URL is the request URL.
	Method  string              // Method is the HTTP method (e.g., GET, POST).
	Headers map[string][]string // Headers contains the request headers.
	Body    []byte              // Body is the raw request body.
}

// ResponseWriterWrapper wraps the standard gin.ResponseWriter to intercept and log response data.
// It is designed to handle both standard and streaming responses, ensuring that logging operations do not block the client response.
type ResponseWriterWrapper struct {
	gin.ResponseWriter
	body           *bytes.Buffer              // body is a buffer to store the response body for non-streaming responses.
	isStreaming    bool                       // isStreaming indicates whether the response is a streaming type (e.g., text/event-stream).
	streamWriter   logging.StreamingLogWriter // streamWriter is a writer for handling streaming log entries.
	chunkChannel   chan []byte                // chunkChannel is a channel for asynchronously passing response chunks to the logger.
	streamDone     chan struct{}              // streamDone signals when the streaming goroutine completes.
	logger         logging.RequestLogger      // logger is the instance of the request logger service.
	requestInfo    *RequestInfo               // requestInfo holds the details of the original request.
	statusCode     int                        // statusCode stores the HTTP status code of the response.
	headers        map[string][]string        // headers stores the response headers.
	logOnErrorOnly bool                       // logOnErrorOnly enables logging only when an error response is detected.
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
		body:           &bytes.Buffer{},
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
	// Ensure headers are captured before first write
	// This is critical because Write() may trigger WriteHeader() internally
	w.ensureHeadersCaptured()

	// CRITICAL: Write to client first (zero latency)
	n, err := w.ResponseWriter.Write(data)

	// THEN: Handle logging based on response type
	if w.isStreaming {
		// For streaming responses: Send to async logging channel (non-blocking)
		if w.chunkChannel != nil {
			select {
			case w.chunkChannel <- append([]byte(nil), data...): // Non-blocking send with copy
			default: // Channel full, skip logging to avoid blocking
			}
		}
	} else {
		// For non-streaming responses: Buffer complete response
		w.body.Write(data)
	}

	return n, err
}

// WriteHeader wraps the underlying ResponseWriter's WriteHeader method.
// It captures the status code, detects if the response is streaming based on the Content-Type header,
// and initializes the appropriate logging mechanism (standard or streaming).
func (w *ResponseWriterWrapper) WriteHeader(statusCode int) {
	w.statusCode = statusCode

	// Capture response headers using the new method
	w.captureCurrentHeaders()

	// Detect streaming based on Content-Type
	contentType := w.ResponseWriter.Header().Get("Content-Type")
	w.isStreaming = w.detectStreaming(contentType)

	// If streaming, initialize streaming log writer
	if w.isStreaming && w.logger.IsEnabled() {
		streamWriter, err := w.logger.LogStreamingRequest(
			w.requestInfo.URL,
			w.requestInfo.Method,
			w.requestInfo.Headers,
			w.requestInfo.Body,
		)
		if err == nil {
			w.streamWriter = streamWriter
			w.chunkChannel = make(chan []byte, 100) // Buffered channel for async writes
			doneChan := make(chan struct{})
			w.streamDone = doneChan

			// Start async chunk processor
			go w.processStreamingChunks(doneChan)

			// Write status immediately
			_ = streamWriter.WriteStatus(statusCode, w.headers)
		}
	}

	// Call original WriteHeader
	w.ResponseWriter.WriteHeader(statusCode)
}

// ensureHeadersCaptured is a helper function to make sure response headers are captured.
// It is safe to call this method multiple times; it will always refresh the headers
// with the latest state from the underlying ResponseWriter.
func (w *ResponseWriterWrapper) ensureHeadersCaptured() {
	// Always capture the current headers to ensure we have the latest state
	w.captureCurrentHeaders()
}

// captureCurrentHeaders reads all headers from the underlying ResponseWriter and stores them
// in the wrapper's headers map. It creates copies of the header values to prevent race conditions.
func (w *ResponseWriterWrapper) captureCurrentHeaders() {
	// Initialize headers map if needed
	if w.headers == nil {
		w.headers = make(map[string][]string)
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

	// Check request body for streaming indicators
	if w.requestInfo.Body != nil {
		bodyStr := string(w.requestInfo.Body)
		if strings.Contains(bodyStr, `"stream": true`) || strings.Contains(bodyStr, `"stream":true`) {
			return true
		}
	}

	return false
}

// processStreamingChunks runs in a separate goroutine to process response chunks from the chunkChannel.
// It asynchronously writes each chunk to the streaming log writer.
func (w *ResponseWriterWrapper) processStreamingChunks(done chan struct{}) {
	if done == nil {
		return
	}

	defer close(done)

	if w.streamWriter == nil || w.chunkChannel == nil {
		return
	}

	for chunk := range w.chunkChannel {
		w.streamWriter.WriteChunkAsync(chunk)
	}
}

// Finalize completes the logging process for the request and response.
// For streaming responses, it closes the chunk channel and the stream writer.
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

	if w.isStreaming {
		if w.chunkChannel != nil {
			close(w.chunkChannel)
			w.chunkChannel = nil
		}

		if w.streamDone != nil {
			<-w.streamDone
			w.streamDone = nil
		}

		if w.streamWriter != nil {
			if err := w.streamWriter.Close(); err != nil {
				w.streamWriter = nil
				return err
			}
			w.streamWriter = nil
		}
		if forceLog {
			return w.logRequest(finalStatusCode, w.cloneHeaders(), w.body.Bytes(), w.extractAPIRequest(c), w.extractAPIResponse(c), slicesAPIResponseError, forceLog)
		}
		return nil
	}

	return w.logRequest(finalStatusCode, w.cloneHeaders(), w.body.Bytes(), w.extractAPIRequest(c), w.extractAPIResponse(c), slicesAPIResponseError, forceLog)
}

func (w *ResponseWriterWrapper) cloneHeaders() map[string][]string {
	w.ensureHeadersCaptured()

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
	data, ok := apiRequest.([]byte)
	if !ok || len(data) == 0 {
		return nil
	}
	return data
}

func (w *ResponseWriterWrapper) extractAPIResponse(c *gin.Context) []byte {
	apiResponse, isExist := c.Get("API_RESPONSE")
	if !isExist {
		return nil
	}
	data, ok := apiResponse.([]byte)
	if !ok || len(data) == 0 {
		return nil
	}
	return data
}

func (w *ResponseWriterWrapper) logRequest(statusCode int, headers map[string][]string, body []byte, apiRequestBody, apiResponseBody []byte, apiResponseErrors []*interfaces.ErrorMessage, forceLog bool) error {
	if w.requestInfo == nil {
		return nil
	}

	var requestBody []byte
	if len(w.requestInfo.Body) > 0 {
		requestBody = w.requestInfo.Body
	}

	if loggerWithOptions, ok := w.logger.(interface {
		LogRequestWithOptions(string, string, map[string][]string, []byte, int, map[string][]string, []byte, []byte, []byte, []*interfaces.ErrorMessage, bool) error
	}); ok {
		return loggerWithOptions.LogRequestWithOptions(
			w.requestInfo.URL,
			w.requestInfo.Method,
			w.requestInfo.Headers,
			requestBody,
			statusCode,
			headers,
			body,
			apiRequestBody,
			apiResponseBody,
			apiResponseErrors,
			forceLog,
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
		apiRequestBody,
		apiResponseBody,
		apiResponseErrors,
	)
}

// Status returns the HTTP response status code captured by the wrapper.
// It defaults to 200 if WriteHeader has not been called.
func (w *ResponseWriterWrapper) Status() int {
	if w.statusCode == 0 {
		return 200 // Default status code
	}
	return w.statusCode
}

// Size returns the size of the response body in bytes for non-streaming responses.
// For streaming responses, it returns -1, as the total size is unknown.
func (w *ResponseWriterWrapper) Size() int {
	if w.isStreaming {
		return -1 // Unknown size for streaming responses
	}
	return w.body.Len()
}

// Written returns true if the response header has been written (i.e., a status code has been set).
func (w *ResponseWriterWrapper) Written() bool {
	return w.statusCode != 0
}
