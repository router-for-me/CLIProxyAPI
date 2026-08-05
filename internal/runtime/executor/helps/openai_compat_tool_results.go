package helps

import (
	"fmt"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/thinking"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

const openAIToolResultImageOmittedText = "[image omitted: unsupported by upstream]"

// ShouldNormalizeOpenAIToolResultsForModel reports whether the selected model
// explicitly excludes image input through its input-modalities configuration.
func ShouldNormalizeOpenAIToolResultsForModel(compat *config.OpenAICompatibility, upstreamModel, requestedModel string) bool {
	if compat == nil {
		return false
	}

	if normalize, matched := openAICompatibilityModelExcludesImages(compat.Models, upstreamModel); matched {
		return normalize
	}
	normalize, _ := openAICompatibilityModelExcludesImages(compat.Models, requestedModel)
	return normalize
}

// NormalizeOpenAIToolResultsTextOnly converts tool message content to strings.
// Text parts are preserved and image parts are replaced with a short marker.
func NormalizeOpenAIToolResultsTextOnly(payload []byte) []byte {
	messages := gjson.GetBytes(payload, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return payload
	}

	out := payload
	messageIndex := 0
	messages.ForEach(func(_, message gjson.Result) bool {
		if message.Get("role").String() == "tool" {
			content := message.Get("content")
			if content.Exists() && content.Type != gjson.String {
				path := fmt.Sprintf("messages.%d.content", messageIndex)
				if updated, errSet := sjson.SetBytes(out, path, flattenOpenAIToolResultContent(content)); errSet == nil {
					out = updated
				}
			}
		}
		messageIndex++
		return true
	})
	return out
}

// ShouldEnsureOpenAICompatReasoningContent reports whether the target model
// or request requires reasoning_content on assistant tool_calls (e.g., DeepSeek/Kimi reasoning models).
//
// When thinking is explicitly disabled in the effective translated payload
// (reasoning_effort:"none", Kimi thinking.type:"disabled", or a "(none)"/"(0)"
// suffix on a model that actually supports disabling), fallback
// reasoning_content is never injected: the request is no longer in thinking
// mode, so the upstream no longer requires reasoning_content on prior tool_calls.
// A "(none)"/"(0)" suffix on a cannot-disable model is clamped back to the
// lowest supported level by the canonical thinking pipeline, so it is NOT
// treated as disabled here; the effective reasoning_effort in the payload
// decides.
func ShouldEnsureOpenAICompatReasoningContent(upstreamModel, requestedModel string, payload []byte) bool {
	if openAICompatReasoningExplicitlyDisabled(upstreamModel, requestedModel, payload) {
		return false
	}

	for _, model := range []string{upstreamModel, requestedModel} {
		if strings.TrimSpace(model) == "" {
			continue
		}
		parsed := thinking.ParseSuffix(model)
		// Only a suffix that parses to a valid thinking config (level, budget,
		// or auto) signals reasoning intent. An arbitrary "(foo)" suffix is
		// treated as no thinking config by the canonical ApplyRequestThinking
		// pipeline (parseSuffixToConfig returns an empty config), so it must
		// not enable fallback reasoning_content injection here. Disabled
		// suffixes ("(none)"/"(0)") are already filtered by the disabled guard.
		if openAICompatSuffixEnablesThinking(parsed) {
			return true
		}
		lowerName := strings.ToLower(parsed.ModelName)
		if strings.Contains(lowerName, "reasoner") ||
			strings.Contains(lowerName, "reasoning") ||
			strings.Contains(lowerName, "deepseek") ||
			strings.Contains(lowerName, "kimi") ||
			strings.Contains(lowerName, "thinking") {
			return true
		}
	}

	if effort := gjson.GetBytes(payload, "reasoning_effort"); effort.Exists() && !isOpenAICompatDisabledEffortValue(effort) {
		return true
	}
	if thinkingField := gjson.GetBytes(payload, "thinking"); thinkingField.Exists() && !isOpenAICompatThinkingObjectDisabled(thinkingField) {
		return true
	}

	return false
}

// openAICompatReasoningExplicitlyDisabled reports whether the request or model
// suffix explicitly disables thinking. This gates fallback reasoning_content
// injection so that a reasoning-capable model with thinking disabled is not
// mutated once the canonical thinking pipeline has disabled reasoning.
//
// The translated payload is authoritative: ApplyRequestThinking runs before
// this guard and has already normalized suffix/body thinking config through
// ValidateConfig. A cannot-disable model requested with a "(none)"/"(0)"
// suffix is clamped back to its lowest supported level (e.g.
// reasoning_effort:"low"), so checking the raw suffix first would wrongly
// treat thinking as disabled while the effective request is still in thinking
// mode and still needs reasoning_content on prior tool_calls (DeepSeek/Kimi
// replay 400). The raw suffix is consulted only as a fallback when the
// payload carries no explicit thinking signal.
func openAICompatReasoningExplicitlyDisabled(upstreamModel, requestedModel string, payload []byte) bool {
	// Native Kimi thinking object takes precedence over legacy reasoning_effort.
	// A payload override may set thinking.type:"disabled" after the OpenAI
	// applier left a reasoning_effort, so the native directive must win.
	if thinkingField := gjson.GetBytes(payload, "thinking"); thinkingField.Exists() {
		if isOpenAICompatThinkingObjectDisabled(thinkingField) {
			return true
		}
		// thinking.type:"enabled" overrides reasoning_effort:"none" (native
		// field has higher precedence, matching extractKimiConfig).
		if strings.EqualFold(strings.TrimSpace(thinkingField.Get("type").String()), "enabled") {
			return false
		}
	}
	if effort := gjson.GetBytes(payload, "reasoning_effort"); effort.Exists() {
		return isOpenAICompatDisabledEffortValue(effort)
	}

	for _, model := range []string{upstreamModel, requestedModel} {
		if strings.TrimSpace(model) == "" {
			continue
		}
		if openAICompatSuffixDisablesThinking(thinking.ParseSuffix(model)) {
			return true
		}
	}

	return false
}

// openAICompatSuffixDisablesThinking reports whether a thinking suffix
// explicitly disables thinking, e.g. "(none)" or budget "(0)".
func openAICompatSuffixDisablesThinking(suffix thinking.SuffixResult) bool {
	if !suffix.HasSuffix {
		return false
	}
	raw := strings.TrimSpace(suffix.RawSuffix)
	if mode, ok := thinking.ParseSpecialSuffix(raw); ok && mode == thinking.ModeNone {
		return true
	}
	if budget, ok := thinking.ParseNumericSuffix(raw); ok && budget == 0 {
		return true
	}
	return false
}

// openAICompatSuffixEnablesThinking reports whether a thinking suffix parses
// to a valid thinking configuration that enables reasoning. It mirrors the
// canonical parseSuffixToConfig acceptance set (special values, discrete
// levels, numeric budgets) so that the capability gate stays in sync with
// ApplyRequestThinking. Disabled suffixes ("(none)"/"(0)") must be rejected
// here; callers handle them via openAICompatSuffixDisablesThinking.
func openAICompatSuffixEnablesThinking(suffix thinking.SuffixResult) bool {
	if !suffix.HasSuffix {
		return false
	}
	raw := strings.TrimSpace(suffix.RawSuffix)
	if mode, ok := thinking.ParseSpecialSuffix(raw); ok {
		return mode != thinking.ModeNone
	}
	if _, ok := thinking.ParseLevelSuffix(raw); ok {
		return true
	}
	if budget, ok := thinking.ParseNumericSuffix(raw); ok {
		return budget > 0
	}
	return false
}

// isOpenAICompatDisabledEffortValue reports whether a reasoning_effort value
// disables thinking (currently the "none" level).
func isOpenAICompatDisabledEffortValue(effort gjson.Result) bool {
	if effort.Type != gjson.String {
		return false
	}
	return strings.ToLower(strings.TrimSpace(effort.String())) == "none"
}

// isOpenAICompatThinkingObjectDisabled reports whether a Kimi-style thinking
// object disables thinking via type:"disabled".
func isOpenAICompatThinkingObjectDisabled(thinkingField gjson.Result) bool {
	if !thinkingField.IsObject() {
		return false
	}
	return strings.ToLower(strings.TrimSpace(thinkingField.Get("type").String())) == "disabled"
}

// EnsureOpenAICompatAssistantReasoningContent ensures every assistant message containing
// tool_calls has a non-empty reasoning_content field to satisfy strict OpenAI-compatible
// reasoning providers (e.g. DeepSeek, Kimi).
func EnsureOpenAICompatAssistantReasoningContent(payload []byte) []byte {
	messages := gjson.GetBytes(payload, "messages")
	if !messages.Exists() || !messages.IsArray() {
		return payload
	}

	out := payload
	messageIndex := 0
	messages.ForEach(func(_, message gjson.Result) bool {
		if message.Get("role").String() == "assistant" {
			toolCalls := message.Get("tool_calls")
			if toolCalls.Exists() && toolCalls.IsArray() && len(toolCalls.Array()) > 0 {
				reasoning := message.Get("reasoning_content")
				if !reasoning.Exists() || strings.TrimSpace(reasoning.String()) == "" {
					path := fmt.Sprintf("messages.%d.reasoning_content", messageIndex)
					if updated, errSet := sjson.SetBytes(out, path, "[reasoning unavailable]"); errSet == nil {
						out = updated
					}
				}
			}
		}
		messageIndex++
		return true
	})
	return out
}

func openAICompatibilityModelExcludesImages(models []config.OpenAICompatibilityModel, model string) (bool, bool) {
	model = normalizeOpenAICompatibilityModelName(model)
	if model == "" {
		return false, false
	}

	for i := range models {
		if strings.EqualFold(model, normalizeOpenAICompatibilityModelName(models[i].Name)) {
			return inputModalitiesExcludeImages(models[i].InputModalities), true
		}
	}

	matched := false
	excludesImages := true
	for i := range models {
		if !strings.EqualFold(model, normalizeOpenAICompatibilityModelName(models[i].Alias)) {
			continue
		}
		matched = true
		if !inputModalitiesExcludeImages(models[i].InputModalities) {
			excludesImages = false
		}
	}
	return excludesImages && matched, matched
}

func inputModalitiesExcludeImages(modalities []string) bool {
	if len(modalities) == 0 {
		return false
	}

	hasText := false
	for _, rawModality := range modalities {
		switch strings.ToLower(strings.TrimSpace(rawModality)) {
		case "image":
			return false
		case "text":
			hasText = true
		}
	}
	return hasText
}

func normalizeOpenAICompatibilityModelName(model string) string {
	model = strings.TrimSpace(model)
	if model == "" {
		return ""
	}
	return strings.TrimSpace(thinking.ParseSuffix(model).ModelName)
}

func flattenOpenAIToolResultContent(content gjson.Result) string {
	if content.Type == gjson.String {
		return content.String()
	}

	if content.IsArray() {
		parts := make([]string, 0, 4)
		content.ForEach(func(_, item gjson.Result) bool {
			if part, ok := openAIToolResultPartText(item); ok {
				parts = append(parts, part)
			}
			return true
		})
		return strings.Join(parts, "\n\n")
	}

	if content.IsObject() {
		if isOpenAIImageToolResultPart(content) {
			return openAIToolResultImageOmittedText
		}
		if text := content.Get("text"); text.Type == gjson.String {
			return text.String()
		}
	}

	return content.Raw
}

func openAIToolResultPartText(item gjson.Result) (string, bool) {
	if item.Type == gjson.String {
		return item.String(), true
	}
	if item.IsObject() {
		if isOpenAIImageToolResultPart(item) {
			return openAIToolResultImageOmittedText, true
		}
		if text := item.Get("text"); text.Type == gjson.String {
			return text.String(), true
		}
	}
	if item.Raw == "" {
		return "", false
	}
	return item.Raw, true
}

func isOpenAIImageToolResultPart(item gjson.Result) bool {
	if !item.IsObject() {
		return false
	}

	switch strings.ToLower(strings.TrimSpace(item.Get("type").String())) {
	case "image", "image_url", "input_image":
		return true
	}
	return item.Get("image_url").Exists() || item.Get("input_image").Exists()
}
