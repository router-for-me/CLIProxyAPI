package util

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const (
	GeminiThinkingBudgetMetadataKey  = "gemini_thinking_budget"
	GeminiIncludeThoughtsMetadataKey = "gemini_include_thoughts"
	GeminiOriginalModelMetadataKey   = "gemini_original_model"
)

func ParseGeminiThinkingSuffix(model string) (string, *int, *bool, bool) {
	if model == "" {
		return model, nil, nil, false
	}
	lower := strings.ToLower(model)
	if !strings.HasPrefix(lower, "gemini-") {
		return model, nil, nil, false
	}

	if strings.HasSuffix(lower, "-nothinking") {
		base := model[:len(model)-len("-nothinking")]
		budgetValue := 0
		if strings.HasPrefix(lower, "gemini-2.5-pro") {
			budgetValue = 128
		}
		include := false
		return base, &budgetValue, &include, true
	}

	// Handle "-reasoning" suffix: enables thinking with dynamic budget (-1)
	// Maps: gemini-2.5-flash-reasoning -> gemini-2.5-flash with thinkingBudget=-1
	if strings.HasSuffix(lower, "-reasoning") {
		base := model[:len(model)-len("-reasoning")]
		budgetValue := -1 // Dynamic budget
		include := true
		return base, &budgetValue, &include, true
	}

	idx := strings.LastIndex(lower, "-thinking-")
	if idx == -1 {
		return model, nil, nil, false
	}

	digits := model[idx+len("-thinking-"):]
	if digits == "" {
		return model, nil, nil, false
	}
	end := len(digits)
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			end = i
			break
		}
	}
	if end == 0 {
		return model, nil, nil, false
	}
	valueStr := digits[:end]
	value, err := strconv.Atoi(valueStr)
	if err != nil {
		return model, nil, nil, false
	}
	base := model[:idx]
	budgetValue := value
	return base, &budgetValue, nil, true
}

func NormalizeGeminiThinkingModel(modelName string) (string, map[string]any) {
	// First try Gemini 3 reasoning effort normalization (-low/-high suffix)
	workingModel := modelName
	var metadata map[string]any

	if base, effort, matched := ParseGemini3ReasoningEffortSuffix(modelName); matched {
		workingModel = base
		metadata = map[string]any{
			Gemini3OriginalModelMetadataKey:   modelName,
			Gemini3ReasoningEffortMetadataKey: effort,
		}
	}

	// Then try thinking suffix normalization on the (possibly already normalized) model
	baseModel, budget, include, matched := ParseGeminiThinkingSuffix(workingModel)
	if !matched {
		return workingModel, metadata
	}

	// Merge thinking metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata[GeminiOriginalModelMetadataKey] = modelName
	if budget != nil {
		metadata[GeminiThinkingBudgetMetadataKey] = *budget
	}
	if include != nil {
		metadata[GeminiIncludeThoughtsMetadataKey] = *include
	}
	return baseModel, metadata
}

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

func GeminiThinkingFromMetadata(metadata map[string]any) (*int, *bool, bool) {
	if len(metadata) == 0 {
		return nil, nil, false
	}
	var (
		budgetPtr  *int
		includePtr *bool
		matched    bool
	)
	if rawBudget, ok := metadata[GeminiThinkingBudgetMetadataKey]; ok {
		switch v := rawBudget.(type) {
		case int:
			budget := v
			budgetPtr = &budget
			matched = true
		case int32:
			budget := int(v)
			budgetPtr = &budget
			matched = true
		case int64:
			budget := int(v)
			budgetPtr = &budget
			matched = true
		case float64:
			budget := int(v)
			budgetPtr = &budget
			matched = true
		case json.Number:
			if val, err := v.Int64(); err == nil {
				budget := int(val)
				budgetPtr = &budget
				matched = true
			}
		}
	}
	if rawInclude, ok := metadata[GeminiIncludeThoughtsMetadataKey]; ok {
		switch v := rawInclude.(type) {
		case bool:
			include := v
			includePtr = &include
			matched = true
		case string:
			if parsed, err := strconv.ParseBool(v); err == nil {
				include := parsed
				includePtr = &include
				matched = true
			}
		case json.Number:
			if val, err := v.Int64(); err == nil {
				include := val != 0
				includePtr = &include
				matched = true
			}
		case int:
			include := v != 0
			includePtr = &include
			matched = true
		case int32:
			include := v != 0
			includePtr = &include
			matched = true
		case int64:
			include := v != 0
			includePtr = &include
			matched = true
		case float64:
			include := v != 0
			includePtr = &include
			matched = true
		}
	}
	return budgetPtr, includePtr, matched
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

const (
	Gemini3ReasoningEffortMetadataKey = "gemini3_reasoning_effort"
	Gemini3OriginalModelMetadataKey   = "gemini3_original_model"
)

// ParseGemini3ReasoningEffortSuffix parses -low or -high suffix from Gemini 3 model names.
// Returns the base model, reasoning effort ("low" or "high"), and whether a match was found.
func ParseGemini3ReasoningEffortSuffix(model string) (baseModel string, reasoningEffort string, matched bool) {
	if model == "" {
		return model, "", false
	}
	lower := strings.ToLower(model)
	if !strings.HasPrefix(lower, "gemini-3-") {
		return model, "", false
	}

	if strings.HasSuffix(lower, "-low") {
		return model[:len(model)-len("-low")], "low", true
	}
	if strings.HasSuffix(lower, "-high") {
		return model[:len(model)-len("-high")], "high", true
	}
	return model, "", false
}

// NormalizeGemini3ReasoningEffortModel normalizes a Gemini 3 model with -low/-high suffix.
// Returns the base model name and metadata containing the reasoning effort.
func NormalizeGemini3ReasoningEffortModel(modelName string) (string, map[string]any) {
	baseModel, effort, matched := ParseGemini3ReasoningEffortSuffix(modelName)
	if !matched {
		return modelName, nil
	}
	metadata := map[string]any{
		Gemini3OriginalModelMetadataKey:   modelName,
		Gemini3ReasoningEffortMetadataKey: effort,
	}
	return baseModel, metadata
}

// Gemini3ReasoningEffortFromMetadata extracts reasoning effort from metadata.
func Gemini3ReasoningEffortFromMetadata(metadata map[string]any) (string, bool) {
	if len(metadata) == 0 {
		return "", false
	}
	if effort, ok := metadata[Gemini3ReasoningEffortMetadataKey]; ok {
		if s, ok := effort.(string); ok && s != "" {
			return s, true
		}
	}
	return "", false
}
