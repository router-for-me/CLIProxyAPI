// Package cursor translates canonical thinking configuration into an executor-only marker.
package cursor

import (
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// Applier stores the normalized effort for CursorExecutor to resolve to a discovered model variant.
type Applier struct{}

var _ thinking.ProviderApplier = (*Applier)(nil)

func init() { thinking.RegisterProvider("cursor", &Applier{}) }

func (a *Applier) Apply(body []byte, config thinking.ThinkingConfig, _ *registry.ModelInfo) ([]byte, error) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		body = []byte(`{}`)
	}
	effort := ""
	switch config.Mode {
	case thinking.ModeLevel:
		effort = string(config.Level)
	case thinking.ModeNone:
		effort = string(thinking.LevelNone)
		if config.Level != "" {
			effort = string(config.Level)
		}
	case thinking.ModeAuto:
		effort = string(thinking.LevelAuto)
	case thinking.ModeBudget:
		if level, ok := thinking.ConvertBudgetToLevel(config.Budget); ok {
			effort = level
		}
	}
	if effort == "" {
		return body, nil
	}
	result, errSet := sjson.SetBytes(body, "cursor.reasoning_effort", effort)
	if errSet != nil {
		return body, errSet
	}
	return result, nil
}
