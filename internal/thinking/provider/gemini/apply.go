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
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Applier applies thinking configuration for Gemini models.
//
// Gemini-specific behavior:
//   - Gemini 2.5: thinkingBudget format, flash series supports ZeroAllowed
//   - Gemini 3.x: thinkingLevel format, disable by removing thinkingConfig when zero is allowed
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
	//   - ModeNone + Budget=0: remove the thinking amount configuration.
	//   - ModeNone + Budget>0: clamp to the model's lowest supported amount.
	// Summary visibility remains independent and is restored only when explicitly set.

	// Remove conflicting fields to avoid both thinkingLevel and thinkingBudget in output
	result, _ := sjson.DeleteBytes(body, "generationConfig.thinkingConfig.thinkingBudget")
	result, _ = sjson.DeleteBytes(result, "generationConfig.thinkingConfig.thinking_budget")
	result, _ = sjson.DeleteBytes(result, "generationConfig.thinkingConfig.thinking_level")
	// Normalize includeThoughts field name and retain only documented booleans.
	result, _ = sjson.DeleteBytes(result, "generationConfig.thinkingConfig.includeThoughts")
	result, _ = sjson.DeleteBytes(result, "generationConfig.thinkingConfig.include_thoughts")

	if config.Mode == thinking.ModeNone {
		if config.Budget == 0 && config.Level == "" {
			// With the amount fully disabled, visibility is irrelevant. Restoring
			// includeThoughts alone would recreate thinkingConfig and let a
			// default-on model think again.
			result, _ = sjson.DeleteBytes(result, "generationConfig.thinkingConfig")
			return result, nil
		}
		if config.Level != "" {
			result, _ = sjson.SetBytes(result, "generationConfig.thinkingConfig.thinkingLevel", string(config.Level))
		}
		return applyGeminiIncludeThoughts(result, body), nil
	}

	// Only handle ModeLevel - budget conversion should be done by upper layer
	if config.Mode != thinking.ModeLevel {
		return body, nil
	}

	level := string(config.Level)
	result, _ = sjson.SetBytes(result, "generationConfig.thinkingConfig.thinkingLevel", level)
	return applyGeminiIncludeThoughts(result, body), nil
}

func (a *Applier) applyBudgetFormat(body []byte, config thinking.ThinkingConfig) ([]byte, error) {
	// Remove conflicting fields to avoid both thinkingLevel and thinkingBudget in output
	result, _ := sjson.DeleteBytes(body, "generationConfig.thinkingConfig.thinkingLevel")
	result, _ = sjson.DeleteBytes(result, "generationConfig.thinkingConfig.thinking_level")
	result, _ = sjson.DeleteBytes(result, "generationConfig.thinkingConfig.thinking_budget")
	// Normalize includeThoughts field name and retain only documented booleans.
	result, _ = sjson.DeleteBytes(result, "generationConfig.thinkingConfig.includeThoughts")
	result, _ = sjson.DeleteBytes(result, "generationConfig.thinkingConfig.include_thoughts")

	budget := config.Budget
	result, _ = sjson.SetBytes(result, "generationConfig.thinkingConfig.thinkingBudget", budget)
	return applyGeminiIncludeThoughts(result, body), nil
}

func applyGeminiIncludeThoughts(result, original []byte) []byte {
	for _, path := range []string{
		"generationConfig.thinkingConfig.includeThoughts",
		"generationConfig.thinkingConfig.include_thoughts",
	} {
		switch value := gjson.GetBytes(original, path); value.Type {
		case gjson.True:
			result, _ = sjson.SetBytes(result, "generationConfig.thinkingConfig.includeThoughts", true)
			return result
		case gjson.False:
			result, _ = sjson.SetBytes(result, "generationConfig.thinkingConfig.includeThoughts", false)
			return result
		}
	}
	return result
}
