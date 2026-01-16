// Package thinking provides unified thinking configuration processing logic.
package thinking

import (
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	log "github.com/sirupsen/logrus"
)

// ClampBudget clamps a budget value to the model's supported range.
//
// Logging:
//   - Warn when value=0 but ZeroAllowed=false
//   - Debug when value is clamped to min/max
//
// Fields: provider, model, original_value, clamped_to, min, max
func ClampBudget(value int, modelInfo *registry.ModelInfo, provider string) int {
	model := "unknown"
	support := (*registry.ThinkingSupport)(nil)
	if modelInfo != nil {
		if modelInfo.ID != "" {
			model = modelInfo.ID
		}
		support = modelInfo.Thinking
	}
	if support == nil {
		return value
	}

	// Auto value (-1) passes through without clamping.
	if value == -1 {
		return value
	}

	min := support.Min
	max := support.Max
	if value == 0 && !support.ZeroAllowed {
		log.WithFields(log.Fields{
			"provider":       provider,
			"model":          model,
			"original_value": value,
			"clamped_to":     min,
			"min":            min,
			"max":            max,
		}).Warn("thinking: budget zero not allowed |")
		return min
	}

	// Some models are level-only and do not define numeric budget ranges.
	if min == 0 && max == 0 {
		return value
	}

	if value < min {
		if value == 0 && support.ZeroAllowed {
			return 0
		}
		logClamp(provider, model, value, min, min, max)
		return min
	}
	if value > max {
		logClamp(provider, model, value, max, min, max)
		return max
	}
	return value
}

// ValidateConfig validates a thinking configuration against model capabilities.
//
// This function performs comprehensive validation:
//   - Checks if the model supports thinking
//   - Auto-converts between Budget and Level formats based on model capability
//   - Validates that requested level is in the model's supported levels list
//   - Clamps budget values to model's allowed range
//
// Parameters:
//   - config: The thinking configuration to validate
//   - support: Model's ThinkingSupport properties (nil means no thinking support)
//
// Returns:
//   - Normalized ThinkingConfig with clamped values
//   - ThinkingError if validation fails (ErrThinkingNotSupported, ErrLevelNotSupported, etc.)
//
// Auto-conversion behavior:
//   - Budget-only model + Level config → Level converted to Budget
//   - Level-only model + Budget config → Budget converted to Level
//   - Hybrid model → preserve original format
func ValidateConfig(config ThinkingConfig, modelInfo *registry.ModelInfo, provider string) (*ThinkingConfig, error) {
	normalized := config

	model := "unknown"
	support := (*registry.ThinkingSupport)(nil)
	if modelInfo != nil {
		if modelInfo.ID != "" {
			model = modelInfo.ID
		}
		support = modelInfo.Thinking
	}

	if support == nil {
		if config.Mode != ModeNone {
			return nil, NewThinkingErrorWithModel(ErrThinkingNotSupported, "thinking not supported for this model", model)
		}
		return &normalized, nil
	}

	capability := detectModelCapability(modelInfo)
	switch capability {
	case CapabilityBudgetOnly:
		if normalized.Mode == ModeLevel {
			if normalized.Level == LevelAuto {
				break
			}
			budget, ok := ConvertLevelToBudget(string(normalized.Level))
			if !ok {
				return nil, NewThinkingError(ErrUnknownLevel, fmt.Sprintf("unknown level: %s", normalized.Level))
			}
			normalized.Mode = ModeBudget
			normalized.Budget = budget
			normalized.Level = ""
		}
	case CapabilityLevelOnly:
		if normalized.Mode == ModeBudget {
			level, ok := ConvertBudgetToLevel(normalized.Budget)
			if !ok {
				return nil, NewThinkingError(ErrUnknownLevel, fmt.Sprintf("budget %d cannot be converted to a valid level", normalized.Budget))
			}
			normalized.Mode = ModeLevel
			normalized.Level = ThinkingLevel(level)
			normalized.Budget = 0
		}
	case CapabilityHybrid:
	}

	if normalized.Mode == ModeLevel && normalized.Level == LevelNone {
		normalized.Mode = ModeNone
		normalized.Budget = 0
		normalized.Level = ""
	}
	if normalized.Mode == ModeLevel && normalized.Level == LevelAuto {
		normalized.Mode = ModeAuto
		normalized.Budget = -1
		normalized.Level = ""
	}
	if normalized.Mode == ModeBudget && normalized.Budget == 0 {
		normalized.Mode = ModeNone
		normalized.Level = ""
	}

	if len(support.Levels) > 0 && normalized.Mode == ModeLevel {
		if !isLevelSupported(string(normalized.Level), support.Levels) {
			validLevels := normalizeLevels(support.Levels)
			message := fmt.Sprintf("level %q not supported, valid levels: %s", strings.ToLower(string(normalized.Level)), strings.Join(validLevels, ", "))
			return nil, NewThinkingError(ErrLevelNotSupported, message)
		}
	}

	// Convert ModeAuto to mid-range if dynamic not allowed
	if normalized.Mode == ModeAuto && !support.DynamicAllowed {
		normalized = convertAutoToMidRange(normalized, support, provider, model)
	}

	if normalized.Mode == ModeNone && provider == "claude" {
		// Claude supports explicit disable via thinking.type="disabled".
		// Keep Budget=0 so applier can omit budget_tokens.
		normalized.Budget = 0
		normalized.Level = ""
	} else {
		switch normalized.Mode {
		case ModeBudget, ModeAuto, ModeNone:
			normalized.Budget = ClampBudget(normalized.Budget, modelInfo, provider)
		}

		// ModeNone with clamped Budget > 0: set Level to lowest for Level-only/Hybrid models
		// This ensures Apply layer doesn't need to access support.Levels
		if normalized.Mode == ModeNone && normalized.Budget > 0 && len(support.Levels) > 0 {
			normalized.Level = ThinkingLevel(support.Levels[0])
		}
	}

	return &normalized, nil
}

