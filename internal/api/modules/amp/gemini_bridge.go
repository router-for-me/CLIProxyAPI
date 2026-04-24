package amp

import (
	"strings"

	"github.com/gin-gonic/gin"
)

// createGeminiBridgeHandler creates a handler that bridges AMP CLI's non-standard Gemini paths
// to our standard Gemini handler by rewriting the request context.
//
// AMP CLI format: /publishers/google/models/gemini-3-pro-preview:streamGenerateContent
// Standard format: /models/gemini-3-pro-preview:streamGenerateContent
//
// This extracts the model+method from the AMP path and sets it as the :action parameter
// so the standard Gemini handler can process it.
//
// The handler parameter should be a Gemini-compatible handler that expects the :action param.
func createGeminiBridgeHandler(handler gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		// Get the full path from the catch-all parameter
		path := c.Param("path")

		// Extract model:method from AMP CLI path format
		// Example: /publishers/google/models/gemini-3-pro-preview:streamGenerateContent
		const modelsPrefix = "/models/"
		if idx := strings.Index(path, modelsPrefix); idx >= 0 {
			// Extract everything after modelsPrefix
			actionPart := path[idx+len(modelsPrefix):]

			// Check if model was mapped by FallbackHandler
			if mappedModel, exists := c.Get(MappedModelContextKey); exists {
				if strModel, ok := mappedModel.(string); ok && strModel != "" {
					// Replace the model part in the action
					// actionPart is like "model-name:method"
					if colonIdx := strings.Index(actionPart, ":"); colonIdx > 0 {
						method := actionPart[colonIdx:] // ":method"
						actionPart = strModel + method
					}
				}
			}

			// Set this as the :action parameter that the Gemini handler expects
			c.Params = append(c.Params, gin.Param{
				Key:   "action",
				Value: actionPart,
			})

			// Call the handler
			handler(c)
			return
		}

		// If we can't parse the path, return 400
		c.JSON(400, gin.H{
			"error": "Invalid Gemini API path format",
		})
	}
}
