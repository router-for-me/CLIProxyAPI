// Package antigravity implements thinking configuration for Antigravity API format.
//
// Antigravity uses request.generationConfig.thinkingConfig.* path (same as gemini-cli)
// but requires additional normalization for Claude models:
//   - Ensure thinking budget < max_tokens
//   - Remove thinkingConfig if budget < minimum allowed
package antigravity

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Applier applies thinking configuration for Antigravity API format.
type Applier struct{}

var _ thinking.ProviderApplier = (*Applier)(nil)

// NewApplier creates a new Antigravity thinking applier.
func NewApplier() *Applier {
	return &Applier{}
}

func init() {
	thinking.RegisterProvider("antigravity", NewApplier())
}

// Apply applies thinking configuration to Antigravity request body.
//
// For Claude models, additional constraints are applied:
//   - Ensure thinking budget < max_tokens
//   - Remove thinkingConfig if budget < minimum allowed
func (a *Applier) Apply(body []byte, config thinking.ThinkingConfig, modelInfo *registry.ModelInfo) ([]byte, error) {
	if thinking.IsUserDefinedModel(modelInfo) {
		return a.applyCompatible(body, config, modelInfo)
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

	isClaude := strings.Contains(strings.ToLower(modelInfo.ID), "claude")

	// ModeAuto: Always use Budget format with thinkingBudget=-1
	if config.Mode == thinking.ModeAuto {
		return a.applyBudgetFormat(body, config, modelInfo, isClaude)
	}
	if config.Mode == thinking.ModeBudget {
		return a.applyBudgetFormat(body, config, modelInfo, isClaude)
	}

	// For non-auto modes, choose format based on model capabilities
	support := modelInfo.Thinking
	if len(support.Levels) > 0 {
		return a.applyLevelFormat(body, config)
	}
	return a.applyBudgetFormat(body, config, modelInfo, isClaude)
}

func (a *Applier) applyCompatible(body []byte, config thinking.ThinkingConfig, modelInfo *registry.ModelInfo) ([]byte, error) {
	if config.Mode != thinking.ModeBudget && config.Mode != thinking.ModeLevel && config.Mode != thinking.ModeNone && config.Mode != thinking.ModeAuto {
		return body, nil
	}

	if len(body) == 0 || !gjson.ValidBytes(body) {
		body = []byte(`{}`)
	}

	isClaude := false
	if modelInfo != nil {
		isClaude = strings.Contains(strings.ToLower(modelInfo.ID), "claude")
	}

	if config.Mode == thinking.ModeAuto {
		return a.applyBudgetFormat(body, config, modelInfo, isClaude)
	}

	if config.Mode == thinking.ModeLevel || (config.Mode == thinking.ModeNone && config.Level != "") {
		return a.applyLevelFormat(body, config)
	}

	return a.applyBudgetFormat(body, config, modelInfo, isClaude)
}

func (a *Applier) applyLevelFormat(body []byte, config thinking.ThinkingConfig) ([]byte, error) {
	// Remove conflicting fields to avoid both thinkingLevel and thinkingBudget in output
	result, _ := sjson.DeleteBytes(body, "request.generationConfig.thinkingConfig.thinkingBudget")
	result, _ = sjson.DeleteBytes(result, "request.generationConfig.thinkingConfig.thinking_budget")
	result, _ = sjson.DeleteBytes(result, "request.generationConfig.thinkingConfig.thinking_level")
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

	// Respect user's explicit includeThoughts setting from original body; default to true if not set
	// Support both camelCase and snake_case variants
	includeThoughts := true
	if inc := gjson.GetBytes(body, "request.generationConfig.thinkingConfig.includeThoughts"); inc.Exists() {
		includeThoughts = inc.Bool()
	} else if inc := gjson.GetBytes(body, "request.generationConfig.thinkingConfig.include_thoughts"); inc.Exists() {
		includeThoughts = inc.Bool()
	}
	result, _ = sjson.SetBytes(result, "request.generationConfig.thinkingConfig.includeThoughts", includeThoughts)
	return result, nil
}

func (a *Applier) applyBudgetFormat(body []byte, config thinking.ThinkingConfig, modelInfo *registry.ModelInfo, isClaude bool) ([]byte, error) {
	// Remove conflicting fields to avoid both thinkingLevel and thinkingBudget in output
	result, _ := sjson.DeleteBytes(body, "request.generationConfig.thinkingConfig.thinkingLevel")
	result, _ = sjson.DeleteBytes(result, "request.generationConfig.thinkingConfig.thinking_level")
	result, _ = sjson.DeleteBytes(result, "request.generationConfig.thinkingConfig.thinking_budget")
	// Normalize includeThoughts field name to avoid oneof conflicts in upstream JSON parsing.
	result, _ = sjson.DeleteBytes(result, "request.generationConfig.thinkingConfig.include_thoughts")

	budget := config.Budget

	// Apply Claude-specific constraints first to get the final budget value
	if isClaude && modelInfo != nil {
		budget, result = a.normalizeClaudeBudget(budget, result, modelInfo)
		// Check if budget was removed entirely
		if budget == -2 {
			return result, nil
		}
	}

	// For ModeNone, always set includeThoughts to false regardless of user setting.
	// This ensures that when user requests budget=0 (disable thinking output),
	// the includeThoughts is correctly set to false even if budget is clamped to min.
	if config.Mode == thinking.ModeNone {
		result, _ = sjson.SetBytes(result, "request.generationConfig.thinkingConfig.thinkingBudget", budget)
		result, _ = sjson.SetBytes(result, "request.generationConfig.thinkingConfig.includeThoughts", false)
		return result, nil
	}

	// Determine includeThoughts: respect user's explicit setting from original body if provided
	// Support both camelCase and snake_case variants
	var includeThoughts bool
	var userSetIncludeThoughts bool
	if inc := gjson.GetBytes(body, "request.generationConfig.thinkingConfig.includeThoughts"); inc.Exists() {
		includeThoughts = inc.Bool()
		userSetIncludeThoughts = true
	} else if inc := gjson.GetBytes(body, "request.generationConfig.thinkingConfig.include_thoughts"); inc.Exists() {
		includeThoughts = inc.Bool()
		userSetIncludeThoughts = true
	}

	if !userSetIncludeThoughts {
		// No explicit setting, use default logic based on mode
		switch config.Mode {
		case thinking.ModeAuto:
			includeThoughts = true
		default:
			includeThoughts = budget > 0
		}
	}

	result, _ = sjson.SetBytes(result, "request.generationConfig.thinkingConfig.thinkingBudget", budget)
	result, _ = sjson.SetBytes(result, "request.generationConfig.thinkingConfig.includeThoughts", includeThoughts)
	return result, nil
}

// normalizeClaudeBudget applies Claude-specific constraints to thinking budget.
//
// It handles:
//   - Ensuring thinking budget < max_tokens
//   - Removing thinkingConfig if budget < minimum allowed
//
// Returns the normalized budget and updated payload.
// Returns budget=-2 as a sentinel indicating thinkingConfig was removed entirely.
func (a *Applier) normalizeClaudeBudget(budget int, payload []byte, modelInfo *registry.ModelInfo) (int, []byte) {
	if modelInfo == nil {
		return budget, payload
	}

	// Get effective max tokens
	effectiveMax, setDefaultMax := a.effectiveMaxTokens(payload, modelInfo)
	if effectiveMax > 0 && budget >= effectiveMax {
		budget = effectiveMax - 1
	}

	// Check minimum budget
	minBudget := 0
	if modelInfo.Thinking != nil {
		minBudget = modelInfo.Thinking.Min
	}
	if minBudget > 0 && budget >= 0 && budget < minBudget {
		// Budget is below minimum, remove thinking config entirely
		payload, _ = sjson.DeleteBytes(payload, "request.generationConfig.thinkingConfig")
		return -2, payload
	}

	// Set default max tokens if needed
	if setDefaultMax && effectiveMax > 0 {
		payload, _ = sjson.SetBytes(payload, "request.generationConfig.maxOutputTokens", effectiveMax)
	}

	return budget, payload
}

// effectiveMaxTokens returns the max tokens to cap thinking:
// prefer request-provided maxOutputTokens; otherwise fall back to model default.
// The boolean indicates whether the value came from the model default (and thus should be written back).
func (a *Applier) effectiveMaxTokens(payload []byte, modelInfo *registry.ModelInfo) (max int, fromModel bool) {
	if maxTok := gjson.GetBytes(payload, "request.generationConfig.maxOutputTokens"); maxTok.Exists() && maxTok.Int() > 0 {
		return int(maxTok.Int()), false
	}
	if modelInfo != nil && modelInfo.MaxCompletionTokens > 0 {
		return modelInfo.MaxCompletionTokens, true
	}
	return 0, false
}
