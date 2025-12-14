// litellm_fallback.go - Response interceptor middleware for LiteLLM fallback on quota errors.
// This file is part of our fork-specific features and should never conflict with upstream.
// See FORK_MAINTENANCE.md for architecture details.
package amp

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httputil"
	"strings"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// responseBuffer wraps gin.ResponseWriter to buffer the response
// until we know if we need to fallback to LiteLLM
type responseBuffer struct {
	gin.ResponseWriter
	statusCode int
	body       *bytes.Buffer
	committed  bool // true once we've started writing to the real writer
}

func newResponseBuffer(w gin.ResponseWriter) *responseBuffer {
	return &responseBuffer{
		ResponseWriter: w,
		statusCode:     http.StatusOK,
		body:           &bytes.Buffer{},
		committed:      false,
	}
}

func (rb *responseBuffer) WriteHeader(code int) {
	rb.statusCode = code
	// Don't write to real writer yet - buffer it
}

func (rb *responseBuffer) Write(data []byte) (int, error) {
	if rb.committed {
		return rb.ResponseWriter.Write(data)
	}
	return rb.body.Write(data)
}

// Flush commits the buffered response to the real writer
func (rb *responseBuffer) Flush() {
	if rb.committed {
		return
	}
	rb.committed = true
	rb.ResponseWriter.WriteHeader(rb.statusCode)
	rb.ResponseWriter.Write(rb.body.Bytes())
}

// shouldFallbackOnError checks if the response indicates a quota/rate limit error
func shouldFallbackOnError(statusCode int, body []byte) bool {
	// Only fallback on specific error codes
	switch statusCode {
	case http.StatusTooManyRequests: // 429
		return true
	case http.StatusForbidden: // 403 - often used for quota exceeded
		// Check body for quota-related keywords
		bodyStr := strings.ToLower(string(body))
		quotaKeywords := []string{
			"quota",
			"rate_limit",
			"rate limit",
			"limit exceeded",
			"too many requests",
			"resource_exhausted",
			"billing",
		}
		for _, keyword := range quotaKeywords {
			if strings.Contains(bodyStr, keyword) {
				return true
			}
		}
	case http.StatusServiceUnavailable: // 503 - sometimes used for overload
		bodyStr := strings.ToLower(string(body))
		if strings.Contains(bodyStr, "overload") || strings.Contains(bodyStr, "capacity") {
			return true
		}
	}
	return false
}

// LiteLLMFallbackMiddleware creates a Gin middleware that intercepts quota/rate limit
// errors from OAuth handlers and retries with LiteLLM.
//
// Flow:
// 1. Buffer the response from OAuth handler
// 2. If quota error (429, 403+quota) detected before streaming starts
// 3. Retry the request with LiteLLM proxy
// 4. Otherwise, flush the original response
func LiteLLMFallbackMiddleware(liteLLMCfg *LiteLLMConfig, proxy *httputil.ReverseProxy) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Skip if fallback not enabled or no proxy
		if !liteLLMCfg.IsFallbackEnabled() || proxy == nil {
			c.Next()
			return
		}

		// Skip non-POST requests
		if c.Request.Method != "POST" {
			c.Next()
			return
		}

		// Skip if this model is already explicitly routed to LiteLLM
		// (handled by LiteLLMMiddleware which runs first)
		// We only want fallback for models that tried OAuth first

		// Save request body for potential retry
		bodyBytes, err := io.ReadAll(c.Request.Body)
		if err != nil {
			log.Debugf("litellm fallback: failed to read body: %v", err)
			c.Next()
			return
		}
		c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))

		// Extract model for logging
		model := extractModelFromBody(bodyBytes)

		// Wrap response writer to buffer the response
		buffer := newResponseBuffer(c.Writer)
		c.Writer = buffer

		// Let the request go through OAuth handlers
		c.Next()

		// Check if we should fallback
		if !buffer.committed && shouldFallbackOnError(buffer.statusCode, buffer.body.Bytes()) {
			log.Warnf("litellm fallback: OAuth failed for %s (status %d), retrying with LiteLLM",
				model, buffer.statusCode)

			// Reset request body for retry
			c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))

			// Clear any headers that were set
			for key := range buffer.Header() {
				buffer.Header().Del(key)
			}

			// Retry with LiteLLM - write directly to original writer
			proxy.ServeHTTP(buffer.ResponseWriter, c.Request)
			return
		}

		// No fallback needed - flush the buffered response
		buffer.Flush()
	}
}
