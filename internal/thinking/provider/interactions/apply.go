// Package interactions applies native Interactions thinking configuration.
package interactions

import (
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

// Applier implements thinking.ProviderApplier for the native Interactions API.
type Applier struct{}

var _ thinking.ProviderDocApplier = (*Applier)(nil)

// NewApplier creates a new Interactions thinking applier.
func NewApplier() *Applier {
	return &Applier{}
}

func init() {
	thinking.RegisterProvider("interactions", NewApplier())
}

// Apply writes thinking configuration using native Interactions generation_config fields.
func (a *Applier) Apply(body []byte, config thinking.ThinkingConfig, modelInfo *registry.ModelInfo) ([]byte, error) {
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

// ApplyDoc writes thinking configuration onto doc in place.
func (a *Applier) ApplyDoc(doc *ojson.Doc, config thinking.ThinkingConfig, modelInfo *registry.ModelInfo) error {
	if doc == nil {
		return nil
	}
	if config.Mode != thinking.ModeBudget && config.Mode != thinking.ModeLevel && config.Mode != thinking.ModeNone && config.Mode != thinking.ModeAuto {
		return nil
	}

	summaries, hasSummaries := originalInteractionsThinkingSummariesDoc(doc)
	includeThoughts, hasIncludeThoughts := originalInteractionsIncludeThoughtsDoc(doc)
	stripInteractionsThinkingFieldsDoc(doc)

	switch config.Mode {
	case thinking.ModeLevel:
		applyInteractionsLevelDoc(doc, string(config.Level), modelInfo, summaries, hasSummaries, includeThoughts, hasIncludeThoughts)
	case thinking.ModeBudget:
		applyInteractionsBudgetDoc(doc, config.Budget, modelInfo, summaries, hasSummaries, includeThoughts, hasIncludeThoughts)
	case thinking.ModeAuto:
		restoreInteractionsThinkingSummariesDoc(doc, summaries, hasSummaries, includeThoughts, hasIncludeThoughts)
	case thinking.ModeNone:
		applyInteractionsNoneDoc(doc, config, modelInfo, summaries, hasSummaries, includeThoughts, hasIncludeThoughts)
	}
	return nil
}

func applyInteractionsBudgetDoc(doc *ojson.Doc, budget int, modelInfo *registry.ModelInfo, summaries string, hasSummaries bool, includeThoughts, hasIncludeThoughts bool) {
	level, ok := thinking.ConvertBudgetToLevel(budget)
	if !ok {
		restoreInteractionsThinkingSummariesDoc(doc, summaries, hasSummaries, includeThoughts, hasIncludeThoughts)
		return
	}
	switch level {
	case string(thinking.LevelNone), string(thinking.LevelAuto):
		restoreInteractionsThinkingSummariesDoc(doc, summaries, hasSummaries, includeThoughts, hasIncludeThoughts)
	default:
		applyInteractionsLevelDoc(doc, level, modelInfo, summaries, hasSummaries, includeThoughts, hasIncludeThoughts)
	}
}

func applyInteractionsLevelDoc(doc *ojson.Doc, level string, modelInfo *registry.ModelInfo, summaries string, hasSummaries bool, includeThoughts, hasIncludeThoughts bool) {
	level = normalizeInteractionsLevel(level, modelInfo)
	if level != "" {
		_ = doc.Set("generation_config.thinking_level", level)
	}
	restoreInteractionsThinkingSummariesDoc(doc, summaries, hasSummaries, includeThoughts, hasIncludeThoughts)
}

func applyInteractionsNoneDoc(doc *ojson.Doc, config thinking.ThinkingConfig, modelInfo *registry.ModelInfo, summaries string, hasSummaries bool, includeThoughts, hasIncludeThoughts bool) {
	if config.Level != "" {
		applyInteractionsLevelDoc(doc, string(config.Level), modelInfo, summaries, hasSummaries, includeThoughts, hasIncludeThoughts)
		return
	}
	if config.Budget > 0 {
		applyInteractionsBudgetDoc(doc, config.Budget, modelInfo, summaries, hasSummaries, includeThoughts, hasIncludeThoughts)
		return
	}
}

func stripInteractionsThinkingFieldsDoc(doc *ojson.Doc) {
	for _, path := range []string{
		"generation_config.thinking_level",
		"generation_config.thinkingLevel",
		"generation_config.thinking_budget",
		"generation_config.thinkingBudget",
		"generation_config.thinking_summaries",
		"generation_config.thinkingSummaries",
		"generation_config.thinking_config",
		"generation_config.thinkingConfig",
		"generationConfig.thinkingLevel",
		"generationConfig.thinking_level",
		"generationConfig.thinkingBudget",
		"generationConfig.thinking_budget",
		"generationConfig.thinkingSummaries",
		"generationConfig.thinking_summaries",
		"generationConfig.thinkingConfig",
	} {
		_ = util.DeleteJSONDocIfExists(doc, path)
	}
}

func restoreInteractionsThinkingSummariesDoc(doc *ojson.Doc, summaries string, hasSummaries bool, includeThoughts, hasIncludeThoughts bool) {
	if hasSummaries {
		_ = doc.Set("generation_config.thinking_summaries", summaries)
		return
	}
	if hasIncludeThoughts {
		value := "none"
		if includeThoughts {
			value = "auto"
		}
		_ = doc.Set("generation_config.thinking_summaries", value)
	}
}

func originalInteractionsThinkingSummariesDoc(doc *ojson.Doc) (string, bool) {
	for _, path := range []string{
		"generation_config.thinking_summaries",
		"generation_config.thinkingSummaries",
	} {
		value := doc.Get(path)
		if value.Type != ojson.String {
			continue
		}
		switch normalized := strings.ToLower(strings.TrimSpace(value.String())); normalized {
		case "auto", "none":
			return normalized, true
		}
	}
	return "", false
}

func originalInteractionsIncludeThoughtsDoc(doc *ojson.Doc) (bool, bool) {
	for _, path := range []string{
		"generation_config.thinking_config.include_thoughts",
		"generation_config.thinking_config.includeThoughts",
		"generation_config.thinkingConfig.include_thoughts",
		"generation_config.thinkingConfig.includeThoughts",
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

func normalizeInteractionsLevel(level string, modelInfo *registry.ModelInfo) string {
	level = strings.ToLower(strings.TrimSpace(level))
	if level == "" || level == string(thinking.LevelNone) || level == string(thinking.LevelAuto) {
		return ""
	}
	if modelInfo != nil && modelInfo.Thinking != nil && len(modelInfo.Thinking.Levels) > 0 {
		for _, candidate := range modelInfo.Thinking.Levels {
			if strings.EqualFold(candidate, level) {
				return strings.ToLower(candidate)
			}
		}
		return strings.ToLower(modelInfo.Thinking.Levels[len(modelInfo.Thinking.Levels)-1])
	}
	switch level {
	case string(thinking.LevelMax), string(thinking.LevelXHigh):
		return string(thinking.LevelHigh)
	default:
		return level
	}
}
