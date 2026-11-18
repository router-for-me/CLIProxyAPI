// Package openai implements thinking configuration for OpenAI/Codex models.
package openai

import (
	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
)

// Applier implements thinking.ProviderApplier for OpenAI models.
type Applier struct{}

var _ thinking.ProviderDocApplier = (*Applier)(nil)

// NewApplier creates a new OpenAI thinking applier.
func NewApplier() *Applier {
	return &Applier{}
}

func init() {
	thinking.RegisterProvider("openai", NewApplier())
}

// Apply applies thinking configuration to OpenAI request body.
func (a *Applier) Apply(body []byte, config thinking.ThinkingConfig, modelInfo *registry.ModelInfo) ([]byte, error) {
	effort, ok := resolveOpenAIEffort(config, modelInfo)
	if !ok {
		return body, nil
	}
	if len(body) == 0 || !ojson.ValidBytes(body) {
		body = []byte("{}")
	}
	result, _ := ojson.SetBytes(body, "reasoning_effort", effort)
	return result, nil
}

// ApplyDoc applies thinking configuration onto doc in place.
func (a *Applier) ApplyDoc(doc *ojson.Doc, config thinking.ThinkingConfig, modelInfo *registry.ModelInfo) error {
	if doc == nil {
		return nil
	}
	effort, ok := resolveOpenAIEffort(config, modelInfo)
	if !ok {
		return nil
	}
	_ = doc.Set("reasoning_effort", effort)
	return nil
}

func resolveOpenAIEffort(config thinking.ThinkingConfig, modelInfo *registry.ModelInfo) (string, bool) {
	if thinking.IsUserDefinedModel(modelInfo) {
		switch config.Mode {
		case thinking.ModeLevel:
			if config.Level == "" {
				return "", false
			}
			return string(config.Level), true
		case thinking.ModeNone:
			effort := string(thinking.LevelNone)
			if config.Level != "" {
				effort = string(config.Level)
			}
			return effort, true
		case thinking.ModeAuto:
			return string(thinking.LevelAuto), true
		case thinking.ModeBudget:
			level, ok := thinking.ConvertBudgetToLevel(config.Budget)
			if !ok {
				return "", false
			}
			return level, true
		default:
			return "", false
		}
	}
	if modelInfo.Thinking == nil {
		return "", false
	}
	if config.Mode != thinking.ModeLevel && config.Mode != thinking.ModeNone {
		return "", false
	}
	if config.Mode == thinking.ModeLevel {
		return string(config.Level), true
	}

	effort := ""
	support := modelInfo.Thinking
	if config.Budget == 0 {
		if support.ZeroAllowed || thinking.HasLevel(support.Levels, string(thinking.LevelNone)) {
			effort = string(thinking.LevelNone)
		}
	}
	if effort == "" && config.Level != "" {
		effort = string(config.Level)
	}
	if effort == "" && len(support.Levels) > 0 {
		effort = support.Levels[0]
	}
	if effort == "" {
		return "", false
	}
	return effort, true
}
