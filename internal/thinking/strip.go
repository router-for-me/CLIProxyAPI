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
		paths = []string{"thinking"}
	case "gemini":
		paths = []string{"generationConfig.thinkingConfig"}
	case "gemini-cli", "antigravity":
		paths = []string{"request.generationConfig.thinkingConfig"}
	case "openai":
		paths = []string{"reasoning_effort"}
	case "codex":
		paths = []string{"reasoning.effort"}
	case "iflow":
		paths = []string{
			"chat_template_kwargs.enable_thinking",
			"chat_template_kwargs.clear_thinking",
			"reasoning_split",
			"reasoning_effort",
		}
	default:
		return body
	}

	result := body
	for _, path := range paths {
		result, _ = sjson.DeleteBytes(result, path)
	}
	return result
}
