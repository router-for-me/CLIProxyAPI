// Package middleware provides HTTP middleware components for the CLI Proxy API server.
// This file contains the request logging middleware that captures comprehensive
// request and response data when enabled through configuration.
package middleware

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	log "github.com/sirupsen/logrus"
)

const (
	maxErrorOnlyCapturedRequestBodyBytes int64 = 1 << 20  // 1 MiB
	maxDeferredErrorRequestBodyBytes     int64 = 32 << 20 // 32 MiB
)

type noLogChecker interface {
	ShouldSkipLog(apiKey string) bool
}

type denyAuthenticatedRequestLogging struct{}

func (denyAuthenticatedRequestLogging) ShouldSkipLog(apiKey string) bool {
	return strings.TrimSpace(apiKey) != ""
}

func selectNoLogChecker(logger logging.RequestLogger, policies []*logging.RequestLogPolicy) noLogChecker {
	if len(policies) > 0 {
		if policies[0] == nil {
			return denyAuthenticatedRequestLogging{}
		}
		return policies[0]
	}
	if loggerChecker, ok := logger.(noLogChecker); ok {
		return loggerChecker
	}
	return denyAuthenticatedRequestLogging{}
}

func isNilLike(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

// RequestLoggingMiddleware creates a Gin middleware that logs HTTP requests and responses.
// It captures detailed information about the request and response, including headers and body,
// and uses the provided RequestLogger to record this data. When full request logging is disabled,
// large and unknown-size bodies are spooled to disk and retained only for error logs.
func RequestLoggingMiddleware(logger logging.RequestLogger, policies ...*logging.RequestLogPolicy) gin.HandlerFunc {
	if isNilLike(logger) {
		return func(c *gin.Context) {
			c.Next()
		}
	}
	checker := selectNoLogChecker(logger, policies)
	return func(c *gin.Context) {
		if shouldSkipMethodForRequestLogging(c.Request) {
			c.Next()
			return
		}

		path := c.Request.URL.Path
		if !shouldLogRequest(path) {
			c.Next()
			return
		}

		rawKey := requestAPIKey(c.Request)
		if rawKey != "" && checker.ShouldSkipLog(rawKey) {
			c.Next()
			return
		}
		requestLogger := scopeRequestLogger(logger, rawKey, false)

		loggerEnabled := requestLogger.IsEnabled()
		captureBody := shouldCaptureRequestBody(loggerEnabled, c.Request)

		// Capture request information
		requestInfo, err := captureRequestInfo(c, captureBody)
		if err != nil {
			// Log error but continue processing
			// In a real implementation, you might want to use a proper logger here
			c.Next()
			return
		}

		// Create response writer wrapper
		wrapper := NewResponseWriterWrapper(c.Writer, requestLogger, requestInfo)
		wrapper.SetAuthLogScopeSources(logger, c)
		if !loggerEnabled {
			wrapper.logOnErrorOnly = true
		}
		c.Writer = wrapper
		attachRequestLogSources(c, requestLogger, loggerEnabled)
		attachDeferredRequestBodyCapture(c.Request, requestLogger, requestInfo, loggerEnabled, captureBody)

		// Process the request
		c.Next()

		// Finalize logging after request processing
		if err = wrapper.Finalize(c); err != nil {
			// Log error but don't interrupt the response
			// In a real implementation, you might want to use a proper logger here
		}
	}
}

func requestAPIKey(req *http.Request) string {
	if req == nil {
		return ""
	}
	if parts := strings.Fields(req.Header.Get("Authorization")); len(parts) == 2 && strings.EqualFold(parts[0], "Bearer") {
		return parts[1]
	}
	for _, header := range []string{"X-Goog-Api-Key", "X-Api-Key"} {
		if value := strings.TrimSpace(req.Header.Get(header)); value != "" {
			return value
		}
	}
	if req.URL != nil {
		if value := strings.TrimSpace(req.URL.Query().Get("key")); value != "" {
			return value
		}
		return strings.TrimSpace(req.URL.Query().Get("auth_token"))
	}
	return ""
}

// resolveRequestLogScope picks the directory key for request logs after auth.
// When a frontend-auth plugin stamps metadata key_id (or Principal differs from
// the raw secret), return that label so logs land under logs/keys/<Key-ID>/.
// Native config-api-key keeps Principal == raw secret and uses fingerprinting /
// api-key-names via ForAPIKey.
func resolveRequestLogScope(c *gin.Context) (value string, asLabel bool) {
	if c == nil {
		return "", false
	}
	if id := accessMetadataKeyID(c); id != "" {
		return id, true
	}
	rawKey := requestAPIKey(c.Request)
	if principal, ok := c.Get("userApiKey"); ok {
		if p, ok := principal.(string); ok {
			p = strings.TrimSpace(p)
			if p != "" && p != rawKey {
				return p, true
			}
		}
	}
	return rawKey, false
}

func accessMetadataKeyID(c *gin.Context) string {
	if c == nil {
		return ""
	}
	raw, ok := c.Get("accessMetadata")
	if !ok || raw == nil {
		return ""
	}
	switch meta := raw.(type) {
	case map[string]string:
		return strings.TrimSpace(meta["key_id"])
	case map[string]any:
		if v, ok := meta["key_id"]; ok {
			if s, ok := v.(string); ok {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

func scopeRequestLogger(logger logging.RequestLogger, value string, asLabel bool) logging.RequestLogger {
	if logger == nil {
		return nil
	}
	if asLabel {
		if labeled, ok := logger.(interface {
			ForKeyLabel(string) logging.RequestLogger
		}); ok {
			return labeled.ForKeyLabel(value)
		}
	}
	if scoped, ok := logger.(interface {
		ForAPIKey(string) logging.RequestLogger
	}); ok {
		return scoped.ForAPIKey(value)
	}
	return logger
}

type fileBodySourceFactory interface {
	NewFileBodySource(prefix string) (*logging.FileBodySource, error)
}

type deferredRequestBodyCapture struct {
	body          io.ReadCloser
	file          *os.File
	source        *logging.FileBodySource
	contentLength int64
	bytesRead     int64
	bytesCaptured int64
	captureErr    error
	finished      bool
	sawEOF        bool
	truncated     bool
}

func attachDeferredRequestBodyCapture(req *http.Request, logger logging.RequestLogger, requestInfo *RequestInfo, loggerEnabled, bodyCaptured bool) *deferredRequestBodyCapture {
	if loggerEnabled || bodyCaptured || req == nil || req.Body == nil || req.Body == http.NoBody || req.ContentLength == 0 || requestInfo == nil {
		return nil
	}
	contentType := strings.ToLower(strings.TrimSpace(req.Header.Get("Content-Type")))
	if strings.HasPrefix(contentType, "multipart/form-data") {
		return nil
	}
	factory, ok := logger.(fileBodySourceFactory)
	if !ok || factory == nil {
		return nil
	}
	source, errSource := factory.NewFileBodySource("request-body")
	if errSource != nil {
		return nil
	}
	file, errPart := source.CreatePart("body")
	if errPart != nil {
		_ = source.Cleanup()
		return nil
	}
	capture := &deferredRequestBodyCapture{
		body:          req.Body,
		file:          file,
		source:        source,
		contentLength: req.ContentLength,
	}
	req.Body = capture
	requestInfo.deferredBodyCapture = capture
	return capture
}

func (c *deferredRequestBodyCapture) Read(payload []byte) (int, error) {
	if c == nil || c.body == nil {
		return 0, io.EOF
	}
	n, errRead := c.body.Read(payload)
	if errRead == io.EOF {
		c.sawEOF = true
	}
	if n == 0 {
		return n, errRead
	}
	c.bytesRead += int64(n)
	if c.file == nil || c.captureErr != nil {
		return n, errRead
	}

	remaining := maxDeferredErrorRequestBodyBytes - c.bytesCaptured
	if remaining <= 0 {
		c.truncated = true
		return n, errRead
	}
	writeLength := int64(n)
	if writeLength > remaining {
		writeLength = remaining
		c.truncated = true
	}
	written, errWrite := c.file.Write(payload[:int(writeLength)])
	c.bytesCaptured += int64(written)
	if errWrite != nil {
		c.captureErr = errWrite
	} else if int64(written) != writeLength {
		c.captureErr = io.ErrShortWrite
	}
	if c.captureErr != nil {
		if errClose := c.file.Close(); errClose != nil {
			c.captureErr = fmt.Errorf("%v; close capture file: %w", c.captureErr, errClose)
		}
		c.file = nil
	}
	return n, errRead
}

func (c *deferredRequestBodyCapture) Close() error {
	if c == nil {
		return nil
	}
	_ = c.Finish()
	if c.body == nil {
		return nil
	}
	return c.body.Close()
}

func (c *deferredRequestBodyCapture) Finish() error {
	if c == nil {
		return nil
	}
	if c.finished {
		return c.captureErr
	}
	c.finished = true
	if c.file != nil {
		if errClose := c.file.Close(); errClose != nil && c.captureErr == nil {
			c.captureErr = errClose
		}
		c.file = nil
	}
	return c.captureErr
}

func (c *deferredRequestBodyCapture) Bytes() ([]byte, string, error) {
	if c == nil || c.source == nil {
		return nil, "", nil
	}
	if errFinish := c.Finish(); errFinish != nil {
		return nil, "", errFinish
	}
	body, errBytes := c.source.Bytes()
	if errBytes != nil {
		return nil, "", errBytes
	}
	return body, c.statusMarker(), nil
}

func (c *deferredRequestBodyCapture) statusMarker() string {
	if c == nil {
		return ""
	}
	var markers []string
	if c.truncated {
		markers = append(markers, fmt.Sprintf("[REQUEST BODY TRUNCATED: captured first %d bytes]", c.bytesCaptured))
	}
	complete := c.sawEOF || (c.contentLength >= 0 && c.bytesRead >= c.contentLength)
	if !complete {
		if c.contentLength >= 0 {
			markers = append(markers, fmt.Sprintf("[REQUEST BODY CAPTURE INCOMPLETE: consumed %d of %d bytes]", c.bytesRead, c.contentLength))
		} else {
			markers = append(markers, fmt.Sprintf("[REQUEST BODY CAPTURE INCOMPLETE: consumed %d bytes from an unknown-length body]", c.bytesRead))
		}
	}
	return strings.Join(markers, "\n")
}

func (c *deferredRequestBodyCapture) Cleanup() {
	if c == nil || c.source == nil {
		return
	}
	if errFinish := c.Finish(); errFinish != nil {
		log.WithError(errFinish).Warn("failed to finish deferred request body capture")
	}
	if errCleanup := c.source.Cleanup(); errCleanup != nil {
		log.WithError(errCleanup).Warn("failed to clean up deferred request body capture")
	}
	c.source = nil
}

func attachRequestLogSources(c *gin.Context, logger logging.RequestLogger, loggerEnabled bool) {
	if c == nil || !loggerEnabled {
		return
	}
	factory, ok := logger.(fileBodySourceFactory)
	if !ok || factory == nil {
		return
	}
	if source, errSource := factory.NewFileBodySource("api-request"); errSource == nil {
		c.Set(logging.APIRequestSourceContextKey, source)
	}
	if source, errSource := factory.NewFileBodySource("api-response"); errSource == nil {
		c.Set(logging.APIResponseSourceContextKey, source)
	}
	if !isResponsesWebsocketUpgrade(c.Request) {
		return
	}
	if source, errSource := factory.NewFileBodySource("websocket-timeline"); errSource == nil {
		c.Set(logging.WebsocketTimelineSourceContextKey, source)
	}
	if source, errSource := factory.NewFileBodySource("api-websocket-timeline"); errSource == nil {
		c.Set(logging.APIWebsocketTimelineSourceContextKey, source)
	}
}

func shouldSkipMethodForRequestLogging(req *http.Request) bool {
	if req == nil {
		return true
	}
	if req.Method != http.MethodGet {
		return false
	}
	return !isResponsesWebsocketUpgrade(req)
}

func isResponsesWebsocketUpgrade(req *http.Request) bool {
	if req == nil || req.URL == nil {
		return false
	}
	if req.URL.Path != "/v1/responses" && req.URL.Path != "/backend-api/codex/responses" {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(req.Header.Get("Upgrade")), "websocket")
}

func shouldCaptureRequestBody(loggerEnabled bool, req *http.Request) bool {
	if loggerEnabled {
		return true
	}
	if req == nil || req.Body == nil {
		return false
	}
	contentType := strings.ToLower(strings.TrimSpace(req.Header.Get("Content-Type")))
	if strings.HasPrefix(contentType, "multipart/form-data") {
		return false
	}
	if req.ContentLength <= 0 {
		return false
	}
	return req.ContentLength <= maxErrorOnlyCapturedRequestBodyBytes
}

// captureRequestInfo extracts relevant information from the incoming HTTP request.
// It captures the URL, method, headers, and body. The request body is read and then
// restored so that it can be processed by subsequent handlers.
func captureRequestInfo(c *gin.Context, captureBody bool) (*RequestInfo, error) {
	// Capture URL with sensitive query parameters masked
	maskedQuery := util.MaskSensitiveQuery(c.Request.URL.RawQuery)
	url := c.Request.URL.Path
	if maskedQuery != "" {
		url += "?" + maskedQuery
	}

	// Capture method
	method := c.Request.Method

	// Capture headers
	headers := make(map[string][]string)
	for key, values := range c.Request.Header {
		headers[key] = values
	}

	// Capture request body
	var body []byte
	if captureBody && c.Request.Body != nil {
		// Read the body
		bodyBytes, err := io.ReadAll(c.Request.Body)
		if err != nil {
			return nil, err
		}

		// Restore the body for the actual request processing
		c.Request.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
		body = decodeCapturedRequestBodyForLog(bodyBytes, c.Request.Header.Get("Content-Encoding"))
	}

	return &RequestInfo{
		URL:       url,
		Method:    method,
		Headers:   headers,
		Body:      body,
		RequestID: logging.GetGinRequestID(c),
		Timestamp: time.Now(),
	}, nil
}

func decodeCapturedRequestBodyForLog(raw []byte, encoding string) []byte {
	if len(raw) == 0 {
		return raw
	}

	decoded, errDecode := decodeCapturedRequestBody(raw, encoding)
	if errDecode != nil {
		return raw
	}
	return decoded
}

func decodeCapturedRequestBodyForLogWithLimit(raw []byte, encoding string, limit int64) []byte {
	if len(raw) == 0 || limit <= 0 {
		return raw
	}
	encoding = strings.TrimSpace(encoding)
	if encoding == "" || strings.EqualFold(encoding, "identity") {
		return raw
	}

	parts := strings.Split(encoding, ",")
	body := raw
	for i := len(parts) - 1; i >= 0; i-- {
		enc := strings.ToLower(strings.TrimSpace(parts[i]))
		switch enc {
		case "", "identity":
			continue
		case "zstd":
			decoded, truncated, errDecode := decodeCapturedZstdRequestBodyWithLimit(body, limit)
			if errDecode != nil {
				return raw
			}
			body = decoded
			if truncated {
				if len(body) > 0 && !bytes.HasSuffix(body, []byte("\n")) {
					body = append(body, '\n')
				}
				return append(body, "[DECOMPRESSED REQUEST BODY TRUNCATED]"...)
			}
		default:
			return raw
		}
	}
	return body
}

func decodeCapturedRequestBody(raw []byte, encoding string) ([]byte, error) {
	encoding = strings.TrimSpace(encoding)
	if encoding == "" || strings.EqualFold(encoding, "identity") {
		return raw, nil
	}

	parts := strings.Split(encoding, ",")
	body := raw
	for i := len(parts) - 1; i >= 0; i-- {
		enc := strings.ToLower(strings.TrimSpace(parts[i]))
		switch enc {
		case "", "identity":
			continue
		case "zstd":
			decoded, errDecode := decodeCapturedZstdRequestBody(body)
			if errDecode != nil {
				return nil, errDecode
			}
			body = decoded
		default:
			return nil, fmt.Errorf("unsupported request content encoding: %s", enc)
		}
	}
	return body, nil
}

func decodeCapturedZstdRequestBody(raw []byte) ([]byte, error) {
	decoder, errNewReader := zstd.NewReader(bytes.NewReader(raw))
	if errNewReader != nil {
		return nil, fmt.Errorf("failed to create zstd request decoder: %w", errNewReader)
	}
	defer decoder.Close()

	decoded, errRead := io.ReadAll(decoder)
	if errRead != nil {
		return nil, fmt.Errorf("failed to decode zstd request body: %w", errRead)
	}
	return decoded, nil
}

func decodeCapturedZstdRequestBodyWithLimit(raw []byte, limit int64) ([]byte, bool, error) {
	decoder, errNewReader := zstd.NewReader(bytes.NewReader(raw))
	if errNewReader != nil {
		return nil, false, fmt.Errorf("failed to create zstd request decoder: %w", errNewReader)
	}
	defer decoder.Close()

	decoded, errRead := io.ReadAll(io.LimitReader(decoder, limit+1))
	if errRead != nil {
		return nil, false, fmt.Errorf("failed to decode zstd request body: %w", errRead)
	}
	if int64(len(decoded)) > limit {
		return decoded[:limit], true, nil
	}
	return decoded, false, nil
}

// shouldLogRequest determines whether the request should be logged.
// It skips management endpoints to avoid leaking secrets but allows
// all other routes, including module-provided ones, to honor request-log.
func shouldLogRequest(path string) bool {
	if strings.HasPrefix(path, "/v0/management") || strings.HasPrefix(path, "/management") {
		return false
	}

	return true
}
