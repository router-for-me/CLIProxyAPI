package util

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	log "github.com/sirupsen/logrus"
)

// RewritePathForLiteLLM transforms paths to formats expected by LiteLLM.
// This function is idempotent and can be called multiple times on the same path.
//
// Transformations:
//   - Vertex AI Gemini: /v1beta1/publishers/google/models/{model}:{action} -> /v1beta/models/{model}:{action}
//   - /api/provider/:provider prefix stripped
//   - Model name mappings applied from config.LiteLLMModelMappings
//   - Already-standard paths returned unchanged
//
// Examples:
//   - /v1beta1/publishers/google/models/gemini-3-pro-preview:streamGenerateContent
//     -> /v1beta/models/gemini-3-pro-preview:streamGenerateContent
//   - /v1beta1/publishers/google/models/gemini-2.5-flash-preview-09-2025:generateContent
//     -> /v1beta/models/gemini-flash:generateContent (with model mapping)
//   - /v1/messages -> /v1/messages (unchanged)
//   - /v1beta/models/gemini-1.5-pro:generateContent -> unchanged (idempotent)
func RewritePathForLiteLLM(path string, cfg *config.Config) string {
	// Idempotent check: If already in standard Gemini form, return unchanged
	// LiteLLM supports /v1beta/models and /v1/models; we preserve /v1beta to match existing behavior
	if strings.HasPrefix(path, "/v1beta/models/") || strings.HasPrefix(path, "/v1/models/") {
		return path
	}

	// Handle Vertex AI Gemini paths:
	// - /v1beta1/publishers/google/models/{model}:{action}
	// - /v1/publishers/google/models/{model}:{action}
	// We convert to: /v1beta/models/{mappedModel}:{action}
	if strings.Contains(path, "/publishers/google/models/") {
		// Extract model and action from Vertex AI path
		// Example: /v1beta1/publishers/google/models/gemini-2.5-flash-preview-09-2025:generateContent
		parts := strings.Split(path, "/models/")
		if len(parts) >= 2 {
			modelAndAction := parts[1] // gemini-2.5-flash-preview-09-2025:generateContent or just model
			model := modelAndAction
			action := ""

			// Split model and action if colon present
			if idx := strings.Index(modelAndAction, ":"); idx >= 0 {
				model = modelAndAction[:idx]   // gemini-2.5-flash-preview-09-2025
				action = modelAndAction[idx:]  // :generateContent (includes colon)
			}

			// Apply model name mapping if configured
			if cfg != nil && cfg.LiteLLMModelMappings != nil {
				if mappedModel, found := cfg.LiteLLMModelMappings[model]; found {
					// Validate that mapped model name is not empty
					if strings.TrimSpace(mappedModel) == "" {
						log.Warnf("LiteLLM path rewrite: mapped model for %s is empty, using original", model)
					} else {
						log.Debugf("LiteLLM path rewrite: mapped model %s -> %s", model, mappedModel)
						model = mappedModel
					}
				}
			}

			// Validate final model name is not empty before constructing path
			if strings.TrimSpace(model) == "" {
				log.Warnf("LiteLLM path rewrite: model name is empty in Vertex AI path, returning original path")
				return path
			}

			// Normalize to standard Gemini path expected by LiteLLM
			// We prefer /v1beta to match current expectations in LiteLLM for Gemini
			return "/v1beta/models/" + model + action
		}
	}

	// No change for other providers/paths (OpenAI, Claude, etc.)
	return path
}
