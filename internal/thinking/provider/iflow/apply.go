// Package iflow implements thinking configuration for iFlow models.
//
// iFlow models use boolean toggle semantics:
//   - Models using chat_template_kwargs.enable_thinking (boolean toggle)
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
//   - enable_thinking toggle models: enable_thinking boolean
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

	if isEnableThinkingModel(modelInfo.ID) {
		return applyEnableThinking(body, config, isGLMModel(modelInfo.ID)), nil
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

// applyEnableThinking applies thinking configuration for models that use
// chat_template_kwargs.enable_thinking format.
//
// Output format when enabled:
//
//	{"chat_template_kwargs": {"enable_thinking": true, "clear_thinking": false}}
//
// Output format when disabled:
//
//	{"chat_template_kwargs": {"enable_thinking": false}}
//
// Note: clear_thinking is only set for GLM models when thinking is enabled.
func applyEnableThinking(body []byte, config thinking.ThinkingConfig, setClearThinking bool) []byte {
	enableThinking := configToBoolean(config)

	if len(body) == 0 || !gjson.ValidBytes(body) {
		body = []byte(`{}`)
	}

	result, _ := sjson.SetBytes(body, "chat_template_kwargs.enable_thinking", enableThinking)

	// clear_thinking is a GLM-only knob, strip it for other models.
	result, _ = sjson.DeleteBytes(result, "chat_template_kwargs.clear_thinking")

	// clear_thinking only needed when thinking is enabled
	if enableThinking && setClearThinking {
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

// isEnableThinkingModel determines if the model uses chat_template_kwargs.enable_thinking format.
func isEnableThinkingModel(modelID string) bool {
	if isGLMModel(modelID) {
		return true
	}
	id := strings.ToLower(modelID)
	switch id {
	case "qwen3-max-preview", "deepseek-v3.2", "deepseek-v3.1":
		return true
	default:
		return false
	}
}

// isGLMModel determines if the model is a GLM series model.
func isGLMModel(modelID string) bool {
	return strings.HasPrefix(strings.ToLower(modelID), "glm")
}

// isMiniMaxModel determines if the model is a MiniMax series model.
// MiniMax models use reasoning_split format.
func isMiniMaxModel(modelID string) bool {
	return strings.HasPrefix(strings.ToLower(modelID), "minimax")
}
