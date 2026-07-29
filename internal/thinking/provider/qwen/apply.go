// Package qwen implements thinking configuration for Qwen/DashScope models.
//
// Qwen models use enable_thinking (boolean) as the primary thinking toggle.
// Newer models (qwen3.7+) also accept reasoning_effort for intensity control,
// but the canonical Qwen format is enable_thinking.
// The top-level reasoning_effort field from OpenAI format is removed and
// translated to the Qwen-native enable_thinking parameter.
package qwen

import (
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Applier implements thinking.ProviderApplier for Qwen models.
//
// Qwen-specific behavior:
//   - Enabled thinking: enable_thinking=true
//   - Disabled thinking: enable_thinking=false
//   - Removes reasoning_effort from the final payload (not Qwen-native)
//   - Supports budget-to-level conversion for unified pipeline
type Applier struct{}

var _ thinking.ProviderApplier = (*Applier)(nil)

// NewApplier creates a new Qwen thinking applier.
func NewApplier() *Applier {
	return &Applier{}
}

func init() {
	thinking.RegisterProvider("qwen", NewApplier())
}

// Apply applies thinking configuration to Qwen request body.
//
// Expected output format (enabled):
//
//	{
//	  "enable_thinking": true
//	}
//
// Expected output format (disabled):
//
//	{
//	  "enable_thinking": false
//	}
func (a *Applier) Apply(body []byte, config thinking.ThinkingConfig, modelInfo *registry.ModelInfo) ([]byte, error) {
	if thinking.IsUserDefinedModel(modelInfo) {
		return applyCompatibleQwen(body, config)
	}
	if modelInfo.Thinking == nil {
		// Model does not support thinking; strip any thinking params.
		return stripThinkingParams(body)
	}

	if len(body) == 0 || !gjson.ValidBytes(body) {
		body = []byte(`{}`)
	}

	switch config.Mode {
	case thinking.ModeLevel:
		if config.Level == "" {
			return body, nil
		}
		if config.Level == thinking.LevelNone {
			return applyDisabledThinking(body)
		}
		return applyEnabledThinking(body)
	case thinking.ModeNone:
		// Respect clamped fallback level for models that cannot disable thinking.
		if config.Level != "" && config.Level != thinking.LevelNone {
			return applyEnabledThinking(body)
		}
		return applyDisabledThinking(body)
	case thinking.ModeBudget:
		level, ok := thinking.ConvertBudgetToLevel(config.Budget)
		if !ok {
			return body, nil
		}
		if level == string(thinking.LevelNone) {
			return applyDisabledThinking(body)
		}
		return applyEnabledThinking(body)
	case thinking.ModeAuto:
		// Auto mode: enable thinking and let the model decide intensity.
		return applyEnabledThinking(body)
	default:
		return body, nil
	}
}

// applyCompatibleQwen applies thinking config for user-defined Qwen models.
func applyCompatibleQwen(body []byte, config thinking.ThinkingConfig) ([]byte, error) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		body = []byte(`{}`)
	}

	switch config.Mode {
	case thinking.ModeLevel:
		if config.Level == "" {
			return body, nil
		}
		if config.Level == thinking.LevelNone {
			return applyDisabledThinking(body)
		}
		return applyEnabledThinking(body)
	case thinking.ModeNone:
		if config.Level == "" || config.Level == thinking.LevelNone {
			return applyDisabledThinking(body)
		}
		return applyEnabledThinking(body)
	case thinking.ModeAuto:
		return applyEnabledThinking(body)
	case thinking.ModeBudget:
		level, ok := thinking.ConvertBudgetToLevel(config.Budget)
		if !ok {
			return body, nil
		}
		if level == string(thinking.LevelNone) {
			return applyDisabledThinking(body)
		}
		return applyEnabledThinking(body)
	default:
		return body, nil
	}
}

func applyEnabledThinking(body []byte) ([]byte, error) {
	result, errDelete := sjson.DeleteBytes(body, "reasoning_effort")
	if errDelete != nil {
		return body, fmt.Errorf("qwen thinking: failed to clear reasoning_effort: %w", errDelete)
	}
	result, errSet := sjson.SetBytes(result, "enable_thinking", true)
	if errSet != nil {
		return body, fmt.Errorf("qwen thinking: failed to set enable_thinking: %w", errSet)
	}
	return result, nil
}

func applyDisabledThinking(body []byte) ([]byte, error) {
	result, errDelete := sjson.DeleteBytes(body, "reasoning_effort")
	if errDelete != nil {
		return body, fmt.Errorf("qwen thinking: failed to clear reasoning_effort: %w", errDelete)
	}
	result, errSet := sjson.SetBytes(result, "enable_thinking", false)
	if errSet != nil {
		return body, fmt.Errorf("qwen thinking: failed to set enable_thinking: %w", errSet)
	}
	return result, nil
}

// stripThinkingParams removes all thinking-related parameters from the payload.
func stripThinkingParams(body []byte) ([]byte, error) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return body, nil
	}
	result := body
	if gjson.GetBytes(result, "reasoning_effort").Exists() {
		var err error
		result, err = sjson.DeleteBytes(result, "reasoning_effort")
		if err != nil {
			return body, err
		}
	}
	if gjson.GetBytes(result, "enable_thinking").Exists() {
		var err error
		result, err = sjson.DeleteBytes(result, "enable_thinking")
		if err != nil {
			return body, err
		}
	}
	return result, nil
}
