package amp

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// AmpRouteType represents the type of routing decision made for an Amp request
type AmpRouteType string

const (
	// RouteTypeLocalProvider indicates the request is handled by a local OAuth provider (free)
	RouteTypeLocalProvider AmpRouteType = "LOCAL_PROVIDER"
	// RouteTypeModelMapping indicates the request was remapped to another available model (free)
	RouteTypeModelMapping AmpRouteType = "MODEL_MAPPING"
	// RouteTypeAmpCredits indicates the request is forwarded to ampcode.com (uses Amp credits)
	RouteTypeAmpCredits AmpRouteType = "AMP_CREDITS"
	// RouteTypeNoProvider indicates no provider or fallback available
	RouteTypeNoProvider AmpRouteType = "NO_PROVIDER"
)

// MappedModelContextKey is the Gin context key for passing mapped model names.
const MappedModelContextKey = "mapped_model"

// FallbackTargetsContextKey is the Gin context key for passing remaining fallback targets.
const FallbackTargetsContextKey = "fallback_targets"

// OriginalModelContextKey stores the original requested model before any mapping.
const OriginalModelContextKey = "original_model"

// ResponseCaptureWriter captures HTTP response for retry decision.
// It buffers both headers and body to allow inspection before committing.
type ResponseCaptureWriter struct {
	gin.ResponseWriter
	body       *bytes.Buffer
	statusCode int
	headers    http.Header
	committed  bool
	mu         sync.Mutex
}

// NewResponseCaptureWriter creates a new response capture writer.
func NewResponseCaptureWriter(w gin.ResponseWriter) *ResponseCaptureWriter {
	return &ResponseCaptureWriter{
		ResponseWriter: w,
		body:           &bytes.Buffer{},
		statusCode:     http.StatusOK,
		headers:        make(http.Header),
	}
}

func (w *ResponseCaptureWriter) WriteHeader(code int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.statusCode = code
}

func (w *ResponseCaptureWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.Write(data)
}

func (w *ResponseCaptureWriter) Header() http.Header {
	return w.headers
}

func (w *ResponseCaptureWriter) Status() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.statusCode
}

func (w *ResponseCaptureWriter) Body() []byte {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.body.Bytes()
}

// Reset clears the captured response for retry.
func (w *ResponseCaptureWriter) Reset() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.body.Reset()
	w.statusCode = http.StatusOK
	w.headers = make(http.Header)
}

// FlushTo writes the captured response to the original writer.
func (w *ResponseCaptureWriter) FlushTo(original gin.ResponseWriter) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.committed {
		return
	}
	w.committed = true

	for key, values := range w.headers {
		for _, value := range values {
			original.Header().Add(key, value)
		}
	}
	original.WriteHeader(w.statusCode)
	original.Write(w.body.Bytes())
}

// isRetryableStatusCode returns true if the status code indicates a retryable error.
// Based on opencode-antigravity-auth patterns: 429 (rate limit), 5xx (server errors).
func isRetryableStatusCode(statusCode int) bool {
	return statusCode == http.StatusTooManyRequests || // 429
		(statusCode >= 500 && statusCode < 600) // 5xx
}

