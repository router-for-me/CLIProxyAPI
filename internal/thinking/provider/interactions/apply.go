// Package interactions applies native Interactions thinking configuration.
package interactions

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Applier implements thinking.ProviderApplier for the native Interactions API.
type Applier struct{}

// NewApplier creates a new Interactions thinking applier.
func NewApplier() *Applier {
	return &Applier{}
}

func init() {
	thinking.RegisterProvider("interactions", NewApplier())
}

// Apply writes thinking configuration using native Interactions generation_config fields.
func (a *Applier) Apply(body []byte, config thinking.ThinkingConfig, modelInfo *registry.ModelInfo) ([]byte, error) {
	if config.Mode != thinking.ModeBudget && config.Mode != thinking.ModeLevel && config.Mode != thinking.ModeNone && config.Mode != thinking.ModeAuto {
		return body, nil
	}
	if len(body) == 0 || !gjson.ValidBytes(body) {
		body = []byte(`{}`)
	}

	result := stripInteractionsThinkingFields(body)
	switch config.Mode {
	case thinking.ModeLevel:
		return applyInteractionsLevel(result, body, string(config.Level), modelInfo, "auto"), nil
	case thinking.ModeBudget:
		return applyInteractionsBudget(result, body, config.Budget, modelInfo, "auto"), nil
	case thinking.ModeAuto:
		return setInteractionsThinkingSummaries(result, body, "auto"), nil
	case thinking.ModeNone:
		return applyInteractionsNone(result, body, config, modelInfo), nil
	default:
		return body, nil
	}
}

func applyInteractionsBudget(result, original []byte, budget int, modelInfo *registry.ModelInfo, summariesFallback string) []byte {
	level, ok := thinking.ConvertBudgetToLevel(budget)
	if !ok {
		return result
	}
	switch level {
	case string(thinking.LevelNone):
		return setInteractionsThinkingSummaries(result, original, "none")
	case string(thinking.LevelAuto):
		return setInteractionsThinkingSummaries(result, original, "auto")
	default:
		return applyInteractionsLevel(result, original, level, modelInfo, summariesFallback)
	}
}

func applyInteractionsLevel(result, original []byte, level string, modelInfo *registry.ModelInfo, summariesFallback string) []byte {
	level = normalizeInteractionsLevel(level, modelInfo)
	if level == "" {
		return result
	}
	result, _ = sjson.SetBytes(result, "generation_config.thinking_level", level)
	return setInteractionsThinkingSummaries(result, original, summariesFallback)
}

func applyInteractionsNone(result, original []byte, config thinking.ThinkingConfig, modelInfo *registry.ModelInfo) []byte {
	if config.Level != "" {
		result = applyInteractionsLevel(result, original, string(config.Level), modelInfo, "none")
	} else if config.Budget > 0 {
		result = applyInteractionsBudget(result, original, config.Budget, modelInfo, "none")
	}
	result, _ = sjson.SetBytes(result, "generation_config.thinking_summaries", "none")
	return result
}

func stripInteractionsThinkingFields(body []byte) []byte {
	result := body
	for _, path := range []string{
		"generation_config.thinking_level",
		"generation_config.thinkingLevel",
		"generation_config.thinking_budget",
		"generation_config.thinkingBudget",
		"generation_config.thinking_summaries",
		"generation_config.thinkingSummaries",
		"generation_config.thinking_config",
		"generation_config.thinkingConfig",
		"generationConfig.thinkingLevel",
		"generationConfig.thinking_level",
		"generationConfig.thinkingBudget",
		"generationConfig.thinking_budget",
		"generationConfig.thinkingSummaries",
		"generationConfig.thinking_summaries",
		"generationConfig.thinkingConfig",
	} {
		result, _ = sjson.DeleteBytes(result, path)
	}
	return result
}

func setInteractionsThinkingSummaries(result, original []byte, fallback string) []byte {
	if value, okValue := originalInteractionsThinkingSummaries(original); okValue {
		result, _ = sjson.SetBytes(result, "generation_config.thinking_summaries", value)
		return result
	}
	if includeThoughts, okValue := originalInteractionsIncludeThoughts(original); okValue {
		value := "none"
		if includeThoughts {
			value = fallback
			if value == "" {
				value = "auto"
			}
		}
		result, _ = sjson.SetBytes(result, "generation_config.thinking_summaries", value)
		return result
	}
	if fallback != "" {
		result, _ = sjson.SetBytes(result, "generation_config.thinking_summaries", fallback)
	}
	return result
}

func originalInteractionsThinkingSummaries(body []byte) (string, bool) {
	for _, path := range []string{
		"generation_config.thinking_summaries",
		"generation_config.thinkingSummaries",
	} {
		value := gjson.GetBytes(body, path)
		if value.Exists() && value.Type == gjson.String {
			return strings.ToLower(strings.TrimSpace(value.String())), true
		}
	}
	return "", false
}

func originalInteractionsIncludeThoughts(body []byte) (bool, bool) {
	for _, path := range []string{
		"generation_config.thinking_config.include_thoughts",
		"generation_config.thinking_config.includeThoughts",
		"generation_config.thinkingConfig.include_thoughts",
		"generation_config.thinkingConfig.includeThoughts",
	} {
		value := gjson.GetBytes(body, path)
		if value.Exists() {
			return value.Bool(), true
		}
	}
	return false, false
}

func normalizeInteractionsLevel(level string, modelInfo *registry.ModelInfo) string {
	level = strings.ToLower(strings.TrimSpace(level))
	if level == "" || level == string(thinking.LevelNone) || level == string(thinking.LevelAuto) {
		return ""
	}
	if modelInfo != nil && modelInfo.Thinking != nil && len(modelInfo.Thinking.Levels) > 0 {
		for _, candidate := range modelInfo.Thinking.Levels {
			if strings.EqualFold(candidate, level) {
				return strings.ToLower(candidate)
			}
		}
		return strings.ToLower(modelInfo.Thinking.Levels[len(modelInfo.Thinking.Levels)-1])
	}
	switch level {
	case string(thinking.LevelMax), string(thinking.LevelXHigh):
		return string(thinking.LevelHigh)
	default:
		return level
	}
}
