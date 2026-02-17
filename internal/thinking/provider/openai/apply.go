// Package openai implements thinking configuration for OpenAI/Codex models.
//
// OpenAI models use the reasoning_effort format with discrete levels
// (low/medium/high). Some models support xhigh and none levels.
// See: _bmad-output/planning-artifacts/architecture.md#Epic-8
package openai

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/thinking"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// validReasoningEffortLevels contains the standard values accepted by the
// OpenAI reasoning_effort field. Provider-specific extensions (xhigh, minimal,
// auto) are NOT in this set and must be clamped before use.
var validReasoningEffortLevels = map[string]struct{}{
	"none":   {},
	"low":    {},
	"medium": {},
	"high":   {},
}

// clampReasoningEffort maps any thinking level string to a value that is safe
// to send as OpenAI reasoning_effort. Non-standard CPA-internal values are
// mapped to the nearest standard equivalent.
//
// Mapping rules:
//   - none / low / medium / high  → returned as-is (already valid)
//   - xhigh                       → "high" (nearest lower standard level)
//   - minimal                     → "low" (nearest higher standard level)
//   - auto                        → "medium" (reasonable default)
//   - anything else               → "medium" (safe default)
func clampReasoningEffort(level string) string {
	if _, ok := validReasoningEffortLevels[level]; ok {
		return level
	}
	var clamped string
	switch level {
	case string(thinking.LevelXHigh):
		clamped = string(thinking.LevelHigh)
	case string(thinking.LevelMinimal):
		clamped = string(thinking.LevelLow)
	case string(thinking.LevelAuto):
		clamped = string(thinking.LevelMedium)
	default:
		clamped = string(thinking.LevelMedium)
	}
	log.WithFields(log.Fields{
		"original": level,
		"clamped":  clamped,
	}).Debug("openai: reasoning_effort clamped to nearest valid standard value")
	return clamped
}

// Applier implements thinking.ProviderApplier for OpenAI models.
//
// OpenAI-specific behavior:
//   - Output format: reasoning_effort (string: low/medium/high/xhigh)
//   - Level-only mode: no numeric budget support
//   - Some models support ZeroAllowed (gpt-5.1, gpt-5.2)
type Applier struct{}

var _ thinking.ProviderApplier = (*Applier)(nil)

// NewApplier creates a new OpenAI thinking applier.
func NewApplier() *Applier {
	return &Applier{}
}

func init() {
	thinking.RegisterProvider("openai", NewApplier())
}

// Apply applies thinking configuration to OpenAI request body.
//
// Expected output format:
//
//	{
//	  "reasoning_effort": "high"
//	}
func (a *Applier) Apply(body []byte, config thinking.ThinkingConfig, modelInfo *registry.ModelInfo) ([]byte, error) {
	if thinking.IsUserDefinedModel(modelInfo) {
		return applyCompatibleOpenAI(body, config)
	}
	if modelInfo.Thinking == nil {
		return body, nil
	}

	// Only handle ModeLevel and ModeNone; other modes pass through unchanged.
	if config.Mode != thinking.ModeLevel && config.Mode != thinking.ModeNone {
		return body, nil
	}

	if len(body) == 0 || !gjson.ValidBytes(body) {
		body = []byte(`{}`)
	}

	if config.Mode == thinking.ModeLevel {
		result, _ := sjson.SetBytes(body, "reasoning_effort", clampReasoningEffort(string(config.Level)))
		return result, nil
	}

	effort := ""
	support := modelInfo.Thinking
	if config.Budget == 0 {
		if support.ZeroAllowed || hasLevel(support.Levels, string(thinking.LevelNone)) {
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
		return body, nil
	}

	result, _ := sjson.SetBytes(body, "reasoning_effort", clampReasoningEffort(effort))
	return result, nil
}

func applyCompatibleOpenAI(body []byte, config thinking.ThinkingConfig) ([]byte, error) {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		body = []byte(`{}`)
	}

	var effort string
	switch config.Mode {
	case thinking.ModeLevel:
		if config.Level == "" {
			return body, nil
		}
		effort = string(config.Level)
	case thinking.ModeNone:
		effort = string(thinking.LevelNone)
		if config.Level != "" {
			effort = string(config.Level)
		}
	case thinking.ModeAuto:
		// Auto mode for user-defined models: pass through as "auto"
		effort = string(thinking.LevelAuto)
	case thinking.ModeBudget:
		// Budget mode: convert budget to level using threshold mapping
		level, ok := thinking.ConvertBudgetToLevel(config.Budget)
		if !ok {
			return body, nil
		}
		effort = level
	default:
		return body, nil
	}

	result, _ := sjson.SetBytes(body, "reasoning_effort", clampReasoningEffort(effort))
	return result, nil
}

func hasLevel(levels []string, target string) bool {
	for _, level := range levels {
		if strings.EqualFold(strings.TrimSpace(level), target) {
			return true
		}
	}
	return false
}