// logAmpRouting logs the routing decision for an Amp request with structured fields
func logAmpRouting(routeType AmpRouteType, requestedModel, resolvedModel, provider, path string) {
	fields := log.Fields{
		"component":       "amp-routing",
		"route_type":      string(routeType),
		"requested_model": requestedModel,
		"path":            path,
		"timestamp":       time.Now().Format(time.RFC3339),
	}

	if resolvedModel != "" && resolvedModel != requestedModel {
		fields["resolved_model"] = resolvedModel
	}
	if provider != "" {
		fields["provider"] = provider
	}

	switch routeType {
	case RouteTypeLocalProvider:
		fields["cost"] = "free"
		fields["source"] = "local_oauth"
		log.WithFields(fields).Debugf("amp using local provider for model: %s", requestedModel)

	case RouteTypeModelMapping:
		fields["cost"] = "free"
		fields["source"] = "local_oauth"
		fields["mapping"] = requestedModel + " -> " + resolvedModel
		// model mapping already logged in mapper; avoid duplicate here

	case RouteTypeAmpCredits:
		fields["cost"] = "amp_credits"
		fields["source"] = "ampcode.com"
		fields["model_id"] = requestedModel // Explicit model_id for easy config reference
		log.WithFields(fields).Warnf("forwarding to ampcode.com (uses amp credits) - model_id: %s | To use local provider, add to config: ampcode.model-mappings: [{from: \"%s\", to: \"<your-local-model>\"}]", requestedModel, requestedModel)

	case RouteTypeNoProvider:
		fields["cost"] = "none"
		fields["source"] = "error"
		fields["model_id"] = requestedModel // Explicit model_id for easy config reference
		log.WithFields(fields).Warnf("no provider available for model_id: %s", requestedModel)
	}
}

// FallbackHandler wraps a standard handler with fallback logic to ampcode.com
// when the model's provider is not available in CLIProxyAPI
type FallbackHandler struct {
	getProxy           func() *httputil.ReverseProxy
	modelMapper        ModelMapper
	forceModelMappings func() bool
}

// NewFallbackHandler creates a new fallback handler wrapper
// The getProxy function allows lazy evaluation of the proxy (useful when proxy is created after routes)
func NewFallbackHandler(getProxy func() *httputil.ReverseProxy) *FallbackHandler {
	return &FallbackHandler{
		getProxy:           getProxy,
		forceModelMappings: func() bool { return false },
	}
}

// NewFallbackHandlerWithMapper creates a new fallback handler with model mapping support
func NewFallbackHandlerWithMapper(getProxy func() *httputil.ReverseProxy, mapper ModelMapper, forceModelMappings func() bool) *FallbackHandler {
	if forceModelMappings == nil {
		forceModelMappings = func() bool { return false }
	}
	return &FallbackHandler{
		getProxy:           getProxy,
		modelMapper:        mapper,
		forceModelMappings: forceModelMappings,
	}
}

// SetModelMapper sets the model mapper for this handler (allows late binding)
func (fh *FallbackHandler) SetModelMapper(mapper ModelMapper) {
	fh.modelMapper = mapper
}

