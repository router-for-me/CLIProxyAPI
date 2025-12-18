package executor

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// ApplyThinkingMetadata applies thinking config from model suffix metadata (e.g., (high), (8192))
// for standard Gemini format payloads. It normalizes the budget when the model supports thinking.
func ApplyThinkingMetadata(payload []byte, metadata map[string]any, model string) []byte {
	budgetOverride, includeOverride, ok := util.ResolveThinkingConfigFromMetadata(model, metadata)
	if !ok || (budgetOverride == nil && includeOverride == nil) {
		return payload
	}
	if !util.ModelSupportsThinking(model) {
		return payload
	}
	if budgetOverride != nil {
		norm := util.NormalizeThinkingBudget(model, *budgetOverride)
		budgetOverride = &norm
	}
	return util.ApplyGeminiThinkingConfig(payload, budgetOverride, includeOverride)
}

// applyThinkingMetadataCLI applies thinking config from model suffix metadata (e.g., (high), (8192))
// for Gemini CLI format payloads (nested under "request"). It normalizes the budget when the model supports thinking.
func applyThinkingMetadataCLI(payload []byte, metadata map[string]any, model string) []byte {
	budgetOverride, includeOverride, ok := util.ResolveThinkingConfigFromMetadata(model, metadata)
	if !ok || (budgetOverride == nil && includeOverride == nil) {
		return payload
	}
	if !util.ModelSupportsThinking(model) {
		return payload
	}
	if budgetOverride != nil {
		norm := util.NormalizeThinkingBudget(model, *budgetOverride)
		budgetOverride = &norm
	}
	return util.ApplyGeminiCLIThinkingConfig(payload, budgetOverride, includeOverride)
}

// ApplyReasoningEffortMetadata applies reasoning effort overrides from metadata to the given JSON path.
// Metadata values take precedence over any existing field when the model supports thinking, intentionally
// overwriting caller-provided values to honor suffix/default metadata priority.
func ApplyReasoningEffortMetadata(payload []byte, metadata map[string]any, model, field string, allowCompat bool) []byte {
	if len(metadata) == 0 {
		return payload
	}
	if field == "" {
		return payload
	}
	baseModel := util.ResolveOriginalModel(model, metadata)
	if baseModel == "" {
		baseModel = model
	}
	if !util.ModelSupportsThinking(baseModel) && !allowCompat {
		return payload
	}
	if effort, ok := util.ReasoningEffortFromMetadata(metadata); ok && effort != "" {
		if util.ModelUsesThinkingLevels(baseModel) || allowCompat {
			if updated, err := sjson.SetBytes(payload, field, effort); err == nil {
				return updated
			}
		}
	}
	// Fallback: numeric thinking_budget suffix for level-based (OpenAI-style) models.
	if util.ModelUsesThinkingLevels(baseModel) || allowCompat {
		if budget, _, _, matched := util.ThinkingFromMetadata(metadata); matched && budget != nil {
			if effort, ok := util.ThinkingBudgetToEffort(baseModel, *budget); ok && effort != "" {
				if updated, err := sjson.SetBytes(payload, field, effort); err == nil {
					return updated
				}
			}
		}
	}
	return payload
}

// applyPayloadConfig applies payload default and override rules from configuration
// to the given JSON payload for the specified model.
// Defaults only fill missing fields, while overrides always overwrite existing values.
func applyPayloadConfig(cfg *config.Config, model string, payload []byte) []byte {
	return applyPayloadConfigWithRoot(cfg, model, "", "", payload)
}

// applyPayloadConfigWithRoot behaves like applyPayloadConfig but treats all parameter
// paths as relative to the provided root path (for example, "request" for Gemini CLI)
// and restricts matches to the given protocol when supplied.
func applyPayloadConfigWithRoot(cfg *config.Config, model, protocol, root string, payload []byte) []byte {
	if cfg == nil || len(payload) == 0 {
		return payload
	}
	rules := cfg.Payload
	if len(rules.Default) == 0 && len(rules.Override) == 0 {
		return payload
	}
	model = strings.TrimSpace(model)
	if model == "" {
		return payload
	}
	out := payload
	// Apply default rules: first write wins per field across all matching rules.
	for i := range rules.Default {
		rule := &rules.Default[i]
		if !payloadRuleMatchesModel(rule, model, protocol) {
			continue
		}
		for path, value := range rule.Params {
			fullPath := buildPayloadPath(root, path)
			if fullPath == "" {
				continue
			}
			if gjson.GetBytes(out, fullPath).Exists() {
				continue
			}
			updated, errSet := sjson.SetBytes(out, fullPath, value)
			if errSet != nil {
				continue
			}
			out = updated
		}
	}
	// Apply override rules: last write wins per field across all matching rules.
	for i := range rules.Override {
		rule := &rules.Override[i]
		if !payloadRuleMatchesModel(rule, model, protocol) {
			continue
		}
		for path, value := range rule.Params {
			fullPath := buildPayloadPath(root, path)
			if fullPath == "" {
				continue
			}
			updated, errSet := sjson.SetBytes(out, fullPath, value)
			if errSet != nil {
				continue
			}
			out = updated
		}
	}
	return out
}

