package util

import (
	"encoding/json"
	"strconv"
	"strings"
)

const (
	ThinkingBudgetMetadataKey            = "thinking_budget"
	ThinkingIncludeThoughtsMetadataKey   = "thinking_include_thoughts"
	ReasoningEffortMetadataKey           = "reasoning_effort"
	ThinkingOriginalModelMetadataKey     = "thinking_original_model"
	ModelMappingOriginalModelMetadataKey = "model_mapping_original_model"
)

// NormalizeThinkingModel parses dynamic thinking suffixes on model names and returns
// the normalized base model with extracted metadata. Supported pattern:
//   - "(<value>)" where value can be:
//   - A numeric budget (e.g., "(8192)", "(16384)")
//   - A reasoning effort level (e.g., "(high)", "(medium)", "(low)")
//
// Examples:
//   - "claude-sonnet-4-5-20250929(16384)" → budget=16384
//   - "gpt-5.1(high)" → reasoning_effort="high"
//   - "gemini-2.5-pro(32768)" → budget=32768
//
// Note: Empty parentheses "()" are not supported and will be ignored.
func NormalizeThinkingModel(modelName string) (string, map[string]any) {
	if modelName == "" {
		return modelName, nil
	}

	baseModel := modelName

	var (
		budgetOverride  *int
		reasoningEffort *string
		matched         bool
	)

	// Match "(<value>)" pattern at the end of the model name
	if idx := strings.LastIndex(modelName, "("); idx != -1 {
		if !strings.HasSuffix(modelName, ")") {
			// Incomplete parenthesis, ignore
			return baseModel, nil
		}

		value := modelName[idx+1 : len(modelName)-1] // Extract content between ( and )
		if value == "" {
			// Empty parentheses not supported
			return baseModel, nil
		}

		candidateBase := modelName[:idx]

		// Auto-detect: pure numeric → budget, string → reasoning effort level
		if parsed, ok := parseIntPrefix(value); ok {
			// Numeric value: treat as thinking budget
			baseModel = candidateBase
			budgetOverride = &parsed
			matched = true
		} else {
			// String value: treat as reasoning effort level
			baseModel = candidateBase
			raw := strings.ToLower(strings.TrimSpace(value))
			if raw != "" {
				reasoningEffort = &raw
				matched = true
			}
		}
	}

	if !matched {
		return baseModel, nil
	}

	metadata := map[string]any{
		ThinkingOriginalModelMetadataKey: modelName,
	}
	if budgetOverride != nil {
		metadata[ThinkingBudgetMetadataKey] = *budgetOverride
	}
	if reasoningEffort != nil {
		metadata[ReasoningEffortMetadataKey] = *reasoningEffort
	}
	return baseModel, metadata
}

// ThinkingFromMetadata extracts thinking overrides from metadata produced by NormalizeThinkingModel.
// It accepts both the new generic keys and legacy Gemini-specific keys.
func ThinkingFromMetadata(metadata map[string]any) (*int, *bool, *string, bool) {
	if len(metadata) == 0 {
		return nil, nil, nil, false
	}

	var (
		budgetPtr  *int
		includePtr *bool
		effortPtr  *string
		matched    bool
	)

	readBudget := func(key string) {
		if budgetPtr != nil {
			return
		}
		if raw, ok := metadata[key]; ok {
			if v, okNumber := parseNumberToInt(raw); okNumber {
				budget := v
				budgetPtr = &budget
				matched = true
			}
		}
	}

	readInclude := func(key string) {
		if includePtr != nil {
			return
		}
		if raw, ok := metadata[key]; ok {
			switch v := raw.(type) {
			case bool:
				val := v
				includePtr = &val
				matched = true
			case *bool:
				if v != nil {
					val := *v
					includePtr = &val
					matched = true
				}
			}
		}
	}

	readEffort := func(key string) {
		if effortPtr != nil {
			return
		}
		if raw, ok := metadata[key]; ok {
			if val, okStr := raw.(string); okStr && strings.TrimSpace(val) != "" {
				normalized := strings.ToLower(strings.TrimSpace(val))
				effortPtr = &normalized
				matched = true
			}
		}
	}

	readBudget(ThinkingBudgetMetadataKey)
	readBudget(GeminiThinkingBudgetMetadataKey)
	readInclude(ThinkingIncludeThoughtsMetadataKey)
	readInclude(GeminiIncludeThoughtsMetadataKey)
	readEffort(ReasoningEffortMetadataKey)
	readEffort("reasoning.effort")

	return budgetPtr, includePtr, effortPtr, matched
}

