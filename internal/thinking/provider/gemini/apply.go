// Package gemini implements thinking configuration for Gemini models.
//
// Gemini models have two formats:
//   - Gemini 2.5: Uses thinkingBudget (numeric)
//   - Gemini 3.x: Uses thinkingLevel (string: minimal/low/medium/high)
//     or thinkingBudget=-1 for auto/dynamic mode
//
// Output format is determined by ThinkingConfig.Mode and ThinkingSupport.Levels:
//   - ModeAuto: Always uses thinkingBudget=-1 (both Gemini 2.5 and 3.x)
//   - len(Levels) > 0: Uses thinkingLevel (Gemini 3.x discrete levels)
//   - len(Levels) == 0: Uses thinkingBudget (Gemini 2.5)
package gemini

import (
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Applier applies thinking configuration for Gemini models.
//
// Gemini-specific behavior:
//   - Gemini 2.5: thinkingBudget format, flash series supports ZeroAllowed
//   - Gemini 3.x: thinkingLevel format, cannot be disabled
//   - Use ThinkingSupport.Levels to decide output format
type Applier struct{}

// NewApplier creates a new Gemini thinking applier.
func NewApplier() *Applier {
	return &Applier{}
}

func init() {
	thinking.RegisterProvider("gemini", NewApplier())
}

// Apply applies thinking configuration to Gemini request body.
//
// Expected output format (Gemini 2.5):
//
//	{
//	  "generationConfig": {
//	    "thinkingConfig": {
//	      "thinkingBudget": 8192,
//	      "includeThoughts": true
//	    }
//	  }
//	}
//
// Expected output format (Gemini 3.x):
//
//	{
//	  "generationConfig": {
//	    "thinkingConfig": {
//	      "thinkingLevel": "high",
//	      "includeThoughts": true
//	    }
//	  }
//	}
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

	// Choose format based on config.Mode and model capabilities:
	// - ModeLevel: use Level format (validation will reject unsupported levels)
	// - ModeNone: use Level format if model has Levels, else Budget format
	// - ModeBudget/ModeAuto: use Budget format
	switch config.Mode {
	case thinking.ModeLevel:
		return a.applyLevelFormat(body, config)
	case thinking.ModeNone:
		// ModeNone: route based on model capability (has Levels or not)
		if len(modelInfo.Thinking.Levels) > 0 {
			return a.applyLevelFormat(body, config)
		}
		return a.applyBudgetFormat(body, config)
	default:
		return a.applyBudgetFormat(body, config)
	}
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
	// ModeNone semantics:
	//   - ModeNone + Budget=0: completely disable thinking (not possible for Level-only models)
	//   - ModeNone + Budget>0: forced to think but hide output (includeThoughts=false)
	// ValidateConfig sets config.Level to the lowest level when ModeNone + Budget > 0.

	// Remove conflicting fields to avoid both thinkingLevel and thinkingBudget in output
	result, _ := sjson.DeleteBytes(body, "generationConfig.thinkingConfig.thinkingBudget")
	result, _ = sjson.DeleteBytes(result, "generationConfig.thinkingConfig.thinking_budget")
	result, _ = sjson.DeleteBytes(result, "generationConfig.thinkingConfig.thinking_level")
	// Normalize includeThoughts field name to avoid oneof conflicts in upstream JSON parsing.
	result, _ = sjson.DeleteBytes(result, "generationConfig.thinkingConfig.include_thoughts")

	if config.Mode == thinking.ModeNone {
		result, _ = sjson.SetBytes(result, "generationConfig.thinkingConfig.includeThoughts", false)
		if config.Level != "" {
			result, _ = sjson.SetBytes(result, "generationConfig.thinkingConfig.thinkingLevel", string(config.Level))
		}
		return result, nil
	}

	// Only handle ModeLevel - budget conversion should be done by upper layer
	if config.Mode != thinking.ModeLevel {
		return body, nil
	}

	level := string(config.Level)
	result, _ = sjson.SetBytes(result, "generationConfig.thinkingConfig.thinkingLevel", level)

	// Respect user's explicit includeThoughts setting from original body; default to true if not set
	// Support both camelCase and snake_case variants
	includeThoughts := true
	if inc := gjson.GetBytes(body, "generationConfig.thinkingConfig.includeThoughts"); inc.Exists() {
		includeThoughts = inc.Bool()
	} else if inc := gjson.GetBytes(body, "generationConfig.thinkingConfig.include_thoughts"); inc.Exists() {
		includeThoughts = inc.Bool()
	}
	result, _ = sjson.SetBytes(result, "generationConfig.thinkingConfig.includeThoughts", includeThoughts)
	return result, nil
}

func (a *Applier) applyBudgetFormat(body []byte, config thinking.ThinkingConfig) ([]byte, error) {
	// Remove conflicting fields to avoid both thinkingLevel and thinkingBudget in output
	result, _ := sjson.DeleteBytes(body, "generationConfig.thinkingConfig.thinkingLevel")
	result, _ = sjson.DeleteBytes(result, "generationConfig.thinkingConfig.thinking_level")
	result, _ = sjson.DeleteBytes(result, "generationConfig.thinkingConfig.thinking_budget")
	// Normalize includeThoughts field name to avoid oneof conflicts in upstream JSON parsing.
	result, _ = sjson.DeleteBytes(result, "generationConfig.thinkingConfig.include_thoughts")

	budget := config.Budget

	// For ModeNone, always set includeThoughts to false regardless of user setting.
	// This ensures that when user requests budget=0 (disable thinking output),
	// the includeThoughts is correctly set to false even if budget is clamped to min.
	if config.Mode == thinking.ModeNone {
		result, _ = sjson.SetBytes(result, "generationConfig.thinkingConfig.thinkingBudget", budget)
		result, _ = sjson.SetBytes(result, "generationConfig.thinkingConfig.includeThoughts", false)
		return result, nil
	}

	// Determine includeThoughts: respect user's explicit setting from original body if provided
	// Support both camelCase and snake_case variants
	var includeThoughts bool
	var userSetIncludeThoughts bool
	if inc := gjson.GetBytes(body, "generationConfig.thinkingConfig.includeThoughts"); inc.Exists() {
		includeThoughts = inc.Bool()
		userSetIncludeThoughts = true
	} else if inc := gjson.GetBytes(body, "generationConfig.thinkingConfig.include_thoughts"); inc.Exists() {
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

	result, _ = sjson.SetBytes(result, "generationConfig.thinkingConfig.thinkingBudget", budget)
	result, _ = sjson.SetBytes(result, "generationConfig.thinkingConfig.includeThoughts", includeThoughts)
	return result, nil
}
