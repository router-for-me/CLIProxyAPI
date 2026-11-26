// Package antigravity implements thinking configuration for Antigravity API format.
package antigravity

import (
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

// Applier applies thinking configuration for Antigravity API format.
type Applier struct{}

var _ thinking.ProviderDocApplier = (*Applier)(nil)

// NewApplier creates a new Antigravity thinking applier.
func NewApplier() *Applier {
	return &Applier{}
}

func init() {
	thinking.RegisterProvider("antigravity", NewApplier())
}

// Apply applies thinking configuration to Antigravity request body.
func (a *Applier) Apply(body []byte, config thinking.ThinkingConfig, modelInfo *registry.ModelInfo) ([]byte, error) {
	if !thinking.IsUserDefinedModel(modelInfo) && modelInfo.Thinking == nil {
		return body, nil
	}
	if config.Mode != thinking.ModeBudget && config.Mode != thinking.ModeLevel && config.Mode != thinking.ModeNone && config.Mode != thinking.ModeAuto {
		return body, nil
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
		return a.applyCompatibleDoc(doc, config, modelInfo)
	}
	if modelInfo.Thinking == nil {
		return nil
	}
	if config.Mode != thinking.ModeBudget && config.Mode != thinking.ModeLevel && config.Mode != thinking.ModeNone && config.Mode != thinking.ModeAuto {
		return nil
	}

	isClaude := strings.Contains(strings.ToLower(modelInfo.ID), "claude")
	if config.Mode == thinking.ModeAuto || config.Mode == thinking.ModeBudget {
		return a.applyBudgetFormatDoc(doc, config, modelInfo, isClaude)
	}

	if len(modelInfo.Thinking.Levels) > 0 {
		return a.applyLevelFormatDoc(doc, config)
	}
	return a.applyBudgetFormatDoc(doc, config, modelInfo, isClaude)
}

func (a *Applier) applyCompatibleDoc(doc *ojson.Doc, config thinking.ThinkingConfig, modelInfo *registry.ModelInfo) error {
	if config.Mode != thinking.ModeBudget && config.Mode != thinking.ModeLevel && config.Mode != thinking.ModeNone && config.Mode != thinking.ModeAuto {
		return nil
	}
	isClaude := false
	if modelInfo != nil {
		isClaude = strings.Contains(strings.ToLower(modelInfo.ID), "claude")
	}
	if config.Mode == thinking.ModeAuto {
		return a.applyBudgetFormatDoc(doc, config, modelInfo, isClaude)
	}
	if config.Mode == thinking.ModeLevel || (config.Mode == thinking.ModeNone && config.Level != "") {
		return a.applyLevelFormatDoc(doc, config)
	}
	return a.applyBudgetFormatDoc(doc, config, modelInfo, isClaude)
}

func (a *Applier) applyLevelFormatDoc(doc *ojson.Doc, config thinking.ThinkingConfig) error {
	includeThoughts, hasIncludeThoughts := extractAntigravityIncludeThoughtsDoc(doc)
	stripAntigravityNonCanonicalThinkingDoc(doc)
	_ = util.DeleteJSONDocIfExists(doc, "request.generationConfig.thinkingConfig.thinkingBudget")
	_ = util.DeleteJSONDocIfExists(doc, "request.generationConfig.thinkingConfig.thinking_budget")
	_ = util.DeleteJSONDocIfExists(doc, "request.generationConfig.thinkingConfig.thinking_level")
	_ = util.DeleteJSONDocIfExists(doc, "request.generationConfig.thinkingConfig.includeThoughts")
	_ = util.DeleteJSONDocIfExists(doc, "request.generationConfig.thinkingConfig.include_thoughts")

	if config.Mode == thinking.ModeNone {
		level := formatAntigravityThinkingLevel(config.Level)
		if config.Budget == 0 && level == "" {
			_ = util.DeleteJSONDocIfExists(doc, "request.generationConfig.thinkingConfig")
			return nil
		}
		if level != "" {
			_ = doc.Set("request.generationConfig.thinkingConfig.thinkingLevel", level)
		}
		if hasIncludeThoughts {
			_ = doc.Set("request.generationConfig.thinkingConfig.includeThoughts", includeThoughts)
		}
		return nil
	}

	if config.Mode != thinking.ModeLevel {
		return nil
	}

	_ = doc.Set("request.generationConfig.thinkingConfig.thinkingLevel", formatAntigravityThinkingLevel(config.Level))
	if hasIncludeThoughts {
		_ = doc.Set("request.generationConfig.thinkingConfig.includeThoughts", includeThoughts)
	}
	return nil
}

func formatAntigravityThinkingLevel(level thinking.ThinkingLevel) string {
	return strings.ToUpper(strings.TrimSpace(string(level)))
}

func (a *Applier) applyBudgetFormatDoc(doc *ojson.Doc, config thinking.ThinkingConfig, modelInfo *registry.ModelInfo, isClaude bool) error {
	includeThoughts, hasIncludeThoughts := extractAntigravityIncludeThoughtsDoc(doc)
	stripAntigravityNonCanonicalThinkingDoc(doc)
	_ = util.DeleteJSONDocIfExists(doc, "request.generationConfig.thinkingConfig.thinkingLevel")
	_ = util.DeleteJSONDocIfExists(doc, "request.generationConfig.thinkingConfig.thinking_level")
	_ = util.DeleteJSONDocIfExists(doc, "request.generationConfig.thinkingConfig.thinking_budget")
	_ = util.DeleteJSONDocIfExists(doc, "request.generationConfig.thinkingConfig.includeThoughts")
	_ = util.DeleteJSONDocIfExists(doc, "request.generationConfig.thinkingConfig.include_thoughts")

	budget := config.Budget
	if isClaude && modelInfo != nil {
		budget = a.normalizeClaudeBudgetDoc(budget, doc, modelInfo)
		if budget == -2 {
			return nil
		}
	}

	_ = doc.Set("request.generationConfig.thinkingConfig.thinkingBudget", budget)
	if hasIncludeThoughts {
		_ = doc.Set("request.generationConfig.thinkingConfig.includeThoughts", includeThoughts)
	}
	return nil
}

func stripAntigravityNonCanonicalThinkingDoc(doc *ojson.Doc) {
	_ = util.DeleteJSONDocIfExists(doc, "request.generationConfig.thinking_config")
	deletedSnakeGenConfig := false
	for _, path := range []string{
		"request.generation_config.thinkingConfig",
		"request.generation_config.thinking_config",
	} {
		if doc.Get(path).Exists() && util.DeleteJSONDocIfExists(doc, path) == nil {
			deletedSnakeGenConfig = true
		}
	}
	if deletedSnakeGenConfig {
		if gc := doc.Get("request.generation_config"); gc.Exists() && gc.IsObject() && len(gc.Map()) == 0 {
			_ = util.DeleteJSONDocIfExists(doc, "request.generation_config")
		}
	}
}

func extractAntigravityIncludeThoughtsDoc(doc *ojson.Doc) (bool, bool) {
	for _, path := range []string{
		"request.generationConfig.thinkingConfig.includeThoughts",
		"request.generationConfig.thinkingConfig.include_thoughts",
		"request.generationConfig.thinking_config.includeThoughts",
		"request.generationConfig.thinking_config.include_thoughts",
		"request.generation_config.thinkingConfig.includeThoughts",
		"request.generation_config.thinkingConfig.include_thoughts",
		"request.generation_config.thinking_config.includeThoughts",
		"request.generation_config.thinking_config.include_thoughts",
	} {
		switch value := doc.Get(path); value.Type {
		case ojson.True:
			return true, true
		case ojson.False:
			return false, true
		}
	}
	return false, false
}

func (a *Applier) normalizeClaudeBudgetDoc(budget int, doc *ojson.Doc, modelInfo *registry.ModelInfo) int {
	if modelInfo == nil {
		return budget
	}

	effectiveMax, setDefaultMax := a.effectiveMaxTokensDoc(doc, modelInfo)
	if effectiveMax > 0 && budget >= effectiveMax {
		budget = effectiveMax - 1
	}

	minBudget := 0
	if modelInfo.Thinking != nil {
		minBudget = modelInfo.Thinking.Min
	}
	if minBudget > 0 && budget >= 0 && budget < minBudget {
		_ = util.DeleteJSONDocIfExists(doc, "request.generationConfig.thinkingConfig")
		stripAntigravityNonCanonicalThinkingDoc(doc)
		return -2
	}

	if setDefaultMax && effectiveMax > 0 {
		_ = doc.Set("request.generationConfig.maxOutputTokens", effectiveMax)
	}

	return budget
}

func (a *Applier) effectiveMaxTokensDoc(doc *ojson.Doc, modelInfo *registry.ModelInfo) (max int, fromModel bool) {
	callerMax := 0
	for _, path := range []string{
		"request.generationConfig.maxOutputTokens",
		"request.generationConfig.max_output_tokens",
		"request.generation_config.maxOutputTokens",
		"request.generation_config.max_output_tokens",
	} {
		maxTok := doc.Get(path)
		if maxTok.Exists() && maxTok.Type == ojson.Number && maxTok.Int() > 0 {
			val := int(maxTok.Int())
			if callerMax == 0 || val < callerMax {
				callerMax = val
			}
		}
	}
	if callerMax > 0 {
		if modelInfo != nil && modelInfo.MaxCompletionTokens > 0 && callerMax > modelInfo.MaxCompletionTokens {
			return modelInfo.MaxCompletionTokens, false
		}
		return callerMax, false
	}
	if modelInfo != nil && modelInfo.MaxCompletionTokens > 0 {
		return modelInfo.MaxCompletionTokens, true
	}
	return 0, false
}