func payloadRuleMatchesModel(rule *config.PayloadRule, model, protocol string) bool {
	if rule == nil {
		return false
	}
	if len(rule.Models) == 0 {
		return false
	}
	for _, entry := range rule.Models {
		name := strings.TrimSpace(entry.Name)
		if name == "" {
			continue
		}
		if ep := strings.TrimSpace(entry.Protocol); ep != "" && protocol != "" && !strings.EqualFold(ep, protocol) {
			continue
		}
		if matchModelPattern(name, model) {
			return true
		}
	}
	return false
}

// buildPayloadPath combines an optional root path with a relative parameter path.
// When root is empty, the parameter path is used as-is. When root is non-empty,
// the parameter path is treated as relative to root.
func buildPayloadPath(root, path string) string {
	r := strings.TrimSpace(root)
	p := strings.TrimSpace(path)
	if r == "" {
		return p
	}
	if p == "" {
		return r
	}
	if strings.HasPrefix(p, ".") {
		p = p[1:]
	}
	return r + "." + p
}

// matchModelPattern performs simple wildcard matching where '*' matches zero or more characters.
// Examples:
//
//	"*-5" matches "gpt-5"
//	"gpt-*" matches "gpt-5" and "gpt-4"
//	"gemini-*-pro" matches "gemini-2.5-pro" and "gemini-3-pro".
func matchModelPattern(pattern, model string) bool {
	pattern = strings.TrimSpace(pattern)
	model = strings.TrimSpace(model)
	if pattern == "" {
		return false
	}
	if pattern == "*" {
		return true
	}
	// Iterative glob-style matcher supporting only '*' wildcard.
	pi, si := 0, 0
	starIdx := -1
	matchIdx := 0
	for si < len(model) {
		if pi < len(pattern) && (pattern[pi] == model[si]) {
			pi++
			si++
			continue
		}
		if pi < len(pattern) && pattern[pi] == '*' {
			starIdx = pi
			matchIdx = si
			pi++
			continue
		}
		if starIdx != -1 {
			pi = starIdx + 1
			matchIdx++
			si = matchIdx
			continue
		}
		return false
	}
	for pi < len(pattern) && pattern[pi] == '*' {
		pi++
	}
	return pi == len(pattern)
}

// NormalizeThinkingConfig normalizes thinking-related fields in the payload
// based on model capabilities. For models without thinking support, it strips
// reasoning fields. For models with level-based thinking, it validates and
// normalizes the reasoning effort level. For models with numeric budget thinking,
// it strips the effort string fields.
func NormalizeThinkingConfig(payload []byte, model string, allowCompat bool) []byte {
	if len(payload) == 0 || model == "" {
		return payload
	}

	if !util.ModelSupportsThinking(model) {
		if allowCompat {
			return payload
		}
		return StripThinkingFields(payload, false)
	}

	if util.ModelUsesThinkingLevels(model) {
		return NormalizeReasoningEffortLevel(payload, model)
	}

	// Model supports thinking but uses numeric budgets, not levels.
	// Strip effort string fields since they are not applicable.
	return StripThinkingFields(payload, true)
}

// StripThinkingFields removes thinking-related fields from the payload for
// models that do not support thinking. If effortOnly is true, only removes
// effort string fields (for models using numeric budgets).
func StripThinkingFields(payload []byte, effortOnly bool) []byte {
	fieldsToRemove := []string{
		"reasoning_effort",
		"reasoning.effort",
	}
	if !effortOnly {
		fieldsToRemove = append([]string{"reasoning", "thinking"}, fieldsToRemove...)
	}
	out := payload
	for _, field := range fieldsToRemove {
		if gjson.GetBytes(out, field).Exists() {
			out, _ = sjson.DeleteBytes(out, field)
		}
	}
	return out
}

// NormalizeReasoningEffortLevel validates and normalizes the reasoning_effort
// or reasoning.effort field for level-based thinking models.
func NormalizeReasoningEffortLevel(payload []byte, model string) []byte {
	out := payload

	if effort := gjson.GetBytes(out, "reasoning_effort"); effort.Exists() {
		if normalized, ok := util.NormalizeReasoningEffortLevel(model, effort.String()); ok {
			out, _ = sjson.SetBytes(out, "reasoning_effort", normalized)
		}
	}

	if effort := gjson.GetBytes(out, "reasoning.effort"); effort.Exists() {
		if normalized, ok := util.NormalizeReasoningEffortLevel(model, effort.String()); ok {
			out, _ = sjson.SetBytes(out, "reasoning.effort", normalized)
		}
	}

	return out
}

// ValidateThinkingConfig checks for unsupported reasoning levels on level-based models.
// Returns a statusErr with 400 when an unsupported level is supplied to avoid silently
// downgrading requests.
func ValidateThinkingConfig(payload []byte, model string) error {
	if len(payload) == 0 || model == "" {
		return nil
	}
	if !util.ModelSupportsThinking(model) || !util.ModelUsesThinkingLevels(model) {
		return nil
	}

	levels := util.GetModelThinkingLevels(model)
	checkField := func(path string) error {
		if effort := gjson.GetBytes(payload, path); effort.Exists() {
			if _, ok := util.NormalizeReasoningEffortLevel(model, effort.String()); !ok {
				return statusErr{
					code: http.StatusBadRequest,
					msg:  fmt.Sprintf("unsupported reasoning effort level %q for model %s (supported: %s)", effort.String(), model, strings.Join(levels, ", ")),
				}
			}
		}
		return nil
	}

	if err := checkField("reasoning_effort"); err != nil {
		return err
	}
	if err := checkField("reasoning.effort"); err != nil {
		return err
	}
	return nil
}
