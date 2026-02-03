package routing

import (
	"strings"

	"github.com/tidwall/gjson"
)

// ModelExtractor extracts model names from request data.
type ModelExtractor interface {
	// Extract returns the model name from the request body and gin parameters.
	// The ginParams map contains route parameters like "action" and "path".
	Extract(body []byte, ginParams map[string]string) (string, error)
}

// DefaultModelExtractor is the standard implementation of ModelExtractor.
type DefaultModelExtractor struct{}

// NewModelExtractor creates a new DefaultModelExtractor.
func NewModelExtractor() *DefaultModelExtractor {
	return &DefaultModelExtractor{}
}

// Extract extracts the model name from the request.
// It checks in order:
// 1. JSON body "model" field (OpenAI, Claude format)
// 2. "action" parameter for Gemini standard format (e.g., "gemini-pro:generateContent")
// 3. "path" parameter for AMP CLI Gemini format (e.g., "/publishers/google/models/gemini-3-pro:streamGenerateContent")
func (e *DefaultModelExtractor) Extract(body []byte, ginParams map[string]string) (string, error) {
	// First try to parse from JSON body (OpenAI, Claude, etc.)
	if result := gjson.GetBytes(body, "model"); result.Exists() && result.Type == gjson.String {
		return result.String(), nil
	}

	// For Gemini requests, model is in the URL path
	// Standard format: /models/{model}:generateContent -> :action parameter
	if action, ok := ginParams["action"]; ok && action != "" {
		// Split by colon to get model name (e.g., "gemini-pro:generateContent" -> "gemini-pro")
		parts := strings.Split(action, ":")
		if len(parts) > 0 && parts[0] != "" {
			return parts[0], nil
		}
	}

	// AMP CLI format: /publishers/google/models/{model}:method -> *path parameter
	// Example: /publishers/google/models/gemini-3-pro-preview:streamGenerateContent
	if path, ok := ginParams["path"]; ok && path != "" {
		// Look for /models/{model}:method pattern
		if idx := strings.Index(path, "/models/"); idx >= 0 {
			modelPart := path[idx+8:] // Skip "/models/"
			// Split by colon to get model name
			if colonIdx := strings.Index(modelPart, ":"); colonIdx > 0 {
				return modelPart[:colonIdx], nil
			}
		}
	}

	return "", nil
}
