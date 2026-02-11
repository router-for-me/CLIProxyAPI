// Package claude implements thinking configuration scaffolding for Claude models.
//
// Claude models use two thinking formats:
//   - Legacy: thinking.type:"enabled" + thinking.budget_tokens (all models)
//   - Adaptive: thinking.type:"adaptive" + output_config.effort (Opus 4.6+)
//
// Adaptive thinking is the recommended mode for models that support it (AdaptiveAllowed=true).
// It lets Claude dynamically decide when and how much to think based on request complexity.
package claude

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	log "github.com/sirupsen/logrus"
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
//   - Mode conversion (Level→Budget, Auto→Budget for non-adaptive models)
//   - Budget clamping to model range
//   - ZeroAllowed constraint enforcement
//
// For adaptive-capable models (AdaptiveAllowed=true), level-based configs are emitted as:
//
//	{
//	  "thinking": {"type": "adaptive"},
//	  "output_config": {"effort": "high"}
//	}
//
// For legacy models, budget-based configs are emitted as:
//
//	{
//	  "thinking": {"type": "enabled", "budget_tokens": 16384}
//	}
//
// Disabled thinking:
//
//	{
//	  "thinking": {"type": "disabled"}
//	}
func (a *Applier) Apply(body []byte, config thinking.ThinkingConfig, modelInfo *registry.ModelInfo) ([]byte, error) {
	if thinking.IsUserDefinedModel(modelInfo) {
		return applyCompatibleClaude(body, config)
	}
	if modelInfo.Thinking == nil {
		return body, nil
	}

	if len(body) == 0 || !gjson.ValidBytes(body) {
		body = []byte(`{}`)
	}

	// Adaptive-capable models use thinking.type:"adaptive" + output_config.effort
	if modelInfo.Thinking.AdaptiveAllowed {
		return a.applyAdaptive(body, config, modelInfo)
	}

	return a.applyLegacy(body, config, modelInfo)
}

// applyAdaptive applies adaptive thinking configuration for models that support it.
// Emits thinking.type:"adaptive" with output_config.effort for level/auto configs,
// and strips any legacy budget_tokens fields.
func (a *Applier) applyAdaptive(body []byte, config thinking.ThinkingConfig, modelInfo *registry.ModelInfo) ([]byte, error) {
	// Handle disabled
	if config.Mode == thinking.ModeNone {
		result, _ := sjson.SetBytes(body, "thinking.type", "disabled")
		result, _ = sjson.DeleteBytes(result, "thinking.budget_tokens")
		result, _ = sjson.DeleteBytes(result, "output_config")
		return result, nil
	}

	result, _ := sjson.SetBytes(body, "thinking.type", "adaptive")
	result, _ = sjson.DeleteBytes(result, "thinking.budget_tokens")

	// Determine effort level
	var effort string
	switch config.Mode {
	case thinking.ModeLevel:
		effort = string(config.Level)
	case thinking.ModeAuto:
		effort = string(thinking.LevelHigh)
	case thinking.ModeBudget:
		// Convert budget to effort level for adaptive models
		if level, ok := thinking.ConvertBudgetToLevel(config.Budget); ok {
			effort = level
		} else {
			effort = string(thinking.LevelHigh)
		}
	}

	if effort != "" {
		effort = clampAdaptiveEffort(effort, modelInfo)
		result, _ = sjson.SetBytes(result, "output_config.effort", effort)
	}

	log.WithFields(log.Fields{
		"model":  modelInfo.ID,
		"effort": effort,
	}).Debug("thinking: applied adaptive mode |")

	return result, nil
}

// clampAdaptiveEffort normalizes adaptive effort to supported model levels.
// Claude Opus 4.6 supports "max" (not "xhigh"), so budgets that derive
// to xhigh should be emitted as max.
func clampAdaptiveEffort(effort string, modelInfo *registry.ModelInfo) string {
	if effort == "" || modelInfo == nil || modelInfo.Thinking == nil || len(modelInfo.Thinking.Levels) == 0 {
		return effort
	}
	if hasLevel(modelInfo.Thinking.Levels, effort) {
		return effort
	}
	if strings.EqualFold(effort, string(thinking.LevelXHigh)) && hasLevel(modelInfo.Thinking.Levels, string(thinking.LevelMax)) {
		return string(thinking.LevelMax)
	}
	return effort
}

