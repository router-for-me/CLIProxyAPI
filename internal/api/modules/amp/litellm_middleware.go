// litellm_middleware.go - Gin middleware for LiteLLM routing.
// This file is part of our fork-specific features and should never conflict with upstream.
// See FORK_MAINTENANCE.md for architecture details.
package amp

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http/httputil"
	"strings"

	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

// LiteLLMMiddleware creates a Gin middleware for LiteLLM routing.
// This middleware intercepts requests and routes matching models to LiteLLM
// before they reach upstream handlers.
func LiteLLMMiddleware(liteLLMCfg *LiteLLMConfig, proxy *httputil.ReverseProxy) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Skip if LiteLLM not enabled or no proxy
		if !liteLLMCfg.IsEnabled() || proxy == nil {
			c.Next()
			return
		}

		// Skip non-POST requests (GET /models, OPTIONS, etc.)
		if c.Request.Method != "POST" {
			c.Next()
			return
		}

		// Read and restore request body
		bodyBytes, err := io.ReadAll(c.Request.Body)
		if err != nil {
			log.Debugf("litellm middleware: failed to read body: %v", err)
			c.Next()
			return
		}
		c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))

		// Extract model from request
		model := extractModelFromBody(bodyBytes)
		if model == "" {
			// Try extracting from URL path (Gemini-style)
			model = extractModelFromPath(c.Request.URL.Path)
		}

		if model == "" {
			// Can't determine model, let upstream handle
			c.Next()
			return
		}

		// Check if should route to LiteLLM
		if liteLLMCfg.ShouldRouteToLiteLLM(model) {
			log.Infof("litellm routing: %s -> LiteLLM", model)

			// Restore body for proxy
			c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))

			// Route to LiteLLM and abort chain
			proxy.ServeHTTP(c.Writer, c.Request)
			c.Abort()
			return
		}

		// Not a LiteLLM model, continue to upstream handlers
		c.Next()
	}
}

// extractModelFromBody extracts the model name from a JSON request body
func extractModelFromBody(body []byte) string {
	var payload struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	return payload.Model
}

// extractModelFromPath extracts model from Gemini-style URL paths
// e.g., /v1beta1/models/gemini-pro:generateContent -> gemini-pro
func extractModelFromPath(path string) string {
	// Handle /models/{model}:{action} pattern
	if idx := strings.Index(path, "/models/"); idx >= 0 {
		modelAction := path[idx+len("/models/"):]
		if colonIdx := strings.Index(modelAction, ":"); colonIdx > 0 {
			return modelAction[:colonIdx]
		}
		// No colon, check for slash
		if slashIdx := strings.Index(modelAction, "/"); slashIdx > 0 {
			return modelAction[:slashIdx]
		}
		return modelAction
	}
	return ""
}
