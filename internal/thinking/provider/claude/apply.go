// Package claude implements thinking configuration scaffolding for Claude models.
//
// Claude models use the thinking.budget_tokens format with values in the range
// 1024-128000. Some Claude models support ZeroAllowed (sonnet-4-5, opus-4-5),
// while older models do not.
// See: _bmad-output/planning-artifacts/architecture.md#Epic-6
package claude

import (
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Applier implements thinking.ProviderApplier for Claude models.
// This applier is stateless and holds no configuration.
type Applier struct{}

// NewApplier creates a new Claude thinking applier.
func NewApplier() *Applier {
	return &Applier{}
}

func init() {
	thinking.RegisterProvider("claude", NewApplier())
}

// Apply applies thinking configuration to Claude request body.
//
// IMPORTANT: This method expects config to be pre-validated by thinking.ValidateConfig.
// ValidateConfig handles:
//   - Mode conversion (Level→Budget, Auto→Budget)
//   - Budget clamping to model range
//   - ZeroAllowed constraint enforcement
//
// Apply only processes ModeBudget and ModeNone; other modes are passed through unchanged.
//
// Expected output format when enabled:
//
//	{
//	  "thinking": {
//	    "type": "enabled",
//	    "budget_tokens": 16384
//	  }
//	}
//
// Expected output format when disabled:
//
//	{
//	  "thinking": {
//	    "type": "disabled"
//	  }
//	}
func (a *Applier) Apply(body []byte, config thinking.ThinkingConfig, modelInfo *registry.ModelInfo) ([]byte, error) {
	if thinking.IsUserDefinedModel(modelInfo) {
		return applyCompatibleClaude(body, config)
	}
	if modelInfo.Thinking == nil {
		return body, nil
	}

	// Only process ModeBudget and ModeNone; other modes pass through
	// (caller should use ValidateConfig first to normalize modes)
	if config.Mode != thinking.ModeBudget && config.Mode != thinking.ModeNone {
		return body, nil
	}

	if len(body) == 0 || !gjson.ValidBytes(body) {
		body = []byte(`{}`)
	}

	// Budget is expected to be pre-validated by ValidateConfig (clamped, ZeroAllowed enforced)
	// Decide enabled/disabled based on budget value
	if config.Budget == 0 {
		result, _ := sjson.SetBytes(body, "thinking.type", "disabled")
		result, _ = sjson.DeleteBytes(result, "thinking.budget_tokens")
		return result, nil
	}

	result, _ := sjson.SetBytes(body, "thinking.type", "enabled")
	result, _ = sjson.SetBytes(result, "thinking.budget_tokens", config.Budget)

	// Ensure max_tokens > thinking.budget_tokens (Anthropic API constraint)
	result = a.normalizeClaudeBudget(result, config.Budget, modelInfo)
	return result, nil
}

// normalizeClaudeBudget applies Claude-specific constraints to ensure max_tokens > budget_tokens.
// Anthropic API requires this constraint; violating it returns a 400 error.
func (a *Applier) normalizeClaudeBudget(body []byte, budgetTokens int, modelInfo *registry.ModelInfo) []byte {
	if budgetTokens <= 0 {
		return body
	}

	// Ensure the request satisfies Claude constraints:
	//  1) Determine effective max_tokens (request overrides model default)
	//  2) If budget_tokens >= max_tokens, reduce budget_tokens to max_tokens-1
	//  3) If the adjusted budget falls below the model minimum, leave the request unchanged
	//  4) If max_tokens came from model default, write it back into the request

	effectiveMax, setDefaultMax := a.effectiveMaxTokens(body, modelInfo)
	if setDefaultMax && effectiveMax > 0 {
		body, _ = sjson.SetBytes(body, "max_tokens", effectiveMax)
	}

	// Compute the budget we would apply after enforcing budget_tokens < max_tokens.
	adjustedBudget := budgetTokens
	if effectiveMax > 0 && adjustedBudget >= effectiveMax {
		adjustedBudget = effectiveMax - 1
	}

	minBudget := 0
	if modelInfo != nil && modelInfo.Thinking != nil {
		minBudget = modelInfo.Thinking.Min
	}
	if minBudget > 0 && adjustedBudget > 0 && adjustedBudget < minBudget {
		// If enforcing the max_tokens constraint would push the budget below the model minimum,
		// leave the request unchanged.
		return body
	}

	if adjustedBudget != budgetTokens {
		body, _ = sjson.SetBytes(body, "thinking.budget_tokens", adjustedBudget)
	}

	return body
}

// effectiveMaxTokens returns the max tokens to cap thinking:
// prefer request-provided max_tokens; otherwise fall back to model default.
// The boolean indicates whether the value came from the model default (and thus should be written back).
func (a *Applier) effectiveMaxTokens(body []byte, modelInfo *registry.ModelInfo) (max int, fromModel bool) {
	if maxTok := gjson.GetBytes(body, "max_tokens"); maxTok.Exists() && maxTok.Int() > 0 {
		return int(maxTok.Int()), false
	}
	if modelInfo != nil && modelInfo.MaxCompletionTokens > 0 {
		return modelInfo.MaxCompletionTokens, true
	}
	return 0, false
}

func applyCompatibleClaude(body []byte, config thinking.ThinkingConfig) ([]byte, error) {
	if config.Mode != thinking.ModeBudget && config.Mode != thinking.ModeNone && config.Mode != thinking.ModeAuto {
		return body, nil
	}

	if len(body) == 0 || !gjson.ValidBytes(body) {
		body = []byte(`{}`)
	}

	switch config.Mode {
	case thinking.ModeNone:
		result, _ := sjson.SetBytes(body, "thinking.type", "disabled")
		result, _ = sjson.DeleteBytes(result, "thinking.budget_tokens")
		return result, nil
	case thinking.ModeAuto:
		result, _ := sjson.SetBytes(body, "thinking.type", "enabled")
		result, _ = sjson.DeleteBytes(result, "thinking.budget_tokens")
		return result, nil
	default:
		result, _ := sjson.SetBytes(body, "thinking.type", "enabled")
		result, _ = sjson.SetBytes(result, "thinking.budget_tokens", config.Budget)
		return result, nil
	}
}
