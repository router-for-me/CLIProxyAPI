// Package middleware provides HTTP middleware components for the CLI Proxy API server.
// This file contains the request logging middleware that captures comprehensive
// request and response data when enabled through configuration.
package middleware

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/logging"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/util"
	log "github.com/sirupsen/logrus"
)

const maxErrorOnlyCapturedRequestBodyBytes int64 = 1 << 20 // 1 MiB

// RequestLoggingMiddleware creates a Gin middleware that logs HTTP requests and responses.
// It captures detailed information about the request and response, including headers and body,
// and uses the provided RequestLogger to record this data. When full request logging is disabled,
// body capture is limited to small known-size payloads to avoid large per-request memory spikes.
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

		// Capture request information
		requestInfo, err := captureRequestInfo(c, shouldCaptureRequestBody(loggerEnabled, c.Request))
		if err != nil {
			// Log error but continue processing
			// In a real implementation, you might want to use a proper logger here
			c.Next()
			return
		}

		// Create response writer wrapper
		wrapper := NewResponseWriterWrapper(c.Writer, logger, requestInfo)
		if !loggerEnabled {
			wrapper.logOnErrorOnly = true
		}
		c.Writer = wrapper

		// Process the request
		c.Next()

		// Finalize logging after request processing
		if err = wrapper.Finalize(c); err != nil {
			log.Errorf("failed to finalize request logging: %v", err)
		}
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
	if req.URL.Path != "/v1/responses" {
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
	headers := sanitizeRequestHeaders(c.Request.Header)

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
		body = sanitizeLoggedPayloadBytes(bodyBytes)
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

func sanitizeRequestHeaders(headers http.Header) map[string][]string {
	sanitized := make(map[string][]string, len(headers))
	for key, values := range headers {
		keyLower := strings.ToLower(strings.TrimSpace(key))
		if keyLower == "authorization" || keyLower == "cookie" || keyLower == "proxy-authorization" {
			sanitized[key] = []string{"[redacted]"}
			continue
		}
<<<<<<< HEAD
		sanitizedValues := make([]string, len(values))
		for i, value := range values {
			sanitizedValues[i] = util.MaskSensitiveHeaderValue(key, value)
		}
		sanitized[key] = sanitizedValues
=======
		sanitized[key] = values
>>>>>>> archive/pr-234-head-20260223
	}
	return sanitized
}

// shouldLogRequest determines whether the request should be logged.
// It skips management endpoints to avoid leaking secrets but allows
// all other routes, including module-provided ones, to honor request-log.
func shouldLogRequest(path string) bool {
	if strings.HasPrefix(path, "/v0/management") || strings.HasPrefix(path, "/management") {
		return false
	}

	if strings.HasPrefix(path, "/api") {
		return strings.HasPrefix(path, "/api/provider")
	}

	return true
}

func sanitizeLoggedPayloadBytes(payload []byte) []byte {
	if len(payload) == 0 {
		return nil
	}

	var parsed any
	if err := json.Unmarshal(payload, &parsed); err != nil {
		return bytes.Clone(payload)
	}

	redacted := sanitizeJSONPayloadValue(parsed)
	out, err := json.Marshal(redacted)
	if err != nil {
		return bytes.Clone(payload)
	}

	return out
}

func sanitizeJSONPayloadValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		redacted := make(map[string]any, len(typed))
		for k, v := range typed {
			if isSensitivePayloadKey(k) {
				redacted[k] = "[REDACTED]"
				continue
			}
			redacted[k] = sanitizeJSONPayloadValue(v)
		}
		return redacted
	case []any:
		items := make([]any, len(typed))
		for i, item := range typed {
			items[i] = sanitizeJSONPayloadValue(item)
		}
		return items
	default:
		return typed
	}
}

func isSensitivePayloadKey(key string) bool {
	normalized := strings.ToLower(strings.TrimSpace(key))
	normalized = strings.ReplaceAll(normalized, "-", "_")
	normalized = strings.TrimPrefix(normalized, "x_")

	if normalized == "authorization" || normalized == "token" || normalized == "secret" || normalized == "password" {
		return true
	}
	if strings.Contains(normalized, "api_key") || strings.Contains(normalized, "apikey") {
		return true
	}
	if strings.Contains(normalized, "access_token") || strings.Contains(normalized, "refresh_token") || strings.Contains(normalized, "id_token") {
		return true
	}
	return false
}
