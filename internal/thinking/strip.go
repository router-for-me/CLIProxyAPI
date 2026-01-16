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

	switch provider {
	case "claude":
		result, _ := sjson.DeleteBytes(body, "thinking")
		return result
	case "gemini":
		result, _ := sjson.DeleteBytes(body, "generationConfig.thinkingConfig")
		return result
	case "gemini-cli", "antigravity":
		result, _ := sjson.DeleteBytes(body, "request.generationConfig.thinkingConfig")
		return result
	case "openai":
		result, _ := sjson.DeleteBytes(body, "reasoning_effort")
		return result
	case "codex":
		result, _ := sjson.DeleteBytes(body, "reasoning.effort")
		return result
	case "iflow":
		result, _ := sjson.DeleteBytes(body, "chat_template_kwargs.enable_thinking")
		result, _ = sjson.DeleteBytes(result, "chat_template_kwargs.clear_thinking")
		result, _ = sjson.DeleteBytes(result, "reasoning_split")
		return result
	default:
		return body
	}
}