func isLevelSupported(level string, supported []string) bool {
	for _, candidate := range supported {
		if strings.EqualFold(level, strings.TrimSpace(candidate)) {
			return true
		}
	}
	return false
}

func normalizeLevels(levels []string) []string {
	normalized := make([]string, 0, len(levels))
	for _, level := range levels {
		normalized = append(normalized, strings.ToLower(strings.TrimSpace(level)))
	}
	return normalized
}

// convertAutoToMidRange converts ModeAuto to a mid-range value when dynamic is not allowed.
//
// This function handles the case where a model does not support dynamic/auto thinking.
// The auto mode is silently converted to a fixed value based on model capability:
//   - Level-only models: convert to ModeLevel with LevelMedium
//   - Budget models: convert to ModeBudget with mid = (Min + Max) / 2
//
// Logging:
//   - Debug level when conversion occurs
//   - Fields: original_mode, clamped_to, reason
func convertAutoToMidRange(config ThinkingConfig, support *registry.ThinkingSupport, provider, model string) ThinkingConfig {
	// For level-only models (has Levels but no Min/Max range), use ModeLevel with medium
	if len(support.Levels) > 0 && support.Min == 0 && support.Max == 0 {
		config.Mode = ModeLevel
		config.Level = LevelMedium
		config.Budget = 0
		log.WithFields(log.Fields{
			"provider":      provider,
			"model":         model,
			"original_mode": "auto",
			"clamped_to":    string(LevelMedium),
		}).Debug("thinking: mode converted, dynamic not allowed, using medium level |")
		return config
	}

	// For budget models, use mid-range budget
	mid := (support.Min + support.Max) / 2
	if mid <= 0 && support.ZeroAllowed {
		config.Mode = ModeNone
		config.Budget = 0
	} else if mid <= 0 {
		config.Mode = ModeBudget
		config.Budget = support.Min
	} else {
		config.Mode = ModeBudget
		config.Budget = mid
	}
	log.WithFields(log.Fields{
		"provider":      provider,
		"model":         model,
		"original_mode": "auto",
		"clamped_to":    config.Budget,
	}).Debug("thinking: mode converted, dynamic not allowed |")
	return config
}

// logClamp logs a debug message when budget clamping occurs.
func logClamp(provider, model string, original, clampedTo, min, max int) {
	log.WithFields(log.Fields{
		"provider":       provider,
		"model":          model,
		"original_value": original,
		"min":            min,
		"max":            max,
		"clamped_to":     clampedTo,
	}).Debug("thinking: budget clamped |")
}
