package util

import (
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	GeminiThinkingBudgetMetadataKey  = "gemini_thinking_budget"
	GeminiIncludeThoughtsMetadataKey = "gemini_include_thoughts"
	GeminiOriginalModelMetadataKey   = "gemini_original_model"
)

func ApplyGeminiThinkingConfig(body []byte, budget *int, includeThoughts *bool) []byte {
	if budget == nil && includeThoughts == nil {
		return body
	}
	updated := body
	if budget != nil {
		valuePath := "generationConfig.thinkingConfig.thinkingBudget"
		rewritten, err := sjson.SetBytes(updated, valuePath, *budget)
		if err == nil {
			updated = rewritten
		}
	}
	if includeThoughts != nil {
		valuePath := "generationConfig.thinkingConfig.include_thoughts"
		rewritten, err := sjson.SetBytes(updated, valuePath, *includeThoughts)
		if err == nil {
			updated = rewritten
		}
	}
	return updated
}

func ApplyGeminiCLIThinkingConfig(body []byte, budget *int, includeThoughts *bool) []byte {
	if budget == nil && includeThoughts == nil {
		return body
	}
	updated := body
	if budget != nil {
		valuePath := "request.generationConfig.thinkingConfig.thinkingBudget"
		rewritten, err := sjson.SetBytes(updated, valuePath, *budget)
		if err == nil {
			updated = rewritten
		}
	}
	if includeThoughts != nil {
		valuePath := "request.generationConfig.thinkingConfig.include_thoughts"
		rewritten, err := sjson.SetBytes(updated, valuePath, *includeThoughts)
		if err == nil {
			updated = rewritten
		}
	}
	return updated
}

// modelsWithDefaultThinking lists models that should have thinking enabled by default
// when no explicit thinkingConfig is provided.
var modelsWithDefaultThinking = map[string]bool{
	"gemini-3-pro-preview": true,
}

// ModelHasDefaultThinking returns true if the model should have thinking enabled by default.
func ModelHasDefaultThinking(model string) bool {
	return modelsWithDefaultThinking[model]
}

// ApplyDefaultThinkingIfNeeded injects default thinkingConfig for models that require it.
// For standard Gemini API format (generationConfig.thinkingConfig path).
// Returns the modified body if thinkingConfig was added, otherwise returns the original.
func ApplyDefaultThinkingIfNeeded(model string, body []byte) []byte {
	if !ModelHasDefaultThinking(model) {
		return body
	}
	if gjson.GetBytes(body, "generationConfig.thinkingConfig").Exists() {
		return body
	}
	updated, _ := sjson.SetBytes(body, "generationConfig.thinkingConfig.thinkingBudget", -1)
	updated, _ = sjson.SetBytes(updated, "generationConfig.thinkingConfig.include_thoughts", true)
	return updated
}

// ApplyDefaultThinkingIfNeededCLI injects default thinkingConfig for models that require it.
// For Gemini CLI API format (request.generationConfig.thinkingConfig path).
// Returns the modified body if thinkingConfig was added, otherwise returns the original.
func ApplyDefaultThinkingIfNeededCLI(model string, body []byte) []byte {
	if !ModelHasDefaultThinking(model) {
		return body
	}
	if gjson.GetBytes(body, "request.generationConfig.thinkingConfig").Exists() {
		return body
	}
	updated, _ := sjson.SetBytes(body, "request.generationConfig.thinkingConfig.thinkingBudget", -1)
	updated, _ = sjson.SetBytes(updated, "request.generationConfig.thinkingConfig.include_thoughts", true)
	return updated
}

// StripThinkingConfigIfUnsupported removes thinkingConfig from the request body
// when the target model does not advertise Thinking capability. It cleans both
// standard Gemini and Gemini CLI JSON envelopes. This acts as a final safety net
// in case upstream injected thinking for an unsupported model.
func StripThinkingConfigIfUnsupported(model string, body []byte) []byte {
	if ModelSupportsThinking(model) || len(body) == 0 {
		return body
	}
	updated := body
	// Gemini CLI path
	updated, _ = sjson.DeleteBytes(updated, "request.generationConfig.thinkingConfig")
	// Standard Gemini path
	updated, _ = sjson.DeleteBytes(updated, "generationConfig.thinkingConfig")
	return updated
}

// NormalizeGeminiThinkingBudget normalizes the thinkingBudget value in a standard Gemini
// request body (generationConfig.thinkingConfig.thinkingBudget path).
func NormalizeGeminiThinkingBudget(model string, body []byte) []byte {
	const budgetPath = "generationConfig.thinkingConfig.thinkingBudget"
	budget := gjson.GetBytes(body, budgetPath)
	if !budget.Exists() {
		return body
	}
	normalized := NormalizeThinkingBudget(model, int(budget.Int()))
	updated, _ := sjson.SetBytes(body, budgetPath, normalized)
	return updated
}

// NormalizeGeminiCLIThinkingBudget normalizes the thinkingBudget value in a Gemini CLI
// request body (request.generationConfig.thinkingConfig.thinkingBudget path).
func NormalizeGeminiCLIThinkingBudget(model string, body []byte) []byte {
	const budgetPath = "request.generationConfig.thinkingConfig.thinkingBudget"
	budget := gjson.GetBytes(body, budgetPath)
	if !budget.Exists() {
		return body
	}
	normalized := NormalizeThinkingBudget(model, int(budget.Int()))
	updated, _ := sjson.SetBytes(body, budgetPath, normalized)
	return updated
}

// ConvertThinkingLevelToBudget checks for "generationConfig.thinkingConfig.thinkingLevel"
// and converts it to "thinkingBudget".
// "high" -> 32768
// "low" -> 128
// It removes "thinkingLevel" after conversion.
func ConvertThinkingLevelToBudget(body []byte) []byte {
	levelPath := "generationConfig.thinkingConfig.thinkingLevel"
	res := gjson.GetBytes(body, levelPath)
	if !res.Exists() {
		return body
	}

	level := strings.ToLower(res.String())
	var budget int
	switch level {
	case "high":
		budget = 32768
	case "low":
		budget = 128
	default:
		// If unknown level, we might just leave it or default.
		// User only specified high and low. We'll assume we shouldn't touch it if it's something else,
		// or maybe we should just remove the invalid level?
		// For safety adhering to strict instructions: "If high... if low...".
		// If it's something else, the upstream might fail anyway if we leave it,
		// but let's just delete the level if we processed it.
		// Actually, let's check if we need to do anything for other values.
		// For now, only handle high/low.
		return body
	}

	// Set budget
	budgetPath := "generationConfig.thinkingConfig.thinkingBudget"
	updated, err := sjson.SetBytes(body, budgetPath, budget)
	if err != nil {
		return body
	}

	// Remove level
	updated, err = sjson.DeleteBytes(updated, levelPath)
	if err != nil {
		return body
	}
	return updated
}
