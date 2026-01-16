// Package geminicli implements thinking configuration for Gemini CLI API format.
//
// Gemini CLI uses request.generationConfig.thinkingConfig.* path instead of
// generationConfig.thinkingConfig.* used by standard Gemini API.
package geminicli

import (
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Applier applies thinking configuration for Gemini CLI API format.
type Applier struct{}

var _ thinking.ProviderApplier = (*Applier)(nil)

// NewApplier creates a new Gemini CLI thinking applier.
func NewApplier() *Applier {
	return &Applier{}
}

func init() {
	thinking.RegisterProvider("gemini-cli", NewApplier())
}

// Apply applies thinking configuration to Gemini CLI request body.
func (a *Applier) Apply(body []byte, config thinking.ThinkingConfig, modelInfo *registry.ModelInfo) ([]byte, error) {
	if thinking.IsUserDefinedModel(modelInfo) {
		return a.applyCompatible(body, config)
	}
	if modelInfo.Thinking == nil {
		return body, nil
	}

	if config.Mode != thinking.ModeBudget && config.Mode != thinking.ModeLevel && config.Mode != thinking.ModeNone && config.Mode != thinking.ModeAuto {
		return body, nil
	}

	if len(body) == 0 || !gjson.ValidBytes(body) {
		body = []byte(`{}`)
	}

	// ModeAuto: Always use Budget format with thinkingBudget=-1
	if config.Mode == thinking.ModeAuto {
		return a.applyBudgetFormat(body, config)
	}
	if config.Mode == thinking.ModeBudget {
		return a.applyBudgetFormat(body, config)
	}

	// For non-auto modes, choose format based on model capabilities
	support := modelInfo.Thinking
	if len(support.Levels) > 0 {
		return a.applyLevelFormat(body, config)
	}
	return a.applyBudgetFormat(body, config)
}

func (a *Applier) applyCompatible(body []byte, config thinking.ThinkingConfig) ([]byte, error) {
	if config.Mode != thinking.ModeBudget && config.Mode != thinking.ModeLevel && config.Mode != thinking.ModeNone && config.Mode != thinking.ModeAuto {
		return body, nil
	}

	if len(body) == 0 || !gjson.ValidBytes(body) {
		body = []byte(`{}`)
	}

	if config.Mode == thinking.ModeAuto {
		return a.applyBudgetFormat(body, config)
	}

	if config.Mode == thinking.ModeLevel || (config.Mode == thinking.ModeNone && config.Level != "") {
		return a.applyLevelFormat(body, config)
	}

	return a.applyBudgetFormat(body, config)
}

func (a *Applier) applyLevelFormat(body []byte, config thinking.ThinkingConfig) ([]byte, error) {
	// Remove conflicting field to avoid both thinkingLevel and thinkingBudget in output
	result, _ := sjson.DeleteBytes(body, "request.generationConfig.thinkingConfig.thinkingBudget")
	// Normalize includeThoughts field name to avoid oneof conflicts in upstream JSON parsing.
	result, _ = sjson.DeleteBytes(result, "request.generationConfig.thinkingConfig.include_thoughts")

	if config.Mode == thinking.ModeNone {
		result, _ = sjson.SetBytes(result, "request.generationConfig.thinkingConfig.includeThoughts", false)
		if config.Level != "" {
			result, _ = sjson.SetBytes(result, "request.generationConfig.thinkingConfig.thinkingLevel", string(config.Level))
		}
		return result, nil
	}

	// Only handle ModeLevel - budget conversion should be done by upper layer
	if config.Mode != thinking.ModeLevel {
		return body, nil
	}

	level := string(config.Level)
	result, _ = sjson.SetBytes(result, "request.generationConfig.thinkingConfig.thinkingLevel", level)
	result, _ = sjson.SetBytes(result, "request.generationConfig.thinkingConfig.includeThoughts", true)
	return result, nil
}

func (a *Applier) applyBudgetFormat(body []byte, config thinking.ThinkingConfig) ([]byte, error) {
	// Remove conflicting field to avoid both thinkingLevel and thinkingBudget in output
	result, _ := sjson.DeleteBytes(body, "request.generationConfig.thinkingConfig.thinkingLevel")
	// Normalize includeThoughts field name to avoid oneof conflicts in upstream JSON parsing.
	result, _ = sjson.DeleteBytes(result, "request.generationConfig.thinkingConfig.include_thoughts")

	budget := config.Budget
	includeThoughts := false
	switch config.Mode {
	case thinking.ModeNone:
		includeThoughts = false
	case thinking.ModeAuto:
		includeThoughts = true
	default:
		includeThoughts = budget > 0
	}

	result, _ = sjson.SetBytes(result, "request.generationConfig.thinkingConfig.thinkingBudget", budget)
	result, _ = sjson.SetBytes(result, "request.generationConfig.thinkingConfig.includeThoughts", includeThoughts)
	return result, nil
}
