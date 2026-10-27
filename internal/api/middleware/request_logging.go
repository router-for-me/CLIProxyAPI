// Package middleware provides HTTP middleware components for the CLI Proxy API server.
// This file contains the request logging middleware that captures comprehensive
// request and response data when enabled through configuration.
package middleware

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	log "github.com/sirupsen/logrus"
)

const (
	maxErrorOnlyCapturedRequestBodyBytes       int64  = 1 << 20  // 1 MiB
	maxDeferredErrorRequestBodyBytes           int64  = 32 << 20 // 32 MiB
	maxCapturedZstdWindowBytes                        = 8 << 20  // 8 MiB: covers every zstd level up to 19
	authenticatedRequestBodyCapturerContextKey string = "CPA_AUTHENTICATED_REQUEST_BODY_CAPTURER"
)

// RequestLoggingMiddleware creates a Gin middleware that logs HTTP requests and responses.
// It captures detailed information about the request and response, including headers and body,
// and uses the provided RequestLogger to record this data. When full request logging is disabled,
// large and unknown-size bodies are spooled to disk and retained only for error logs.
func RequestLoggingMiddleware(logger logging.RequestLogger) gin.HandlerFunc {
	return func(c *gin.Context) {
		if logger == nil {
			c.Next()
			return
		}

		if shouldSkipMethodForRequestLogging(c.Request) {
			c.Next()
			return
		}

		path := c.Request.URL.Path
		if !shouldLogRequest(path) {
			c.Next()
			return
		}

		loggerEnabled := logger.IsEnabled()
		captureBody := shouldCaptureRequestBody(loggerEnabled, c.Request)

		// Capture request metadata before authentication without reading the request body.
		requestInfo, err := captureRequestInfo(c, false)
		if err != nil {
			c.Next()
			return
		}

		// Create response writer wrapper
		wrapper := NewResponseWriterWrapper(c.Writer, logger, requestInfo)
		if !loggerEnabled {
			wrapper.logOnErrorOnly = true
		}
		c.Writer = wrapper
		attachRequestLogSources(c, logger, loggerEnabled)
		deferredCapture := attachDeferredRequestBodyCapture(c.Request, logger, requestInfo, false, false)
		if captureBody {
			c.Set(authenticatedRequestBodyCapturerContextKey, func() {
				if deferredCapture != nil && deferredCapture.bytesRead == 0 {
					c.Request.Body = deferredCapture.body
					requestInfo.deferredBodyCapture = nil
					deferredCapture.Cleanup()
					deferredCapture = nil
				}
				if errCapture := captureRequestInfoBody(c, requestInfo); errCapture != nil {
					var maxBytesErr *http.MaxBytesError
					if errors.As(errCapture, &maxBytesErr) {
						c.Header("Connection", "close")
						c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{"error": errCapture.Error()})
					}
				}
			})
		}

		// Process the request
		c.Next()

		// Finalize logging after request processing
		if err = wrapper.Finalize(c); err != nil {
			// Log error but don't interrupt the response
			// In a real implementation, you might want to use a proper logger here
		}
	}
}

type fileBodySourceFactory interface {
	NewFileBodySource(prefix string) (*logging.FileBodySource, error)
}

// CaptureAuthenticatedRequestBody eagerly captures the request body when full request
// logging is enabled after the request has passed authentication.
func CaptureAuthenticatedRequestBody(c *gin.Context) {
	if c == nil {
		return
	}
	value, exists := c.Get(authenticatedRequestBodyCapturerContextKey)
	if !exists {
		return
	}
	c.Set(authenticatedRequestBodyCapturerContextKey, nil)
	capturer, ok := value.(func())
	if !ok || capturer == nil {
		return
	}
	capturer()
}

type deferredRequestBodyCapture struct {
	body          io.ReadCloser
	factory       fileBodySourceFactory
	memBuf        bytes.Buffer
	file          *os.File
	source        *logging.FileBodySource
	contentLength int64
	bytesRead     int64
	bytesCaptured int64
	captureErr    error
	finished      bool
	sawEOF        bool
	skipCapture   bool
	truncated     bool
}

