// Package qoder applies canonical thinking configuration to Qoder chat requests.
package qoder

import (
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/thinking"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Applier writes Qoder's effort input; the executor moves it into parameters.
type Applier struct{}

var _ thinking.ProviderApplier = (*Applier)(nil)

func init() {
	applier := &Applier{}
	thinking.RegisterProvider("qoder", applier)
	thinking.RegisterProvider("qoder-cn", applier)
}

func (a *Applier) Apply(body []byte, config thinking.ThinkingConfig, modelInfo *registry.ModelInfo) ([]byte, error) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		body = []byte(`{}`)
	}
	if modelInfo == nil || (!thinking.IsUserDefinedModel(modelInfo) && modelInfo.Thinking == nil) {
		return body, nil
	}
	var effort string
	switch config.Mode {
	case thinking.ModeLevel:
		if config.Level == "" {
			return body, nil
		}
		effort = string(config.Level)
	case thinking.ModeNone:
		if config.Level != "" && config.Level != thinking.LevelNone {
			effort = string(config.Level)
		} else {
			effort = string(thinking.LevelNone)
		}
	case thinking.ModeBudget:
		level, ok := thinking.ConvertBudgetToLevel(config.Budget)
		if !ok {
			return body, nil
		}
		effort = level
	default:
		return body, nil
	}
	return sjson.SetBytes(body, "reasoning_effort", effort)
}