func hasLevel(levels []string, target string) bool {
	for _, level := range levels {
		if strings.EqualFold(strings.TrimSpace(level), target) {
			return true
		}
	}
	return false
}

// applyLegacy applies traditional budget-based thinking configuration.
// Used for models that do not support adaptive thinking (pre-Opus 4.6).
func (a *Applier) applyLegacy(body []byte, config thinking.ThinkingConfig, modelInfo *registry.ModelInfo) ([]byte, error) {
	// Only process ModeBudget and ModeNone; other modes pass through
	if config.Mode != thinking.ModeBudget && config.Mode != thinking.ModeNone {
		return body, nil
	}

	if config.Budget == 0 {
		result, _ := sjson.SetBytes(body, "thinking.type", "disabled")
		result, _ = sjson.DeleteBytes(result, "thinking.budget_tokens")
		result, _ = sjson.DeleteBytes(result, "output_config")
		return result, nil
	}

	result, _ := sjson.SetBytes(body, "thinking.type", "enabled")
	result, _ = sjson.SetBytes(result, "thinking.budget_tokens", config.Budget)
	result, _ = sjson.DeleteBytes(result, "output_config")

	result = a.normalizeClaudeBudget(result, config.Budget, modelInfo)
	return result, nil
}

// normalizeClaudeBudget applies Claude-specific constraints to ensure max_tokens > budget_tokens.
// Anthropic API requires this constraint; violating it returns a 400 error.
func (a *Applier) normalizeClaudeBudget(body []byte, budgetTokens int, modelInfo *registry.ModelInfo) []byte {
	if budgetTokens <= 0 {
		return body
	}

	effectiveMax, setDefaultMax := a.effectiveMaxTokens(body, modelInfo)
	if setDefaultMax && effectiveMax > 0 {
		body, _ = sjson.SetBytes(body, "max_tokens", effectiveMax)
	}

	adjustedBudget := budgetTokens
	if effectiveMax > 0 && adjustedBudget >= effectiveMax {
		adjustedBudget = effectiveMax - 1
	}

	minBudget := 0
	if modelInfo != nil && modelInfo.Thinking != nil {
		minBudget = modelInfo.Thinking.Min
	}
	if minBudget > 0 && adjustedBudget > 0 && adjustedBudget < minBudget {
		return body
	}

	if adjustedBudget != budgetTokens {
		body, _ = sjson.SetBytes(body, "thinking.budget_tokens", adjustedBudget)
	}

	return body
}

// effectiveMaxTokens returns the max tokens to cap thinking:
// prefer request-provided max_tokens; otherwise fall back to model default.
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
	if config.Mode != thinking.ModeBudget && config.Mode != thinking.ModeNone && config.Mode != thinking.ModeAuto && config.Mode != thinking.ModeLevel {
		return body, nil
	}

	if len(body) == 0 || !gjson.ValidBytes(body) {
		body = []byte(`{}`)
	}

	switch config.Mode {
	case thinking.ModeNone:
		result, _ := sjson.SetBytes(body, "thinking.type", "disabled")
		result, _ = sjson.DeleteBytes(result, "thinking.budget_tokens")
		result, _ = sjson.DeleteBytes(result, "output_config")
		return result, nil
	case thinking.ModeAuto:
		result, _ := sjson.SetBytes(body, "thinking.type", "enabled")
		result, _ = sjson.DeleteBytes(result, "thinking.budget_tokens")
		result, _ = sjson.DeleteBytes(result, "output_config")
		return result, nil
	case thinking.ModeLevel:
		// User-defined models with level config: emit adaptive format
		result, _ := sjson.SetBytes(body, "thinking.type", "adaptive")
		result, _ = sjson.DeleteBytes(result, "thinking.budget_tokens")
		result, _ = sjson.SetBytes(result, "output_config.effort", string(config.Level))
		return result, nil
	default:
		result, _ := sjson.SetBytes(body, "thinking.type", "enabled")
		result, _ = sjson.SetBytes(result, "thinking.budget_tokens", config.Budget)
		result, _ = sjson.DeleteBytes(result, "output_config")
		return result, nil
	}
}
