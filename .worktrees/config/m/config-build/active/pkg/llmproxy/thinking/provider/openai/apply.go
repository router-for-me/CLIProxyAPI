// Package openai implements thinking configuration for OpenAI/Codex models.
//
// OpenAI models use the reasoning_effort format with discrete levels
// (low/medium/high). Some models support xhigh and none levels.
// See: _bmad-output/planning-artifacts/architecture.md#Epic-8
package openai

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/thinking"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// validReasoningEffortLevels contains the standard values accepted by the
// OpenAI reasoning_effort field. Provider-specific extensions (minimal, xhigh,
// auto) are normalized to standard equivalents when the model does not support
// them.
var validReasoningEffortLevels = map[string]struct{}{
	"none":   {},
	"low":    {},
	"medium": {},
	"high":   {},
	"xhigh":  {},
}

// clampReasoningEffort maps any thinking level string to a value that is safe
// to send as OpenAI reasoning_effort. Non-standard CPA-internal values are
// mapped to the nearest supported equivalent for the target model.
//
// Mapping rules:
//   - none / low / medium / high  → returned as-is (already valid)
//   - xhigh                       → "high" (nearest lower standard level)
//   - minimal                     → "low" (nearest higher standard level)
//   - auto                        → "medium" (reasonable default)
//   - anything else               → "medium" (safe default)
func clampReasoningEffort(level string, support *registry.ThinkingSupport) string {
	raw := strings.ToLower(strings.TrimSpace(level))
	if raw == "" {
		return raw
	}
	if hasLevel(support.Levels, raw) {
		return raw
	}

	if _, ok := validReasoningEffortLevels[raw]; !ok {
		log.WithFields(log.Fields{
			"original": level,
			"clamped":  string(thinking.LevelMedium),
		}).Debug("openai: reasoning_effort clamped to default level")
		return string(thinking.LevelMedium)
	}

	// Normalize non-standard inputs when not explicitly supported by model.
	if support == nil || len(support.Levels) == 0 {
		switch raw {
		case string(thinking.LevelXHigh):
			return string(thinking.LevelHigh)
		case string(thinking.LevelMinimal):
			return string(thinking.LevelLow)
		case string(thinking.LevelAuto):
			return string(thinking.LevelMedium)
		}
		return raw
	}

	if hasLevel(support.Levels, string(thinking.LevelXHigh)) && raw == string(thinking.LevelXHigh) {
		return raw
	}

	// If the provider supports minimal levels, preserve them.
	if raw == string(thinking.LevelMinimal) && hasLevel(support.Levels, string(thinking.LevelMinimal)) {
		return level
	}

	// Model does not support provider-specific levels; map to nearest supported standard
	// level for compatibility.
	switch raw {
	case string(thinking.LevelXHigh):
		if hasLevel(support.Levels, string(thinking.LevelHigh)) {
			return string(thinking.LevelHigh)
		}
	case string(thinking.LevelMinimal):
		if hasLevel(support.Levels, string(thinking.LevelLow)) {
			return string(thinking.LevelLow)
		}
	case string(thinking.LevelAuto):
		return string(thinking.LevelMedium)
	default:
		break
	}

	// Fall back to the provided level only when model support is not constrained.
	if _, ok := validReasoningEffortLevels[raw]; ok {
		return raw
	}
	return string(thinking.LevelMedium)
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
		result, _ := sjson.SetBytes(body, "reasoning_effort", clampReasoningEffort(string(config.Level), modelInfo.Thinking))
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

	result, _ := sjson.SetBytes(body, "reasoning_effort", clampReasoningEffort(effort, support))
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

	result, _ := sjson.SetBytes(body, "reasoning_effort", effort)
	return result, nil
}

func hasLevel(levels []string, target string) bool {
	for _, level := range levels {
		if strings.EqualFold(strings.TrimSpace(level), strings.TrimSpace(target)) {
			return true
		}
	}
	return false
}
