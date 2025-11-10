package amp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	log "github.com/sirupsen/logrus"
)

// FallbackHandler wraps a standard handler with fallback logic to ampcode.com
// when the model's provider is not available in CLIProxyAPI.
// With hybrid mode, it can also route to LiteLLM based on configuration.
type FallbackHandler struct {
	config       *config.Config
	oauthHandler gin.HandlerFunc
	getProxy     func() *httputil.ReverseProxy // Amp upstream proxy
	liteLLMProxy *httputil.ReverseProxy         // LiteLLM proxy for hybrid routing
}

// NewFallbackHandler creates a new fallback handler wrapper
// The getProxy function allows lazy evaluation of the Amp proxy (useful when proxy is created after routes)
// liteLLMProxy can be nil if LiteLLM is not configured
func NewFallbackHandler(cfg *config.Config, oauthHandler gin.HandlerFunc, getProxy func() *httputil.ReverseProxy, liteLLMProxy *httputil.ReverseProxy) *FallbackHandler {
	return &FallbackHandler{
		config:       cfg,
		oauthHandler: oauthHandler,
		getProxy:     getProxy,
		liteLLMProxy: liteLLMProxy,
	}
}

// WrapHandler wraps a gin.HandlerFunc with intelligent routing logic:
// 1. Primary routing: Explicit models in litellm-models list → LiteLLM
// 2. OAuth routing: Models with configured OAuth providers → OAuth (with fallback)
// 3. Unknown models: Fallback to LiteLLM if enabled, otherwise Amp upstream
func (fh *FallbackHandler) WrapHandler(handler gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Read the request body to extract the model name
		bodyBytes, err := io.ReadAll(c.Request.Body)
		if err != nil {
			log.Errorf("amp routing: failed to read request body: %v", err)
			handler(c)
			return
		}

		// Restore the body for the handler to read
		c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))

		// Try to extract model from request body or URL path (for Gemini)
		modelName := extractModelFromRequest(bodyBytes, c)
		if modelName == "" {
			// Can't determine model, proceed with normal handler
			handler(c)
			return
		}

		// Normalize model (handles Gemini thinking suffixes)
		normalizedModel, _ := util.NormalizeGeminiThinkingModel(modelName)

		// STEP 1: PRIMARY ROUTING - Check if model explicitly configured for LiteLLM
		if fh.shouldRouteLiteLLM(normalizedModel) {
			log.Infof("amp routing: model %s → LiteLLM (explicit config)", normalizedModel)
			c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			fh.liteLLMProxy.ServeHTTP(c.Writer, c.Request)
			return
		}

		// STEP 2: Check if we have OAuth providers for this model
		providers := util.GetProviderName(normalizedModel)

		if len(providers) > 0 {
			// OAuth providers available - try OAuth with fallback support

			// Filter Anthropic-Beta header to remove features requiring special subscription
			if betaHeader := c.Request.Header.Get("Anthropic-Beta"); betaHeader != "" {
				filtered := filterBetaFeatures(betaHeader, "context-1m-2025-08-07")
				if filtered != "" {
					c.Request.Header.Set("Anthropic-Beta", filtered)
				} else {
					c.Request.Header.Del("Anthropic-Beta")
				}
			}

			// For streaming requests, skip error-based fallback (too complex to buffer)
			if isStreamingRequest(c) {
				log.Debugf("amp routing: model %s → OAuth (streaming, no fallback)", normalizedModel)
				c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))
				handler(c)
				return
			}

			// Non-streaming: Use response recorder to capture OAuth response for potential fallback
			recorder := httptest.NewRecorder()

			// Restore body for OAuth handler
			c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))

			// Call OAuth handler with recorder
			origWriter := c.Writer
			c.Writer = &responseWriterWrapper{ResponseWriter: recorder, ginWriter: origWriter}
			handler(c)
			c.Writer = origWriter

			// STEP 3: ERROR-BASED FALLBACK - Check if OAuth failed and fallback is enabled
			if fh.shouldFallbackToLiteLLM(recorder) {
				log.Warnf("amp routing: OAuth failed for %s (status %d), falling back to LiteLLM", normalizedModel, recorder.Code)
				c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))
				fh.liteLLMProxy.ServeHTTP(c.Writer, c.Request)
				return
			}

			// OAuth succeeded or fallback disabled - return OAuth response
			copyResponse(c.Writer, recorder)
			return
		}

		// STEP 4: No OAuth providers - check LiteLLM fallback for unknown models
		if fh.config.LiteLLMFallbackEnabled && fh.liteLLMProxy != nil {
			log.Infof("amp routing: model %s has no OAuth provider, trying LiteLLM", normalizedModel)
			c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			fh.liteLLMProxy.ServeHTTP(c.Writer, c.Request)
			return
		}

		// STEP 5: FINAL FALLBACK - Amp upstream (ampcode.com)
		proxy := fh.getProxy()
		if proxy != nil {
			log.Infof("amp routing: model %s → Amp upstream (no OAuth, no LiteLLM)", normalizedModel)
			c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			proxy.ServeHTTP(c.Writer, c.Request)
			return
		}

		// No fallback available, let the handler return an error
		log.Debugf("amp routing: model %s has no providers and no fallback available", normalizedModel)
		handler(c)
	}
}

