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
		paths = []string{"reasoning_effort", "reasoning"}
	case "kimi":
		paths = []string{
			"reasoning_effort",
			"thinking",
		}
	case "codex", "xai":
		paths = []string{"reasoning"}
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

// ReconcileClaudePayloadEffort adjusts Claude thinking configuration when a
// payload rule has injected output_config.effort after the thinking applier
// already ran.
//
// The thinking applier runs before payload rules in the executor pipeline, so
// when a payload rule writes output_config.effort the thinking configuration
// may already be in budget_tokens form. This function detects that mismatch
// and converts the thinking block to adaptive mode, letting the discrete
// effort level reach the upstream.
//
// See: https://github.com/router-for-me/CLIProxyAPI/issues/4796
func ReconcileClaudePayloadEffort(body []byte) []byte {
	effort := gjson.GetBytes(body, "output_config.effort")
	if !effort.Exists() || effort.Type != gjson.String || effort.String() == "" {
		return body
	}
	thinkingType := gjson.GetBytes(body, "thinking.type").String()
	if thinkingType == "adaptive" {
		// Already in adaptive mode; nothing to reconcile.
		return body
	}
	// Convert enabled/other → adaptive and drop the numeric budget so the
	// upstream uses the discrete effort level instead.
	body, _ = sjson.SetBytes(body, "thinking.type", "adaptive")
	body, _ = sjson.DeleteBytes(body, "thinking.budget_tokens")
	return body
}
