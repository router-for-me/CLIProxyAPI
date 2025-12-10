package util

import (
	"encoding/json"
	"strconv"
	"strings"
)

const (
	ThinkingBudgetMetadataKey          = "thinking_budget"
	ThinkingIncludeThoughtsMetadataKey = "thinking_include_thoughts"
	ReasoningEffortMetadataKey         = "reasoning_effort"
	ThinkingOriginalModelMetadataKey   = "thinking_original_model"
)

// NormalizeThinkingModel parses dynamic thinking suffixes on model names and returns
// the normalized base model with extracted metadata. Supported patterns:
//   - "-thinking-<number>" extracts a numeric budget
//   - "-thinking-<level>" extracts a reasoning effort level (minimal/low/medium/high/xhigh/auto/none)
//   - "-thinking" maps to a default reasoning effort of "medium"
//   - "-reasoning" maps to dynamic budget (-1) and include_thoughts=true
//   - "-nothinking" maps to budget=0 and include_thoughts=false
func NormalizeThinkingModel(modelName string) (string, map[string]any) {
	if modelName == "" {
		return modelName, nil
	}

	lower := strings.ToLower(modelName)
	baseModel := modelName

	var (
		budgetOverride  *int
		includeThoughts *bool
		reasoningEffort *string
		matched         bool
	)

	switch {
	case strings.HasSuffix(lower, "-nothinking"):
		baseModel = modelName[:len(modelName)-len("-nothinking")]
		budget := 0
		include := false
		budgetOverride = &budget
		includeThoughts = &include
		matched = true
	case strings.HasSuffix(lower, "-reasoning"):
		baseModel = modelName[:len(modelName)-len("-reasoning")]
		budget := -1
		include := true
		budgetOverride = &budget
		includeThoughts = &include
		matched = true
	default:
		if idx := strings.LastIndex(lower, "-thinking-"); idx != -1 {
			value := modelName[idx+len("-thinking-"):]
			if value != "" {
				if parsed, ok := parseIntPrefix(value); ok {
					baseModel = modelName[:idx]
					budgetOverride = &parsed
					matched = true
				} else if effort, okEffort := normalizeReasoningEffort(value); okEffort {
					baseModel = modelName[:idx]
					reasoningEffort = &effort
					matched = true
				}
			}
		} else if strings.HasSuffix(lower, "-thinking") {
			baseModel = modelName[:len(modelName)-len("-thinking")]
			effort := "medium"
			reasoningEffort = &effort
			matched = true
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
	if includeThoughts != nil {
		metadata[ThinkingIncludeThoughtsMetadataKey] = *includeThoughts
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
		return *effort, true
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

// ThinkingEffortToBudget maps reasoning effort levels to approximate budgets,
// clamping the result to the model's supported range.
func ThinkingEffortToBudget(model, effort string) (int, bool) {
	if effort == "" {
		return 0, false
	}
	switch strings.ToLower(effort) {
	case "none":
		return 0, true
	case "auto":
		return NormalizeThinkingBudget(model, -1), true
	case "minimal":
		return NormalizeThinkingBudget(model, 512), true
	case "low":
		return NormalizeThinkingBudget(model, 1024), true
	case "medium":
		return NormalizeThinkingBudget(model, 8192), true
	case "high":
		return NormalizeThinkingBudget(model, 24576), true
	case "xhigh":
		return NormalizeThinkingBudget(model, 32768), true
	default:
		return 0, false
	}
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

func normalizeReasoningEffort(value string) (string, bool) {
	if value == "" {
		return "", false
	}
	effort := strings.ToLower(strings.TrimSpace(value))
	switch effort {
	case "minimal", "low", "medium", "high", "xhigh", "auto", "none":
		return effort, true
	default:
		return "", false
	}
}