// responseWriterWrapper wraps httptest.ResponseRecorder to implement gin.ResponseWriter
type responseWriterWrapper struct {
	http.ResponseWriter
	ginWriter gin.ResponseWriter
}

func (w *responseWriterWrapper) WriteHeader(code int) {
	w.ResponseWriter.WriteHeader(code)
}

func (w *responseWriterWrapper) Write(data []byte) (int, error) {
	return w.ResponseWriter.Write(data)
}

func (w *responseWriterWrapper) WriteHeaderNow() {
	// No-op for recorder
}

func (w *responseWriterWrapper) Status() int {
	if rw, ok := w.ResponseWriter.(*httptest.ResponseRecorder); ok {
		return rw.Code
	}
	return 0
}

func (w *responseWriterWrapper) Size() int {
	if rw, ok := w.ResponseWriter.(*httptest.ResponseRecorder); ok {
		return rw.Body.Len()
	}
	return 0
}

func (w *responseWriterWrapper) Written() bool {
	return w.Size() > 0
}

func (w *responseWriterWrapper) Pusher() http.Pusher {
	return nil
}

func (w *responseWriterWrapper) CloseNotify() <-chan bool {
	// Return a channel that never closes (deprecated method)
	return make(<-chan bool)
}

func (w *responseWriterWrapper) Flush() {
	// No-op for recorder - httptest.ResponseRecorder doesn't support flushing
}

func (w *responseWriterWrapper) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	// Not supported for response recorder
	return nil, nil, http.ErrNotSupported
}

func (w *responseWriterWrapper) WriteString(s string) (int, error) {
	return w.Write([]byte(s))
}

// extractModelFromRequest attempts to extract the model name from various request formats
func extractModelFromRequest(body []byte, c *gin.Context) string {
	// First try to parse from JSON body (OpenAI, Claude, etc.)
	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err == nil {
		// Check common model field names
		if model, ok := payload["model"].(string); ok {
			return model
		}
	}

	// For Gemini requests, model is in the URL path: /models/{model}:generateContent
	// Extract from :action parameter (e.g., "gemini-pro:generateContent")
	if action := c.Param("action"); action != "" {
		// Split by colon to get model name (e.g., "gemini-pro:generateContent" -> "gemini-pro")
		parts := strings.Split(action, ":")
		if len(parts) > 0 && parts[0] != "" {
			return parts[0]
		}
	}

	return ""
}

// shouldRouteLiteLLM checks if the given model should be explicitly routed to LiteLLM
func (fh *FallbackHandler) shouldRouteLiteLLM(model string) bool {
	if !fh.config.LiteLLMHybridMode || fh.liteLLMProxy == nil {
		return false
	}

	// Check if model is in the explicit LiteLLM models list
	for _, m := range fh.config.LiteLLMModels {
		if m == model {
			return true
		}
	}

	return false
}

// shouldFallbackToLiteLLM determines if an OAuth error should trigger fallback to LiteLLM
func (fh *FallbackHandler) shouldFallbackToLiteLLM(recorder *httptest.ResponseRecorder) bool {
	if !fh.config.LiteLLMFallbackEnabled || fh.liteLLMProxy == nil {
		return false
	}

	statusCode := recorder.Code

	// Fallback on specific error codes
	fallbackCodes := []int{
		http.StatusUnauthorized,          // 401 - Auth error
		http.StatusPaymentRequired,       // 402 - Quota/payment required
		http.StatusTooManyRequests,       // 429 - Rate limit
		http.StatusServiceUnavailable,    // 503 - Service unavailable
	}

	for _, code := range fallbackCodes {
		if statusCode == code {
			return true
		}
	}

	// Check response body for quota-related keywords
	body := recorder.Body.String()
	quotaKeywords := []string{
		"quota",
		"rate limit",
		"insufficient_quota",
		"credit",
		"billing",
	}

	bodyLower := strings.ToLower(body)
	for _, keyword := range quotaKeywords {
		if strings.Contains(bodyLower, keyword) {
			return true
		}
	}

	return false
}

// isStreamingRequest detects if the request is expecting a streaming response
func isStreamingRequest(c *gin.Context) bool {
	// Check Accept header
	if c.GetHeader("Accept") == "text/event-stream" {
		return true
	}

	// Check stream query parameter
	if c.Query("stream") == "true" {
		return true
	}

	// Check stream in request body
	if c.Request.Body != nil {
		// Note: Body is already read by WrapHandler, so this is safe
		// We just need to check if there's a stream field
		return false // Body will be checked in WrapHandler
	}

	return false
}

// copyResponse copies a recorded response to the actual response writer
func copyResponse(w gin.ResponseWriter, recorder *httptest.ResponseRecorder) {
	// Copy headers
	for k, v := range recorder.Header() {
		for _, vv := range v {
			w.Header().Add(k, vv)
		}
	}

	// Write status code
	w.WriteHeader(recorder.Code)

	// Write body
	w.Write(recorder.Body.Bytes())
}