// ResolveThinkingConfigFromMetadata derives thinking budget/include overrides,
// converting reasoning effort strings into budgets when possible.
func ResolveThinkingConfigFromMetadata(model string, metadata map[string]any) (*int, *bool, bool) {
	budget, include, effort, matched := ThinkingFromMetadata(metadata)
	if !matched {
		return nil, nil, false
	}
	// Level-based models (OpenAI-style) do not accept numeric thinking budgets in
	// Claude/Gemini-style protocols, so we don't derive budgets for them here.
	if ModelUsesThinkingLevels(model) {
		return nil, nil, false
	}

	if budget == nil && effort != nil {
		if derived, ok := ThinkingEffortToBudget(model, *effort); ok {
			budget = &derived
		}
	}
	return budget, include, budget != nil || include != nil || effort != nil
}

// ReasoningEffortFromMetadata resolves a reasoning effort string from metadata,
// inferring "auto" and "none" when budgets request dynamic or disabled thinking.
func ReasoningEffortFromMetadata(metadata map[string]any) (string, bool) {
	budget, include, effort, matched := ThinkingFromMetadata(metadata)
	if !matched {
		return "", false
	}
	if effort != nil && *effort != "" {
		return strings.ToLower(strings.TrimSpace(*effort)), true
	}
	if budget != nil {
		switch *budget {
		case -1:
			return "auto", true
		case 0:
			return "none", true
		}
	}
	if include != nil && !*include {
		return "none", true
	}
	return "", true
}

// ResolveOriginalModel returns the original model name stored in metadata (if present),
// otherwise falls back to the provided model.
func ResolveOriginalModel(model string, metadata map[string]any) string {
	normalize := func(name string) string {
		if name == "" {
			return ""
		}
		if base, _ := NormalizeThinkingModel(name); base != "" {
			return base
		}
		return strings.TrimSpace(name)
	}

	if metadata != nil {
		if v, ok := metadata[ModelMappingOriginalModelMetadataKey]; ok {
			if s, okStr := v.(string); okStr && strings.TrimSpace(s) != "" {
				if base := normalize(s); base != "" {
					return base
				}
			}
		}
		if v, ok := metadata[ThinkingOriginalModelMetadataKey]; ok {
			if s, okStr := v.(string); okStr && strings.TrimSpace(s) != "" {
				if base := normalize(s); base != "" {
					return base
				}
			}
		}
		if v, ok := metadata[GeminiOriginalModelMetadataKey]; ok {
			if s, okStr := v.(string); okStr && strings.TrimSpace(s) != "" {
				if base := normalize(s); base != "" {
					return base
				}
			}
		}
	}
	// Fallback: try to re-normalize the model name when metadata was dropped.
	if base := normalize(model); base != "" {
		return base
	}
	return model
}

func parseIntPrefix(value string) (int, bool) {
	if value == "" {
		return 0, false
	}
	digits := strings.TrimLeft(value, "-")
	if digits == "" {
		return 0, false
	}
	end := len(digits)
	for i := 0; i < len(digits); i++ {
		if digits[i] < '0' || digits[i] > '9' {
			end = i
			break
		}
	}
	if end == 0 {
		return 0, false
	}
	val, err := strconv.Atoi(digits[:end])
	if err != nil {
		return 0, false
	}
	return val, true
}

func parseNumberToInt(raw any) (int, bool) {
	switch v := raw.(type) {
	case int:
		return v, true
	case int32:
		return int(v), true
	case int64:
		return int(v), true
	case float64:
		return int(v), true
	case json.Number:
		if val, err := v.Int64(); err == nil {
			return int(val), true
		}
	case string:
		if strings.TrimSpace(v) == "" {
			return 0, false
		}
		if parsed, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return parsed, true
		}
	}
	return 0, false
}
