// Package iflow implements thinking configuration for iFlow models (GLM, MiniMax).
//
// iFlow models use boolean toggle semantics:
//   - GLM models: chat_template_kwargs.enable_thinking (boolean)
//   - MiniMax models: reasoning_split (boolean)
//
// Level values are converted to boolean: none=false, all others=true
// See: _bmad-output/planning-artifacts/architecture.md#Epic-9
package iflow

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Applier implements thinking.ProviderApplier for iFlow models.
//
// iFlow-specific behavior:
//   - GLM models: enable_thinking boolean + clear_thinking=false
//   - MiniMax models: reasoning_split boolean
//   - Level to boolean: none=false, others=true
//   - No quantized support (only on/off)
type Applier struct{}

var _ thinking.ProviderApplier = (*Applier)(nil)

// NewApplier creates a new iFlow thinking applier.
func NewApplier() *Applier {
	return &Applier{}
}

func init() {
	thinking.RegisterProvider("iflow", NewApplier())
}

// Apply applies thinking configuration to iFlow request body.
//
// Expected output format (GLM):
//
//	{
//	  "chat_template_kwargs": {
//	    "enable_thinking": true,
//	    "clear_thinking": false
//	  }
//	}
//
// Expected output format (MiniMax):
//
//	{
//	  "reasoning_split": true
//	}
func (a *Applier) Apply(body []byte, config thinking.ThinkingConfig, modelInfo *registry.ModelInfo) ([]byte, error) {
	if thinking.IsUserDefinedModel(modelInfo) {
		return body, nil
	}
	if modelInfo.Thinking == nil {
		return body, nil
	}

	if isGLMModel(modelInfo.ID) {
		return applyGLM(body, config), nil
	}

	if isMiniMaxModel(modelInfo.ID) {
		return applyMiniMax(body, config), nil
	}

	return body, nil
}

// configToBoolean converts ThinkingConfig to boolean for iFlow models.
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

// applyGLM applies thinking configuration for GLM models.
//
// Output format when enabled:
//
//	{"chat_template_kwargs": {"enable_thinking": true, "clear_thinking": false}}
//
// Output format when disabled:
//
//	{"chat_template_kwargs": {"enable_thinking": false}}
//
// Note: clear_thinking is only set when thinking is enabled, to preserve
// thinking output in the response.
func applyGLM(body []byte, config thinking.ThinkingConfig) []byte {
	enableThinking := configToBoolean(config)

	if len(body) == 0 || !gjson.ValidBytes(body) {
		body = []byte(`{}`)
	}

	result, _ := sjson.SetBytes(body, "chat_template_kwargs.enable_thinking", enableThinking)

	// clear_thinking only needed when thinking is enabled
	if enableThinking {
		result, _ = sjson.SetBytes(result, "chat_template_kwargs.clear_thinking", false)
	}

	return result
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

	result, _ := sjson.SetBytes(body, "reasoning_split", reasoningSplit)

	return result
}

// isGLMModel determines if the model is a GLM series model.
// GLM models use chat_template_kwargs.enable_thinking format.
func isGLMModel(modelID string) bool {
	return strings.HasPrefix(strings.ToLower(modelID), "glm")
}

// isMiniMaxModel determines if the model is a MiniMax series model.
// MiniMax models use reasoning_split format.
func isMiniMaxModel(modelID string) bool {
	return strings.HasPrefix(strings.ToLower(modelID), "minimax")
}
