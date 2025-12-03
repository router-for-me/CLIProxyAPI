package amp

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http/httputil"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	log "github.com/sirupsen/logrus"
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
		log.WithFields(fields).Infof("[amp] using local provider for model: %s", requestedModel)

	case RouteTypeModelMapping:
		fields["cost"] = "free"
		fields["source"] = "local_oauth"
		fields["mapping"] = requestedModel + " -> " + resolvedModel
		log.WithFields(fields).Infof("[amp] model mapped: %s -> %s", requestedModel, resolvedModel)

	case RouteTypeAmpCredits:
		fields["cost"] = "amp_credits"
		fields["source"] = "ampcode.com"
		fields["model_id"] = requestedModel // Explicit model_id for easy config reference
		log.WithFields(fields).Warnf("[amp] forwarding to ampcode.com (uses amp credits) - model_id: %s | To use local proxy, add to config: amp-model-mappings: [{from: \"%s\", to: \"<your-local-model>\"}]", requestedModel, requestedModel)

	case RouteTypeNoProvider:
		fields["cost"] = "none"
		fields["source"] = "error"
		fields["model_id"] = requestedModel // Explicit model_id for easy config reference
		log.WithFields(fields).Warnf("[amp] no provider available for model_id: %s", requestedModel)
	}
}

// FallbackHandler wraps a standard handler with fallback logic to ampcode.com
// when the model's provider is not available in CLIProxyAPI
type FallbackHandler struct {
	getProxy    func() *httputil.ReverseProxy
	modelMapper ModelMapper
}

// NewFallbackHandler creates a new fallback handler wrapper
// The getProxy function allows lazy evaluation of the proxy (useful when proxy is created after routes)
func NewFallbackHandler(getProxy func() *httputil.ReverseProxy) *FallbackHandler {
	return &FallbackHandler{
		getProxy: getProxy,
	}
}

// NewFallbackHandlerWithMapper creates a new fallback handler with model mapping support
func NewFallbackHandlerWithMapper(getProxy func() *httputil.ReverseProxy, mapper ModelMapper) *FallbackHandler {
	return &FallbackHandler{
		getProxy:    getProxy,
		modelMapper: mapper,
	}
}

// SetModelMapper sets the model mapper for this handler (allows late binding)
func (fh *FallbackHandler) SetModelMapper(mapper ModelMapper) {
	fh.modelMapper = mapper
}

// WrapHandler wraps a gin.HandlerFunc with fallback logic
// If the model's provider is not configured in CLIProxyAPI, it forwards to ampcode.com
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

		// Normalize model (handles Gemini thinking suffixes)
		normalizedModel, _ := util.NormalizeGeminiThinkingModel(modelName)

		// Check if we have providers for this model
		providers := util.GetProviderName(normalizedModel)

		// Track resolved model for logging (may change if mapping is applied)
		resolvedModel := normalizedModel
		usedMapping := false

		if len(providers) == 0 {
			// No providers configured - check if we have a model mapping
			if fh.modelMapper != nil {
				if mappedModel := fh.modelMapper.MapModel(normalizedModel); mappedModel != "" {
					// Mapping found - rewrite the model in request body
					bodyBytes = rewriteModelInBody(bodyBytes, mappedModel)
					c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))
					resolvedModel = mappedModel
					usedMapping = true

					// Get providers for the mapped model
					providers = util.GetProviderName(mappedModel)

					// Continue to handler with remapped model
					goto handleRequest
				}
			}

			// No mapping found - check if we have a proxy for fallback
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

	handleRequest:

		// Log the routing decision
		providerName := ""
		if len(providers) > 0 {
			providerName = providers[0]
		}

		if usedMapping {
			// Log: Model was mapped to another model
			logAmpRouting(RouteTypeModelMapping, modelName, resolvedModel, providerName, requestPath)
		} else if len(providers) > 0 {
			// Log: Using local provider (free)
			logAmpRouting(RouteTypeLocalProvider, modelName, resolvedModel, providerName, requestPath)
		}

		// Providers available or no proxy for fallback, restore body and use normal handler
		// Filter Anthropic-Beta header to remove features requiring special subscription
		// This is needed when using local providers (bypassing the Amp proxy)
		if betaHeader := c.Request.Header.Get("Anthropic-Beta"); betaHeader != "" {
			filtered := filterBetaFeatures(betaHeader, "context-1m-2025-08-07")
			if filtered != "" {
				c.Request.Header.Set("Anthropic-Beta", filtered)
			} else {
				c.Request.Header.Del("Anthropic-Beta")
			}
		}

		c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		handler(c)
	}
}

// rewriteModelInBody replaces the model name in a JSON request body
func rewriteModelInBody(body []byte, newModel string) []byte {
	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		log.Warnf("amp model mapping: failed to parse body for rewrite: %v", err)
		return body
	}

	if _, exists := payload["model"]; exists {
		payload["model"] = newModel
		newBody, err := json.Marshal(payload)
		if err != nil {
			log.Warnf("amp model mapping: failed to marshal rewritten body: %v", err)
			return body
		}
		return newBody
	}

	return body
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
