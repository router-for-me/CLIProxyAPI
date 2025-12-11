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
		if found, min, max, zeroAllowed, dynamicAllowed := thinkingRangeFromRegistry(model); found {
			if dynamicAllowed {
				return -1
			}
			mid := (min + max) / 2
			if mid <= 0 && zeroAllowed {
				return 0
			}
			if mid <= 0 {
				return min
			}
			return mid
		}
		return -1
	}
	if found, min, max, zeroAllowed, _ := thinkingRangeFromRegistry(model); found {
		if budget == 0 {
			if zeroAllowed {
				return 0
			}
			return min
		}
		if budget < min {
			return min
		}
		if budget > max {
			return max
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
