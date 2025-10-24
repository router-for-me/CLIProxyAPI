package amp

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http/httputil"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	log "github.com/sirupsen/logrus"
)

// FallbackHandler wraps a standard handler with fallback logic to ampcode.com
// when the model's provider is not available in CLIProxyAPI
type FallbackHandler struct {
	getProxy func() *httputil.ReverseProxy
}

// NewFallbackHandler creates a new fallback handler wrapper
// The getProxy function allows lazy evaluation of the proxy (useful when proxy is created after routes)
func NewFallbackHandler(getProxy func() *httputil.ReverseProxy) *FallbackHandler {
	return &FallbackHandler{
		getProxy: getProxy,
	}
}

// WrapHandler wraps a gin.HandlerFunc with fallback logic
// If the model's provider is not configured in CLIProxyAPI, it forwards to ampcode.com
func (fh *FallbackHandler) WrapHandler(handler gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
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

		if len(providers) == 0 {
			// No providers configured - check if we have a proxy for fallback
			proxy := fh.getProxy()
			if proxy != nil {
				// Fallback to ampcode.com
				log.Infof("amp fallback: model %s has no configured provider, forwarding to ampcode.com", modelName)

				// Restore body again for the proxy
				c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))

				// Forward to ampcode.com
				proxy.ServeHTTP(c.Writer, c.Request)
				return
			}

			// No proxy available, let the normal handler return the error
			log.Debugf("amp fallback: model %s has no configured provider and no proxy available", modelName)
		}

		// Providers available or no proxy for fallback, restore body and use normal handler
		c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		handler(c)
	}
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
