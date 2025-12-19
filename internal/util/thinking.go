package util

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
)

// ModelSupportsThinking reports whether the given model has Thinking capability
// according to the model registry metadata (provider-agnostic).
func ModelSupportsThinking(model string) bool {
	if model == "" {
		return false
	}
	if info := registry.GetGlobalRegistry().GetModelInfo(model); info != nil {
		return info.Thinking != nil
	}
	return false
}

// NormalizeThinkingBudget clamps the requested thinking budget to the
// supported range for the specified model using registry metadata only.
// If the model is unknown or has no Thinking metadata, returns the original budget.
// For dynamic (-1), returns -1 if DynamicAllowed; otherwise approximates mid-range
// or min (0 if zero is allowed and mid <= 0).
func NormalizeThinkingBudget(model string, budget int) int {
	if budget == -1 { // dynamic
		if found, minBudget, maxBudget, zeroAllowed, dynamicAllowed := thinkingRangeFromRegistry(model); found {
			if dynamicAllowed {
				return -1
			}
			mid := (minBudget + maxBudget) / 2
			if mid <= 0 && zeroAllowed {
				return 0
			}
			if mid <= 0 {
				return minBudget
			}
			return mid
		}
		return -1
	}
	if found, minBudget, maxBudget, zeroAllowed, _ := thinkingRangeFromRegistry(model); found {
		if budget == 0 {
			if zeroAllowed {
				return 0
			}
			return minBudget
		}
		if budget < minBudget {
			return minBudget
		}
		if budget > maxBudget {
			return maxBudget
		}
		return budget
	}
	return budget
}

// thinkingRangeFromRegistry attempts to read thinking ranges from the model registry.
func thinkingRangeFromRegistry(model string) (found bool, min int, max int, zeroAllowed bool, dynamicAllowed bool) {
	if model == "" {
		return false, 0, 0, false, false
	}
	info := registry.GetGlobalRegistry().GetModelInfo(model)
	if info == nil || info.Thinking == nil {
		return false, 0, 0, false, false
	}
	return true, info.Thinking.Min, info.Thinking.Max, info.Thinking.ZeroAllowed, info.Thinking.DynamicAllowed
}

// GetModelThinkingLevels returns the discrete reasoning effort levels for the model.
// Returns nil if the model has no thinking support or no levels defined.
func GetModelThinkingLevels(model string) []string {
	if model == "" {
		return nil
	}
	info := registry.GetGlobalRegistry().GetModelInfo(model)
	if info == nil || info.Thinking == nil {
		return nil
	}
	return info.Thinking.Levels
}

// ModelUsesThinkingLevels reports whether the model uses discrete reasoning
// effort levels instead of numeric budgets.
func ModelUsesThinkingLevels(model string) bool {
	levels := GetModelThinkingLevels(model)
	return len(levels) > 0
}

// NormalizeReasoningEffortLevel validates and normalizes a reasoning effort
// level for the given model. Returns false when the level is not supported.
func NormalizeReasoningEffortLevel(model, effort string) (string, bool) {
	levels := GetModelThinkingLevels(model)
	if len(levels) == 0 {
		return "", false
	}
	loweredEffort := strings.ToLower(strings.TrimSpace(effort))
	for _, lvl := range levels {
		if strings.ToLower(lvl) == loweredEffort {
			return lvl, true
		}
	}
	return "", false
}

// IsOpenAICompatibilityModel reports whether the model is registered as an OpenAI-compatibility model.
// These models may not advertise Thinking metadata in the registry.
func IsOpenAICompatibilityModel(model string) bool {
	if model == "" {
		return false
	}
	info := registry.GetGlobalRegistry().GetModelInfo(model)
	if info == nil {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(info.Type), "openai-compatibility")
}

// ThinkingEffortToBudget maps a reasoning effort level to a numeric thinking budget (tokens),
// clamping the result to the model's supported range.
//
// Mappings (values are normalized to model's supported range):
//   - "none"    -> 0
//   - "auto"    -> -1
//   - "minimal" -> 512
//   - "low"     -> 1024
//   - "medium"  -> 8192
//   - "high"    -> 24576
//   - "xhigh"   -> 32768
//
// Returns false when the effort level is empty or unsupported.
func ThinkingEffortToBudget(model, effort string) (int, bool) {
	if effort == "" {
		return 0, false
	}
	normalized, ok := NormalizeReasoningEffortLevel(model, effort)
	if !ok {
		normalized = strings.ToLower(strings.TrimSpace(effort))
	}
	switch normalized {
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

// ThinkingLevelToBudget maps a Gemini thinkingLevel to a numeric thinking budget (tokens).
//
// Mappings:
//   - "minimal" -> 512
//   - "low"     -> 1024
//   - "medium"  -> 8192
//   - "high"    -> 32768
//
// Returns false when the level is empty or unsupported.
func ThinkingLevelToBudget(level string) (int, bool) {
	if level == "" {
		return 0, false
	}
	normalized := strings.ToLower(strings.TrimSpace(level))
	switch normalized {
	case "minimal":
		return 512, true
	case "low":
		return 1024, true
	case "medium":
		return 8192, true
	case "high":
		return 32768, true
	default:
		return 0, false
	}
}

// ThinkingBudgetToEffort maps a numeric thinking budget (tokens)
// to a reasoning effort level for level-based models.
//
// Mappings:
//   - 0            -> "none" (or lowest supported level if model doesn't support "none")
//   - -1           -> "auto"
//   - 1..1024      -> "low"
//   - 1025..8192   -> "medium"
//   - 8193..24576  -> "high"
//   - 24577..      -> highest supported level for the model (defaults to "xhigh")
//
// Returns false when the budget is unsupported (negative values other than -1).
func ThinkingBudgetToEffort(model string, budget int) (string, bool) {
	switch {
	case budget == -1:
		return "auto", true
	case budget < -1:
		return "", false
	case budget == 0:
		if levels := GetModelThinkingLevels(model); len(levels) > 0 {
			return levels[0], true
		}
		return "none", true
	case budget > 0 && budget <= 1024:
		return "low", true
	case budget <= 8192:
		return "medium", true
	case budget <= 24576:
		return "high", true
	case budget > 24576:
		if levels := GetModelThinkingLevels(model); len(levels) > 0 {
			return levels[len(levels)-1], true
		}
		return "xhigh", true
	default:
		return "", false
	}
}
