// Package kimi implements thinking configuration for Kimi (Moonshot AI) models.
package kimi

import (
	"fmt"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

// Applier implements thinking.ProviderApplier for Kimi models.
type Applier struct{}

var _ thinking.ProviderDocApplier = (*Applier)(nil)

// NewApplier creates a new Kimi thinking applier.
func NewApplier() *Applier {
	return &Applier{}
}

func init() {
	applier := NewApplier()
	thinking.RegisterProvider("kimi", applier)
	thinking.RegisterProvider("kimi-ai", applier)
	thinking.RegisterProvider("kimi.ai", applier)
	thinking.RegisterProvider("kimi.com", applier)
}

// Apply applies thinking configuration to Kimi request body.
func (a *Applier) Apply(body []byte, config thinking.ThinkingConfig, modelInfo *registry.ModelInfo) ([]byte, error) {
	if !thinking.IsUserDefinedModel(modelInfo) && modelInfo.Thinking == nil {
		return body, nil
	}
	effort, disable, ok := resolveKimiAction(config, modelInfo)
	if !ok {
		return body, nil
	}
	if len(body) == 0 || !ojson.ValidBytes(body) {
		body = []byte("{}")
	}
	doc := ojson.NewDoc(body)
	var err error
	if disable {
		err = applyDisabledThinkingDoc(doc)
	} else {
		err = applyEnabledThinkingDoc(doc, effort)
	}
	if err != nil {
		return body, err
	}
	return doc.Bytes(), nil
}

// ApplyDoc applies thinking configuration onto doc in place.
func (a *Applier) ApplyDoc(doc *ojson.Doc, config thinking.ThinkingConfig, modelInfo *registry.ModelInfo) error {
	if doc == nil {
		return nil
	}
	if !thinking.IsUserDefinedModel(modelInfo) && modelInfo.Thinking == nil {
		return nil
	}
	effort, disable, ok := resolveKimiAction(config, modelInfo)
	if !ok {
		return nil
	}
	if disable {
		return applyDisabledThinkingDoc(doc)
	}
	return applyEnabledThinkingDoc(doc, effort)
}

func resolveKimiAction(config thinking.ThinkingConfig, modelInfo *registry.ModelInfo) (effort string, disable bool, ok bool) {
	if thinking.IsUserDefinedModel(modelInfo) {
		switch config.Mode {
		case thinking.ModeLevel:
			if config.Level == "" {
				return "", false, false
			}
			return string(config.Level), false, true
		case thinking.ModeNone:
			if config.Level == "" || config.Level == thinking.LevelNone {
				return "", true, true
			}
			return string(config.Level), false, true
		case thinking.ModeAuto:
			return string(thinking.LevelAuto), false, true
		case thinking.ModeBudget:
			level, okConvert := thinking.ConvertBudgetToLevel(config.Budget)
			if !okConvert {
				return "", false, false
			}
			return level, false, true
		default:
			return "", false, false
		}
	}

	switch config.Mode {
	case thinking.ModeLevel:
		if config.Level == "" {
			return "", false, false
		}
		return string(config.Level), false, true
	case thinking.ModeNone:
		if config.Level != "" && config.Level != thinking.LevelNone {
			return string(config.Level), false, true
		}
		return "", true, true
	case thinking.ModeBudget:
		level, okConvert := thinking.ConvertBudgetToLevel(config.Budget)
		if !okConvert {
			return "", false, false
		}
		return level, false, true
	case thinking.ModeAuto:
		return string(thinking.LevelAuto), false, true
	default:
		return "", false, false
	}
}

func applyEnabledThinkingDoc(doc *ojson.Doc, effort string) error {
	if errDeleteLegacyEffort := util.DeleteJSONDocIfExists(doc, "reasoning_effort"); errDeleteLegacyEffort != nil {
		return fmt.Errorf("kimi thinking: failed to clear reasoning_effort: %w", errDeleteLegacyEffort)
	}
	if errSetType := doc.Set("thinking.type", "enabled"); errSetType != nil {
		return fmt.Errorf("kimi thinking: failed to set thinking.type: %w", errSetType)
	}
	if errSetEffort := doc.Set("thinking.effort", effort); errSetEffort != nil {
		return fmt.Errorf("kimi thinking: failed to set thinking.effort: %w", errSetEffort)
	}
	return nil
}

func applyDisabledThinkingDoc(doc *ojson.Doc) error {
	if errDeleteThinking := util.DeleteJSONDocIfExists(doc, "thinking"); errDeleteThinking != nil {
		return fmt.Errorf("kimi thinking: failed to clear thinking object: %w", errDeleteThinking)
	}
	if errDeleteEffort := util.DeleteJSONDocIfExists(doc, "reasoning_effort"); errDeleteEffort != nil {
		return fmt.Errorf("kimi thinking: failed to clear reasoning_effort: %w", errDeleteEffort)
	}
	if errSetType := doc.Set("thinking.type", "disabled"); errSetType != nil {
		return fmt.Errorf("kimi thinking: failed to set thinking.type: %w", errSetType)
	}
	return nil
}
