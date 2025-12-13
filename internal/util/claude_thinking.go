package util

import (
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ApplyClaudeThinkingConfig applies thinking configuration to a Claude API request payload.
// It sets the thinking.type to "enabled" and thinking.budget_tokens to the specified budget.
// If budget is nil or the payload already has thinking config, it returns the payload unchanged.
func ApplyClaudeThinkingConfig(body []byte, budget *int) []byte {
	if budget == nil {
		return body
	}
	if gjson.GetBytes(body, "thinking").Exists() {
		return body
	}
	if *budget <= 0 {
		return body
	}
	updated := body
	updated, _ = sjson.SetBytes(updated, "thinking.type", "enabled")
	updated, _ = sjson.SetBytes(updated, "thinking.budget_tokens", *budget)
	return updated
}

// ResolveClaudeThinkingConfig resolves thinking configuration from metadata for Claude models.
// It uses the unified ResolveThinkingConfigFromMetadata and normalizes the budget.
// Returns the normalized budget (nil if thinking should not be enabled) and whether it matched.
func ResolveClaudeThinkingConfig(modelName string, metadata map[string]any) (*int, bool) {
	if !ModelSupportsThinking(modelName) {
		return nil, false
	}
	budget, include, matched := ResolveThinkingConfigFromMetadata(modelName, metadata)
	if !matched {
		return nil, false
	}
	if include != nil && !*include {
		return nil, true
	}
	if budget == nil {
		return nil, true
	}
	normalized := NormalizeThinkingBudget(modelName, *budget)
	if normalized <= 0 {
		return nil, true
	}
	return &normalized, true
}
