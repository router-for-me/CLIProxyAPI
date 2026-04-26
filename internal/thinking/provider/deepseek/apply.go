// Package deepseek implements thinking configuration for DeepSeek models.
//
// DeepSeek V4 uses an OpenAI-compatible Chat Completions surface with two
// controls: thinking.type toggles thinking, and reasoning_effort controls effort.
package deepseek

import (
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Applier implements thinking.ProviderApplier for DeepSeek models.
type Applier struct{}

var _ thinking.ProviderApplier = (*Applier)(nil)

// NewApplier creates a new DeepSeek thinking applier.
func NewApplier() *Applier { return &Applier{} }

func init() {
	thinking.RegisterProvider("deepseek", NewApplier())
}

// Apply applies DeepSeek thinking configuration to an OpenAI-compatible body.
func (a *Applier) Apply(body []byte, config thinking.ThinkingConfig, modelInfo *registry.ModelInfo) ([]byte, error) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		body = []byte(`{}`)
	}

	var effort string
	enabled := true
	switch config.Mode {
	case thinking.ModeLevel:
		if config.Level == "" {
			return body, nil
		}
		effort = normalizeEffort(config.Level)
	case thinking.ModeNone:
		enabled = false
	case thinking.ModeAuto:
		// DeepSeek defaults to high/max automatically. Keep thinking enabled and
		// avoid forcing a specific effort when the caller asks for auto.
	case thinking.ModeBudget:
		level, ok := thinking.ConvertBudgetToLevel(config.Budget)
		if !ok {
			return body, nil
		}
		effort = normalizeEffort(thinking.ThinkingLevel(level))
	default:
		return body, nil
	}

	if !thinking.IsUserDefinedModel(modelInfo) && modelInfo != nil && modelInfo.Thinking == nil && enabled {
		return body, nil
	}

	return applyDeepSeekThinking(body, enabled, effort)
}

func normalizeEffort(level thinking.ThinkingLevel) string {
	switch level {
	case thinking.LevelMinimal, thinking.LevelLow, thinking.LevelMedium, thinking.LevelHigh:
		return string(thinking.LevelHigh)
	case thinking.LevelXHigh, thinking.LevelMax:
		return string(thinking.LevelMax)
	default:
		return string(level)
	}
}

func applyDeepSeekThinking(body []byte, enabled bool, effort string) ([]byte, error) {
	result, errSetType := sjson.SetBytes(body, "thinking.type", thinkingType(enabled))
	if errSetType != nil {
		return body, fmt.Errorf("deepseek thinking: failed to set thinking.type: %w", errSetType)
	}

	if !enabled || effort == "" {
		result, errDeleteEffort := sjson.DeleteBytes(result, "reasoning_effort")
		if errDeleteEffort != nil {
			return body, fmt.Errorf("deepseek thinking: failed to clear reasoning_effort: %w", errDeleteEffort)
		}
		return result, nil
	}

	result, errSetEffort := sjson.SetBytes(result, "reasoning_effort", effort)
	if errSetEffort != nil {
		return body, fmt.Errorf("deepseek thinking: failed to set reasoning_effort: %w", errSetEffort)
	}
	return result, nil
}

func thinkingType(enabled bool) string {
	if enabled {
		return "enabled"
	}
	return "disabled"
}
