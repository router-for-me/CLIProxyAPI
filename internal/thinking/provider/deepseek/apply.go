// Package deepseek implements thinking configuration for DeepSeek OpenAI-compatible models.
package deepseek

import (
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Applier implements thinking.ProviderApplier for DeepSeek models.
type Applier struct{}

var _ thinking.ProviderApplier = (*Applier)(nil)

// NewApplier creates a new DeepSeek thinking applier.
func NewApplier() *Applier {
	return &Applier{}
}

func init() {
	thinking.RegisterProvider("deepseek", NewApplier())
}

// Apply applies DeepSeek thinking configuration to an OpenAI-compatible request body.
func (a *Applier) Apply(body []byte, config thinking.ThinkingConfig, modelInfo *registry.ModelInfo) ([]byte, error) {
	if modelInfo != nil && modelInfo.Thinking == nil {
		return body, nil
	}
	if len(body) == 0 || !gjson.ValidBytes(body) {
		body = []byte(`{}`)
	}

	switch config.Mode {
	case thinking.ModeNone:
		result, _ := sjson.SetBytes(body, "thinking.type", "disabled")
		result, _ = sjson.DeleteBytes(result, "reasoning_effort")
		return result, nil
	case thinking.ModeAuto:
		return applyDeepSeekEffort(body, string(thinking.LevelHigh))
	case thinking.ModeLevel:
		if config.Level == thinking.LevelNone {
			return a.Apply(body, thinking.ThinkingConfig{Mode: thinking.ModeNone}, modelInfo)
		}
		effort, ok := mapDeepSeekEffort(config.Level)
		if !ok {
			return body, nil
		}
		return applyDeepSeekEffort(body, effort)
	case thinking.ModeBudget:
		level, ok := thinking.ConvertBudgetToLevel(config.Budget)
		if !ok {
			return body, nil
		}
		if level == string(thinking.LevelNone) {
			return a.Apply(body, thinking.ThinkingConfig{Mode: thinking.ModeNone}, modelInfo)
		}
		effort, ok := mapDeepSeekEffort(thinking.ThinkingLevel(level))
		if !ok {
			return body, nil
		}
		return applyDeepSeekEffort(body, effort)
	default:
		return body, nil
	}
}

func applyDeepSeekEffort(body []byte, effort string) ([]byte, error) {
	result, _ := sjson.SetBytes(body, "thinking.type", "enabled")
	result, _ = sjson.SetBytes(result, "reasoning_effort", effort)
	return result, nil
}

func mapDeepSeekEffort(level thinking.ThinkingLevel) (string, bool) {
	switch level {
	case thinking.LevelMinimal, thinking.LevelLow, thinking.LevelMedium, thinking.LevelHigh, thinking.LevelAuto:
		return string(thinking.LevelHigh), true
	case thinking.LevelXHigh, thinking.LevelMax:
		return string(thinking.LevelMax), true
	default:
		return "", false
	}
}
