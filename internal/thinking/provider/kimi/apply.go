// Package kimi implements thinking configuration for Kimi (Moonshot AI) models.
//
// Kimi models use the OpenAI-compatible reasoning_effort format with discrete levels
// (low/medium/high). The provider strips any existing thinking config and applies
// the unified ThinkingConfig in OpenAI format.
package kimi

import (
	"fmt"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Applier implements thinking.ProviderApplier for Kimi models.
//
// Kimi-specific behavior:
//   - Output format: reasoning_effort (string: low/medium/high)
//   - Uses OpenAI-compatible format
//   - Supports budget-to-level conversion
type Applier struct{}

var _ thinking.ProviderApplier = (*Applier)(nil)

// NewApplier creates a new Kimi thinking applier.
func NewApplier() *Applier {
	return &Applier{}
}

func init() {
	thinking.RegisterProvider("kimi", NewApplier())
}

// Apply applies thinking configuration to Kimi request body.
//
// Expected output format:
//
//	{
//	  "reasoning_effort": "high"
//	}
func (a *Applier) Apply(body []byte, config thinking.ThinkingConfig, modelInfo *registry.ModelInfo) ([]byte, error) {
	if thinking.IsUserDefinedModel(modelInfo) {
		return applyCompatibleKimi(body, config)
	}
	if modelInfo.Thinking == nil {
		return body, nil
	}

	if len(body) == 0 || !gjson.ValidBytes(body) {
		body = []byte(`{}`)
	}

	var effort string
	switch config.Mode {
	case thinking.ModeLevel:
		if config.Level == "" {
			return body, nil
		}
		effort = string(config.Level)
	case thinking.ModeNone:
		// Kimi uses "none" to disable thinking
		effort = string(thinking.LevelNone)
	case thinking.ModeBudget:
		// Convert budget to level using threshold mapping
		level, ok := thinking.ConvertBudgetToLevel(config.Budget)
		if !ok {
			return body, nil
		}
		effort = level
	case thinking.ModeAuto:
		// Auto mode maps to "auto" effort
		effort = string(thinking.LevelAuto)
	default:
		return body, nil
	}

	if effort == "" {
		return body, nil
	}

	result, err := sjson.SetBytes(body, "reasoning_effort", effort)
	if err != nil {
		return body, fmt.Errorf("kimi thinking: failed to set reasoning_effort: %w", err)
	}
	return result, nil
}

// applyCompatibleKimi applies thinking config for user-defined Kimi models.
func applyCompatibleKimi(body []byte, config thinking.ThinkingConfig) ([]byte, error) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		body = []byte(`{}`)
	}

	var effort string
	switch config.Mode {
	case thinking.ModeLevel:
		if config.Level == "" {
			return body, nil
		}
		effort = string(config.Level)
	case thinking.ModeNone:
		effort = string(thinking.LevelNone)
		if config.Level != "" {
			effort = string(config.Level)
		}
	case thinking.ModeAuto:
		effort = string(thinking.LevelAuto)
	case thinking.ModeBudget:
		// Convert budget to level
		level, ok := thinking.ConvertBudgetToLevel(config.Budget)
		if !ok {
			return body, nil
		}
		effort = level
	default:
		return body, nil
	}

	result, err := sjson.SetBytes(body, "reasoning_effort", effort)
	if err != nil {
		return body, fmt.Errorf("kimi thinking: failed to set reasoning_effort: %w", err)
	}
	return result, nil
}
