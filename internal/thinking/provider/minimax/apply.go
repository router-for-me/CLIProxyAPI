// Package minimax implements thinking configuration for MiniMax models.
//
// MiniMax models use a boolean toggle for thinking:
//   - reasoning_split: true/false
//
// Level values are converted to boolean: none=false, all others=true
package minimax

import (
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Applier implements thinking.ProviderApplier for MiniMax models.
//
// MiniMax-specific behavior:
//   - Uses reasoning_split boolean toggle
//   - Level to boolean: none=false, others=true
//   - No quantized support (only on/off)
type Applier struct{}

var _ thinking.ProviderApplier = (*Applier)(nil)

// NewApplier creates a new MiniMax thinking applier.
func NewApplier() *Applier {
	return &Applier{}
}

func init() {
	thinking.RegisterProvider("minimax", NewApplier())
}

// Apply applies thinking configuration to MiniMax request body.
//
// Expected output format:
//
//	{
//	  "reasoning_split": true/false
//	}
func (a *Applier) Apply(body []byte, config thinking.ThinkingConfig, modelInfo *registry.ModelInfo) ([]byte, error) {
	if thinking.IsUserDefinedModel(modelInfo) {
		return applyMiniMax(body, config), nil
	}
	if modelInfo.Thinking == nil {
		return body, nil
	}

	return applyMiniMax(body, config), nil
}

// configToBoolean converts ThinkingConfig to boolean for MiniMax models.
//
// Conversion rules:
//   - ModeNone: false
//   - ModeAuto: true
//   - ModeBudget + Budget=0: false
//   - ModeBudget + Budget>0: true
//   - ModeLevel + Level="none": false
//   - ModeLevel + any other level: true
//   - Default (unknown mode): true
func configToBoolean(config thinking.ThinkingConfig) bool {
	switch config.Mode {
	case thinking.ModeNone:
		return false
	case thinking.ModeAuto:
		return true
	case thinking.ModeBudget:
		return config.Budget > 0
	case thinking.ModeLevel:
		return config.Level != thinking.LevelNone
	default:
		return true
	}
}

// applyMiniMax applies thinking configuration for MiniMax models.
//
// Output format:
//
//	{"reasoning_split": true/false}
func applyMiniMax(body []byte, config thinking.ThinkingConfig) []byte {
	reasoningSplit := configToBoolean(config)

	if len(body) == 0 || !gjson.ValidBytes(body) {
		body = []byte(`{}`)
	}

	// Remove any OpenAI-style reasoning_effort that may have been set
	result, _ := sjson.DeleteBytes(body, "reasoning_effort")
	result, _ = sjson.SetBytes(result, "reasoning_split", reasoningSplit)

	return result
}
