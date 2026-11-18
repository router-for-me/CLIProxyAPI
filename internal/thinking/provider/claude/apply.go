// Package claude implements thinking configuration scaffolding for Claude models.
package claude

import (
	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

// Applier implements thinking.ProviderApplier for Claude models.
type Applier struct{}

var _ thinking.ProviderDocApplier = (*Applier)(nil)

// NewApplier creates a new Claude thinking applier.
func NewApplier() *Applier {
	return &Applier{}
}

func init() {
	thinking.RegisterProvider("claude", NewApplier())
}

// Apply applies thinking configuration to Claude request body.
func (a *Applier) Apply(body []byte, config thinking.ThinkingConfig, modelInfo *registry.ModelInfo) ([]byte, error) {
	if !thinking.IsUserDefinedModel(modelInfo) && modelInfo.Thinking == nil {
		return body, nil
	}
	if config.Mode != thinking.ModeBudget && config.Mode != thinking.ModeNone && config.Mode != thinking.ModeAuto && config.Mode != thinking.ModeLevel {
		return body, nil
	}
	if thinking.IsUserDefinedModel(modelInfo) && config.Mode == thinking.ModeLevel && config.Level == "" {
		return body, nil
	}
	if !thinking.IsUserDefinedModel(modelInfo) && config.Mode == thinking.ModeLevel {
		supportsAdaptive := modelInfo != nil && modelInfo.Thinking != nil && len(modelInfo.Thinking.Levels) > 0
		if !(supportsAdaptive && config.Level != "") {
			if _, ok := thinking.ConvertLevelToBudget(string(config.Level)); !ok {
				return body, nil
			}
		}
	}

	if len(body) == 0 || !ojson.ValidBytes(body) {
		body = []byte("{}")
	}
	doc := ojson.NewDoc(body)
	if errApply := a.ApplyDoc(doc, config, modelInfo); errApply != nil {
		return body, errApply
	}
	return doc.Bytes(), nil
}

// ApplyDoc applies thinking configuration onto doc in place.
func (a *Applier) ApplyDoc(doc *ojson.Doc, config thinking.ThinkingConfig, modelInfo *registry.ModelInfo) error {
	if doc == nil {
		return nil
	}
	if thinking.IsUserDefinedModel(modelInfo) {
		return applyCompatibleClaudeDoc(doc, config)
	}
	if modelInfo.Thinking == nil {
		return nil
	}

	supportsAdaptive := modelInfo != nil && modelInfo.Thinking != nil && len(modelInfo.Thinking.Levels) > 0

	switch config.Mode {
	case thinking.ModeNone:
		disableThinkingDoc(doc, modelInfo)
		return nil

	case thinking.ModeLevel:
		if supportsAdaptive && config.Level != "" {
			_ = doc.Set("thinking.type", "adaptive")
			_ = util.DeleteJSONDocIfExists(doc, "thinking.budget_tokens")
			_ = doc.Set("output_config.effort", string(config.Level))
			return nil
		}

		if budget, ok := thinking.ConvertLevelToBudget(string(config.Level)); ok {
			config.Mode = thinking.ModeBudget
			config.Budget = budget
			config.Level = ""
		} else {
			return nil
		}
		fallthrough

	case thinking.ModeBudget:
		if config.Budget == 0 {
			disableThinkingDoc(doc, modelInfo)
			return nil
		}

		_ = doc.Set("thinking.type", "enabled")
		_ = doc.Set("thinking.budget_tokens", config.Budget)
		_ = util.DeleteJSONDocIfExists(doc, "output_config.effort")
		if oc := doc.Get("output_config"); oc.Exists() && oc.IsObject() && len(oc.Map()) == 0 {
			_ = doc.Delete("output_config")
		}

		a.normalizeClaudeBudgetDoc(doc, config.Budget, modelInfo)
		return nil

	case thinking.ModeAuto:
		thinkingType := "enabled"
		if supportsAdaptive {
			thinkingType = "adaptive"
		}
		_ = doc.Set("thinking.type", thinkingType)
		_ = util.DeleteJSONDocIfExists(doc, "thinking.budget_tokens")
		_ = util.DeleteJSONDocIfExists(doc, "output_config.effort")
		if oc := doc.Get("output_config"); oc.Exists() && oc.IsObject() && len(oc.Map()) == 0 {
			_ = doc.Delete("output_config")
		}
		return nil

	default:
		return nil
	}
}

func disableThinkingDoc(doc *ojson.Doc, modelInfo *registry.ModelInfo) {
	disableType := "disabled"
	if modelInfo != nil && modelInfo.Thinking != nil && modelInfo.Thinking.DisableType != "" {
		disableType = modelInfo.Thinking.DisableType
	}
	_ = doc.SetRaw("thinking", "{}")
	_ = doc.Set("thinking.type", disableType)
	_ = util.DeleteJSONDocIfExists(doc, "output_config.effort")
	if oc := doc.Get("output_config"); oc.Exists() && oc.IsObject() && len(oc.Map()) == 0 {
		_ = doc.Delete("output_config")
	}
}

func (a *Applier) normalizeClaudeBudgetDoc(doc *ojson.Doc, budgetTokens int, modelInfo *registry.ModelInfo) {
	if budgetTokens <= 0 {
		return
	}

	effectiveMax, setDefaultMax := a.effectiveMaxTokensDoc(doc, modelInfo)
	if setDefaultMax && effectiveMax > 0 {
		_ = doc.Set("max_tokens", effectiveMax)
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
		return
	}

	if adjustedBudget != budgetTokens {
		_ = doc.Set("thinking.budget_tokens", adjustedBudget)
	}
}

func (a *Applier) effectiveMaxTokensDoc(doc *ojson.Doc, modelInfo *registry.ModelInfo) (max int, fromModel bool) {
	if maxTok := doc.Get("max_tokens"); maxTok.Exists() && maxTok.Int() > 0 {
		return int(maxTok.Int()), false
	}
	if modelInfo != nil && modelInfo.MaxCompletionTokens > 0 {
		return modelInfo.MaxCompletionTokens, true
	}
	return 0, false
}

func applyCompatibleClaudeDoc(doc *ojson.Doc, config thinking.ThinkingConfig) error {
	switch config.Mode {
	case thinking.ModeNone:
		disableThinkingDoc(doc, nil)
		return nil
	case thinking.ModeAuto:
		_ = doc.Set("thinking.type", "enabled")
		_ = util.DeleteJSONDocIfExists(doc, "thinking.budget_tokens")
		_ = util.DeleteJSONDocIfExists(doc, "output_config.effort")
		if oc := doc.Get("output_config"); oc.Exists() && oc.IsObject() && len(oc.Map()) == 0 {
			_ = doc.Delete("output_config")
		}
		return nil
	case thinking.ModeLevel:
		if config.Level == "" {
			return nil
		}
		_ = doc.Set("thinking.type", "adaptive")
		_ = util.DeleteJSONDocIfExists(doc, "thinking.budget_tokens")
		_ = doc.Set("output_config.effort", string(config.Level))
		return nil
	case thinking.ModeBudget:
		if config.Budget == 0 {
			disableThinkingDoc(doc, nil)
			return nil
		}
		_ = doc.Set("thinking.type", "enabled")
		_ = doc.Set("thinking.budget_tokens", config.Budget)
		_ = util.DeleteJSONDocIfExists(doc, "output_config.effort")
		if oc := doc.Get("output_config"); oc.Exists() && oc.IsObject() && len(oc.Map()) == 0 {
			_ = doc.Delete("output_config")
		}
		return nil
	default:
		return nil
	}
}