// WrapHandler wraps a gin.HandlerFunc with fallback logic
// If the model's provider is not configured in CLIProxyAPI, it forwards to ampcode.com
// Supports model mapping with multiple fallback targets (tried in order on error).
func (fh *FallbackHandler) WrapHandler(handler gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		requestPath := c.Request.URL.Path

		// Read the request body to extract the model name
		bodyBytes, err := io.ReadAll(c.Request.Body)
		if err != nil {
			log.Errorf("amp fallback: failed to read request body: %v", err)
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

		// Normalize model (handles dynamic thinking suffixes)
		normalizedModel, _ := util.NormalizeThinkingModel(modelName)

		// Store original model for potential fallback chain
		c.Set(OriginalModelContextKey, normalizedModel)

		// Track resolved model for logging (may change if mapping is applied)
		resolvedModel := normalizedModel
		usedMapping := false
		var providers []string
		var fallbackTargets []string

		// Check if model mappings should be forced ahead of local API keys
		forceMappings := fh.forceModelMappings != nil && fh.forceModelMappings()

		if forceMappings {
			// FORCE MODE: Check model mappings FIRST (takes precedence over local API keys)
			// This allows users to route Amp requests to their preferred OAuth providers
			if fh.modelMapper != nil {
				allTargets := fh.modelMapper.GetFallbacks(normalizedModel)
				if len(allTargets) > 0 {
					// Try each target in order until we find one with available providers
					for i, target := range allTargets {
						mappedProviders := util.GetProviderName(target)
						if len(mappedProviders) > 0 {
							bodyBytes = rewriteModelInRequest(bodyBytes, target)
							c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))
							c.Set(MappedModelContextKey, target)
							resolvedModel = target
							usedMapping = true
							providers = mappedProviders
							// Store remaining targets for potential retry on error
							if i+1 < len(allTargets) {
								fallbackTargets = allTargets[i+1:]
							}
							break
						}
						log.Debugf("amp model mapping: target %s has no providers, trying next", target)
					}
				}
			}

			// If no mapping applied, check for local providers
			if !usedMapping {
				providers = util.GetProviderName(normalizedModel)
			}
		} else {
			// DEFAULT MODE: Check local providers first, then mappings as fallback
			providers = util.GetProviderName(normalizedModel)

			if len(providers) == 0 {
				// No providers configured - check if we have a model mapping
				if fh.modelMapper != nil {
					allTargets := fh.modelMapper.GetFallbacks(normalizedModel)
					if len(allTargets) > 0 {
						for i, target := range allTargets {
							mappedProviders := util.GetProviderName(target)
							if len(mappedProviders) > 0 {
								bodyBytes = rewriteModelInRequest(bodyBytes, target)
								c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))
								c.Set(MappedModelContextKey, target)
								resolvedModel = target
								usedMapping = true
								providers = mappedProviders
								if i+1 < len(allTargets) {
									fallbackTargets = allTargets[i+1:]
								}
								break
							}
							log.Debugf("amp model mapping: target %s has no providers, trying next", target)
						}
					}
				}
			}
		}

		// Store fallback targets in context for potential retry on error
		if len(fallbackTargets) > 0 {
			c.Set(FallbackTargetsContextKey, fallbackTargets)
		}

		// If no providers available, fallback to ampcode.com
		if len(providers) == 0 {
			proxy := fh.getProxy()
			if proxy != nil {
				// Log: Forwarding to ampcode.com (uses Amp credits)
				logAmpRouting(RouteTypeAmpCredits, modelName, "", "", requestPath)

				// Restore body again for the proxy
				c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))

				// Forward to ampcode.com
				proxy.ServeHTTP(c.Writer, c.Request)
				return
			}

			// No proxy available, let the normal handler return the error
			logAmpRouting(RouteTypeNoProvider, modelName, "", "", requestPath)
		}

		// Log the routing decision
		providerName := ""
		if len(providers) > 0 {
			providerName = providers[0]
		}

		if usedMapping {
			// Model mapping with fallback retry support
			fh.executeWithFallbackRetry(c, handler, bodyBytes, normalizedModel, resolvedModel, fallbackTargets, modelName, providerName, requestPath)
		} else if len(providers) > 0 {
			// Log: Using local provider (free)
			logAmpRouting(RouteTypeLocalProvider, modelName, resolvedModel, providerName, requestPath)
			// Filter Anthropic-Beta header only for local handling paths
			filterAntropicBetaHeader(c)
			c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			handler(c)
		} else {
			// No provider, no mapping, no proxy: fall back to the wrapped handler so it can return an error response
			c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))
			handler(c)
		}
	}
}

// filterAntropicBetaHeader filters Anthropic-Beta header to remove features requiring special subscription
// This is needed when using local providers (bypassing the Amp proxy)
func filterAntropicBetaHeader(c *gin.Context) {
	if betaHeader := c.Request.Header.Get("Anthropic-Beta"); betaHeader != "" {
		if filtered := filterBetaFeatures(betaHeader, "context-1m-2025-08-07"); filtered != "" {
			c.Request.Header.Set("Anthropic-Beta", filtered)
		} else {
			c.Request.Header.Del("Anthropic-Beta")
		}
	}
}

