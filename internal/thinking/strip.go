// Package thinking provides unified thinking configuration processing.
package thinking

import (
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// StripThinkingConfig removes thinking configuration fields from request body.
//
// This function is used when a model doesn't support thinking but the request
// contains thinking configuration. The configuration is silently removed to
// prevent upstream API errors.
//
// Parameters:
//   - body: Original request body JSON
//   - provider: Provider name (determines which fields to strip)
//
// Returns:
//   - Modified request body JSON with thinking configuration removed
//   - Original body is returned unchanged if:
//   - body is empty or invalid JSON
//   - provider is unknown
//   - no thinking configuration found
func StripThinkingConfig(body []byte, provider string) []byte {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return body
	}

	var paths []string
	switch provider {
	case "claude":
		paths = []string{"thinking", "output_config.effort"}
	case "gemini":
		paths = []string{"generationConfig.thinkingConfig"}
	case "antigravity":
		paths = []string{"request.generationConfig.thinkingConfig"}
	case "interactions":
		paths = []string{
			"generation_config.thinking_level",
			"generation_config.thinkingLevel",
			"generation_config.thinking_budget",
			"generation_config.thinkingBudget",
			"generation_config.thinking_summaries",
			"generation_config.thinkingSummaries",
			"generation_config.thinking_config",
			"generation_config.thinkingConfig",
		}
	case "openai":
		paths = []string{"reasoning_effort"}
	case "kimi":
		paths = []string{
			"reasoning_effort",
			"thinking",
		}
	case "codex", "xai":
		paths = []string{"reasoning.effort"}
	default:
		return body
	}

	result := body
	for _, path := range paths {
		result, _ = sjson.DeleteBytes(result, path)
	}

	// Avoid leaving an empty output_config object for Claude when effort was the only field.
	if provider == "claude" {
		if oc := gjson.GetBytes(result, "output_config"); oc.Exists() && oc.IsObject() && len(oc.Map()) == 0 {
			result, _ = sjson.DeleteBytes(result, "output_config")
		}
	}
	return result
}
