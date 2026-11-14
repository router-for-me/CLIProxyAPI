// Package qoder implements thinking configuration for Qoder models.
//
// Qoder carries the effort level in parameters.reasoning_effort of its chat
// request. The canonical configuration is expressed here as a top-level
// reasoning_effort field on the OpenAI-shaped body; the Qoder executor moves it
// into parameters and reconciles it with the per-model effort list published by
// /algo/api/v2/model/list.
package qoder

import (
	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
)

// Applier implements thinking.ProviderApplier for Qoder models.
//
// Qoder-specific behavior:
//   - Enabled thinking: reasoning_effort=<level>
//   - Disabled thinking: reasoning_effort="none" (the executor drops the field
//     and reports is_reasoning=false, which is how the official client disables
//     reasoning for models whose thinking_config exposes a "disabled" entry)
//   - Budget input is converted to the nearest canonical level
type Applier struct{}

var _ thinking.ProviderApplier = (*Applier)(nil)

// NewApplier creates a new Qoder thinking applier.
func NewApplier() *Applier {
	return &Applier{}
}

func init() {
	thinking.RegisterProvider("qoder", NewApplier())
}

// Apply applies thinking configuration to a Qoder request body.
//
// Expected output format (enabled):
//
//	{
//	  "reasoning_effort": "high"
//	}
//
// Expected output format (disabled):
//
//	{
//	  "reasoning_effort": "none"
//	}
func (a *Applier) Apply(body []byte, config thinking.ThinkingConfig, modelInfo *registry.ModelInfo) ([]byte, error) {
	if len(body) == 0 || !ojson.ValidBytes(body) {
		body = []byte(`{}`)
	}
	if !thinking.IsUserDefinedModel(modelInfo) && modelInfo.Thinking == nil {
		// The catalogue publishes no thinking_config for this model, so an
		// effort would be meaningless here.
		return body, nil
	}

	var effort string
	switch config.Mode {
	case thinking.ModeLevel:
		if config.Level == "" {
			return body, nil
		}
		effort = string(config.Level)
	case thinking.ModeNone:
		// A model that cannot disable thinking falls back to its lowest level,
		// which validation already placed in config.Level.
		if config.Level != "" && config.Level != thinking.LevelNone {
			effort = string(config.Level)
			break
		}
		effort = string(thinking.LevelNone)
	case thinking.ModeBudget:
		level, ok := thinking.ConvertBudgetToLevel(config.Budget)
		if !ok {
			return body, nil
		}
		effort = level
	default:
		// ModeAuto and anything else: leave the request untouched so Qoder
		// applies its own per-model default.
		return body, nil
	}

	result, errSet := ojson.SetBytes(body, "reasoning_effort", effort)
	if errSet != nil {
		return body, errSet
	}
	return result, nil
}