// executeWithFallbackRetry executes the handler with fallback retry on retryable errors.
// If the first model fails with a retryable error (429, 500, etc.), it tries the next fallback target.
// Based on patterns from opencode-antigravity-auth and opencode-google-antigravity-auth.
func (fh *FallbackHandler) executeWithFallbackRetry(
	c *gin.Context,
	handler gin.HandlerFunc,
	originalBody []byte,
	originalModel string,
	currentTarget string,
	fallbackTargets []string,
	requestedModel string,
	providerName string,
	requestPath string,
) {
	allTargets := append([]string{currentTarget}, fallbackTargets...)
	originalWriter := c.Writer

	var lastCapture *ResponseCaptureWriter
	var successfulTarget string

	for i, target := range allTargets {
		bodyBytes := rewriteModelInRequest(originalBody, target)
		c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))

		c.Set(MappedModelContextKey, target)

		capture := NewResponseCaptureWriter(originalWriter)
		rewriter := NewResponseRewriter(capture, originalModel)
		c.Writer = rewriter

		filterAntropicBetaHeader(c)

		if i == 0 {
			fallbackInfo := ""
			if len(fallbackTargets) > 0 {
				fallbackInfo = fmt.Sprintf(" (fallbacks: %v)", fallbackTargets)
			}
			log.Debugf("amp model mapping: request %s -> %s%s", originalModel, target, fallbackInfo)
			logAmpRouting(RouteTypeModelMapping, requestedModel, target, providerName, requestPath)
		} else {
			log.Infof("amp fallback retry: trying model %s (%d/%d)", target, i+1, len(allTargets))
		}

		handler(c)
		rewriter.Flush()

		lastCapture = capture

		if !isRetryableStatusCode(capture.Status()) {
			successfulTarget = target
			log.Debugf("amp model mapping: response %s -> %s (status: %d)", target, originalModel, capture.Status())
			break
		}

		if i+1 < len(allTargets) {
			log.Warnf("amp fallback: model %s returned %d, trying next fallback", target, capture.Status())
			capture.Reset()
		} else {
			log.Warnf("amp fallback: model %s returned %d, no more fallbacks available", target, capture.Status())
		}
	}

	if lastCapture != nil {
		lastCapture.FlushTo(originalWriter)

		if successfulTarget != "" && successfulTarget != currentTarget {
			log.Infof("amp fallback: successfully used fallback model %s", successfulTarget)
		}
	}
}

// rewriteModelInRequest replaces the model name in a JSON request body
func rewriteModelInRequest(body []byte, newModel string) []byte {
	if !gjson.GetBytes(body, "model").Exists() {
		return body
	}
	result, err := sjson.SetBytes(body, "model", newModel)
	if err != nil {
		log.Warnf("amp model mapping: failed to rewrite model in request body: %v", err)
		return body
	}
	return result
}

// extractModelFromRequest attempts to extract the model name from various request formats
func extractModelFromRequest(body []byte, c *gin.Context) string {
	// First try to parse from JSON body (OpenAI, Claude, etc.)
	// Check common model field names
	if result := gjson.GetBytes(body, "model"); result.Exists() && result.Type == gjson.String {
		return result.String()
	}

	// For Gemini requests, model is in the URL path
	// Standard format: /models/{model}:generateContent -> :action parameter
	if action := c.Param("action"); action != "" {
		// Split by colon to get model name (e.g., "gemini-pro:generateContent" -> "gemini-pro")
		parts := strings.Split(action, ":")
		if len(parts) > 0 && parts[0] != "" {
			return parts[0]
		}
	}

	// AMP CLI format: /publishers/google/models/{model}:method -> *path parameter
	// Example: /publishers/google/models/gemini-3-pro-preview:streamGenerateContent
	if path := c.Param("path"); path != "" {
		// Look for /models/{model}:method pattern
		if idx := strings.Index(path, "/models/"); idx >= 0 {
			modelPart := path[idx+8:] // Skip "/models/"
			// Split by colon to get model name
			if colonIdx := strings.Index(modelPart, ":"); colonIdx > 0 {
				return modelPart[:colonIdx]
			}
		}
	}

	return ""
}