func attachDeferredRequestBodyCapture(req *http.Request, logger logging.RequestLogger, requestInfo *RequestInfo, loggerEnabled, bodyCaptured bool) *deferredRequestBodyCapture {
	if loggerEnabled || bodyCaptured || req == nil || req.Body == nil || req.Body == http.NoBody || (req.ContentLength == 0 && len(req.TransferEncoding) == 0) || requestInfo == nil {
		return nil
	}
	contentType := strings.ToLower(strings.TrimSpace(req.Header.Get("Content-Type")))
	skipCapture := strings.HasPrefix(contentType, "multipart/form-data") || isResponsesWebsocketUpgrade(req)
	factory, _ := logger.(fileBodySourceFactory)
	capture := &deferredRequestBodyCapture{
		body:          req.Body,
		factory:       factory,
		contentLength: req.ContentLength,
		skipCapture:   skipCapture,
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
	if c.skipCapture || c.captureErr != nil || c.finished {
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
	chunk := payload[:int(writeLength)]

	if c.file == nil && c.source == nil {
		if c.bytesCaptured+writeLength <= maxErrorOnlyCapturedRequestBodyBytes || c.factory == nil {
			_, _ = c.memBuf.Write(chunk)
			c.bytesCaptured += writeLength
			return n, errRead
		}
		source, errSource := c.factory.NewFileBodySource("request-body")
		if errSource != nil {
			c.captureErr = errSource
			return n, errRead
		}
		file, errPart := source.CreatePart("body")
		if errPart != nil {
			_ = source.Cleanup()
			c.captureErr = errPart
			return n, errRead
		}
		c.source = source
		c.file = file
		if c.memBuf.Len() > 0 {
			buffered := c.memBuf.Bytes()
			writtenBuf, errWriteBuf := c.file.Write(buffered)
			if errWriteBuf != nil {
				c.captureErr = errWriteBuf
			} else if writtenBuf != len(buffered) {
				c.captureErr = io.ErrShortWrite
			}
			c.memBuf.Reset()
			if c.captureErr != nil {
				if errClose := c.file.Close(); errClose != nil {
					c.captureErr = fmt.Errorf("%v; close capture file: %w", c.captureErr, errClose)
				}
				c.file = nil
				return n, errRead
			}
		}
	}

	if c.file == nil {
		return n, errRead
	}
	written, errWrite := c.file.Write(chunk)
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
	if c == nil || c.skipCapture {
		return nil, "", nil
	}
	if errFinish := c.Finish(); errFinish != nil {
		return nil, "", errFinish
	}
	if c.source != nil {
		body, errBytes := c.source.Bytes()
		if errBytes != nil {
			return nil, "", errBytes
		}
		return body, c.statusMarker(), nil
	}
	var body []byte
	if c.memBuf.Len() > 0 {
		body = bytes.Clone(c.memBuf.Bytes())
	}
	return body, c.statusMarker(), nil
}

func (c *deferredRequestBodyCapture) isComplete() bool {
	if c == nil {
		return true
	}
	return c.sawEOF || (c.contentLength >= 0 && c.bytesRead >= c.contentLength)
}

func (c *deferredRequestBodyCapture) statusMarker() string {
	if c == nil {
		return ""
	}
	var markers []string
	if c.truncated {
		markers = append(markers, fmt.Sprintf("[REQUEST BODY TRUNCATED: captured first %d bytes]", c.bytesCaptured))
	}
	if !c.isComplete() {
		if c.contentLength >= 0 {
			markers = append(markers, fmt.Sprintf("[REQUEST BODY CAPTURE INCOMPLETE: consumed %d of %d bytes]", c.bytesRead, c.contentLength))
		} else {
			markers = append(markers, fmt.Sprintf("[REQUEST BODY CAPTURE INCOMPLETE: consumed %d bytes from an unknown-length body]", c.bytesRead))
		}
	}
	return strings.Join(markers, "\n")
}

func (c *deferredRequestBodyCapture) Cleanup() {
	if c == nil {
		return
	}
	if errFinish := c.Finish(); errFinish != nil {
		log.WithError(errFinish).Warn("failed to finish deferred request body capture")
	}
	c.memBuf.Reset()
	if c.source == nil {
		return
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
	if !loggerEnabled {
		return false
	}
	return req != nil && req.Body != nil
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
	var headers map[string][]string
	if c.Request.Header != nil {
		headers = c.Request.Header.Clone()
	} else {
		headers = make(map[string][]string)
	}

	info := &RequestInfo{
		URL:       url,
		Method:    method,
		Headers:   headers,
		RequestID: logging.GetGinRequestID(c),
		Timestamp: time.Now(),
	}
	if captureBody {
		if errCapture := captureRequestInfoBody(c, info); errCapture != nil {
			return nil, errCapture
		}
	}
	return info, nil
}

func captureRequestInfoBody(c *gin.Context, info *RequestInfo) error {
	if c == nil || c.Request == nil || c.Request.Body == nil || info == nil {
		return nil
	}
	bodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return err
	}
	c.Request.Body = io.NopCloser(bytes.NewBuffer(bodyBytes))
	// The decoded body is bounded even when no max-request-body-bytes is configured:
	// a small zstd body can expand to gigabytes.
	maxBytes := c.GetInt64("maxRequestBodyBytes")
	if maxBytes <= 0 || maxBytes > maxDeferredErrorRequestBodyBytes {
		maxBytes = maxDeferredErrorRequestBodyBytes
	}
	info.Body = decodeCapturedRequestBodyForLogWithLimit(bodyBytes, c.Request.Header.Get("Content-Encoding"), maxBytes)
	return nil
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

func decodeCapturedZstdRequestBodyWithLimit(raw []byte, limit int64) ([]byte, bool, error) {
	// One decoder goroutine and a bounded window: a frame that declares a larger window is not decoded (the raw bytes are logged).
	decoder, errNewReader := zstd.NewReader(bytes.NewReader(raw), zstd.WithDecoderConcurrency(1), zstd.WithDecoderMaxWindow(maxCapturedZstdWindowBytes))
	if errNewReader != nil {
		return nil, false, fmt.Errorf("failed to create zstd request decoder: %w", errNewReader)
	}
	defer decoder.Close()

	var reader io.Reader = decoder
	if limit > 0 && limit < math.MaxInt64 {
		reader = io.LimitReader(decoder, limit+1)
	}
	decoded, errRead := io.ReadAll(reader)
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
	if strings.HasPrefix(path, "/v0/management") || strings.HasPrefix(path, "/v8/management") || strings.HasPrefix(path, "/management") {
		return false
	}

	return true
}
