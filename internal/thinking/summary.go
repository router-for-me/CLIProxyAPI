package thinking

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// SummaryMode represents whether the client explicitly requested reasoning summaries.
type SummaryMode int

const (
	SummaryUnspecified SummaryMode = iota
	SummaryDisabled
	SummaryEnabled
)

// SummaryConfig is the provider-neutral reasoning-summary visibility intent.
// Detail preserves protocols that distinguish auto, concise, and detailed summaries.
type SummaryConfig struct {
	Mode   SummaryMode
	Detail string
}

// ExtractSummaryConfig reads protocol-specific summary visibility intent.
//
// OpenAI Chat is the one protocol where effort implies summaries: chat
// completions has no summary field of its own, and clients that send
// reasoning_effort have always received reasoning summaries here, so treating a
// non-none effort as an explicit request preserves that contract. Every other
// protocol carries a dedicated summary field, so effort alone means nothing.
func ExtractSummaryConfig(body []byte, format string) SummaryConfig {
	normalized := strings.ToLower(strings.TrimSpace(format))
	// Check the format first so unsupported targets skip whole-body validation.
	if !summaryFormatSupported(normalized) || len(body) == 0 || !gjson.ValidBytes(body) {
		return SummaryConfig{}
	}

	switch normalized {
	case "openai":
		if config, ok := extractOpenAIExplicitSummaryConfig(body); ok {
			return config
		}
		if effort := gjson.GetBytes(body, "reasoning_effort"); effort.Type == gjson.String {
			value := strings.ToLower(strings.TrimSpace(effort.String()))
			if value == "" {
				return SummaryConfig{}
			}
			if value == "none" {
				return SummaryConfig{Mode: SummaryDisabled}
			}
			return SummaryConfig{Mode: SummaryEnabled, Detail: "auto"}
		}
	case "openai-response", "codex":
		if config, ok := responsesSummaryConfig(body, "reasoning.summary"); ok {
			return config
		}
		if config, ok := responsesSummaryConfig(body, "reasoning.generate_summary"); ok {
			return config
		}
	case "claude":
		// Anthropic only accepts display alongside active adaptive/manual thinking.
		if !claudeThinkingAcceptsDisplay(body) {
			return SummaryConfig{}
		}
		if config, ok := claudeSummaryConfig(body, "thinking.display"); ok {
			return config
		}
	case "gemini":
		if config, ok := firstSummaryBoolConfig(body, []string{
			"generationConfig.thinkingConfig.includeThoughts",
			"generationConfig.thinkingConfig.include_thoughts",
			"generation_config.thinking_config.include_thoughts",
			"generation_config.thinking_config.includeThoughts",
		}); ok {
			return config
		}
	case "antigravity":
		if config, ok := firstSummaryBoolConfig(body, []string{
			"request.generationConfig.thinkingConfig.includeThoughts",
			"request.generationConfig.thinkingConfig.include_thoughts",
			"request.generationConfig.thinking_config.includeThoughts",
			"request.generationConfig.thinking_config.include_thoughts",
		}); ok {
			return config
		}
	case "interactions":
		for _, path := range []string{
			"generation_config.thinking_summaries",
			"generation_config.thinkingSummaries",
		} {
			if config, ok := interactionsSummaryConfig(body, path); ok {
				return config
			}
		}
	}

	return SummaryConfig{}
}

// ClearSummaryConfig removes target visibility controls when the source did not
// explicitly request thinking text. This prevents a protocol translator's
// historical default from becoming a new, unintended visibility request.
func ClearSummaryConfig(body []byte, format string) []byte {
	normalized := strings.ToLower(strings.TrimSpace(format))
	if !summaryFormatSupported(normalized) || len(body) == 0 || !gjson.ValidBytes(body) {
		return body
	}

	switch normalized {
	case "claude":
		body, _ = sjson.DeleteBytes(body, "thinking.display")
	case "gemini":
		for _, path := range []string{
			"generationConfig.thinkingConfig.includeThoughts",
			"generationConfig.thinkingConfig.include_thoughts",
			"generation_config.thinking_config.include_thoughts",
			"generation_config.thinking_config.includeThoughts",
		} {
			body, _ = sjson.DeleteBytes(body, path)
		}
	case "antigravity":
		for _, path := range []string{
			"request.generationConfig.thinkingConfig.includeThoughts",
			"request.generationConfig.thinkingConfig.include_thoughts",
			"request.generationConfig.thinking_config.includeThoughts",
			"request.generation_config.thinking_config.include_thoughts",
		} {
			body, _ = sjson.DeleteBytes(body, path)
		}
	case "interactions":
		body, _ = sjson.DeleteBytes(body, "generation_config.thinking_summaries")
		body, _ = sjson.DeleteBytes(body, "generation_config.thinkingSummaries")
	case "openai-response", "codex":
		body, _ = sjson.DeleteBytes(body, "reasoning.summary")
		body, _ = sjson.DeleteBytes(body, "reasoning.generate_summary")
		if reasoning := gjson.GetBytes(body, "reasoning"); reasoning.IsObject() && len(reasoning.Map()) == 0 {
			body, _ = sjson.DeleteBytes(body, "reasoning")
		}
	}
	return body
}

// ApplySummaryConfig writes canonical summary intent in the target protocol.
func ApplySummaryConfig(body []byte, format string, config SummaryConfig) []byte {
	return ApplySummaryConfigForModel(body, format, "", config)
}

// ApplySummaryConfigForModel writes canonical summary intent in the target
// protocol and uses target model capabilities when a valid target request must
// activate thinking before it can request summaries.
func ApplySummaryConfigForModel(body []byte, format, model string, config SummaryConfig) []byte {
	normalized := strings.ToLower(strings.TrimSpace(format))
	if config.Mode == SummaryUnspecified || !summaryFormatSupported(normalized) || len(body) == 0 || !gjson.ValidBytes(body) {
		return body
	}

	enabled := config.Mode == SummaryEnabled
	switch normalized {
	case "openai":
		body = applyOpenAIChatSummaryConfig(body, model, enabled)
	case "claude":
		// Anthropic documents display as invalid with thinking.type=disabled and
		// requires it alongside adaptive or enabled thinking. A summary-only
		// Responses/Gemini/Interactions request is valid at the source because
		// those APIs have model-default thinking, so activate the target model's
		// documented thinking mode before writing summarized display.
		if enabled && !gjson.GetBytes(body, "thinking.type").Exists() {
			body = enableClaudeThinkingForSummary(body, model)
		}
		if !claudeThinkingAcceptsDisplay(body) {
			return body
		}
		value := "omitted"
		if enabled {
			value = "summarized"
		}
		body, _ = sjson.SetBytes(body, "thinking.display", value)
	case "gemini":
		body, _ = sjson.SetBytes(body, "generationConfig.thinkingConfig.includeThoughts", enabled)
		for _, path := range []string{
			"generationConfig.thinkingConfig.include_thoughts",
			"generation_config.thinking_config.include_thoughts",
			"generation_config.thinking_config.includeThoughts",
		} {
			body, _ = sjson.DeleteBytes(body, path)
		}
	case "antigravity":
		body, _ = sjson.SetBytes(body, "request.generationConfig.thinkingConfig.includeThoughts", enabled)
		for _, path := range []string{
			"request.generationConfig.thinkingConfig.include_thoughts",
			"request.generationConfig.thinking_config.include_thoughts",
			"request.generationConfig.thinking_config.includeThoughts",
		} {
			body, _ = sjson.DeleteBytes(body, path)
		}
	case "interactions":
		// Google Interactions only accepts auto or none. OpenAI's concise and
		// detailed selectors therefore collapse to the supported enabled value.
		value := "none"
		if enabled {
			value = "auto"
		}
		body, _ = sjson.SetBytes(body, "generation_config.thinking_summaries", value)
		body, _ = sjson.DeleteBytes(body, "generation_config.thinkingSummaries")
	case "openai-response", "codex":
		if enabled {
			body, _ = sjson.SetBytes(body, "reasoning.summary", normalizedSummaryDetail(config.Detail))
			body, _ = sjson.DeleteBytes(body, "reasoning.generate_summary")
			break
		}
		// Omitting the field is the documented way to disable summaries; an
		// explicit null is not accepted by every Responses-compatible backend.
		body, _ = sjson.DeleteBytes(body, "reasoning.summary")
		body, _ = sjson.DeleteBytes(body, "reasoning.generate_summary")
		if reasoning := gjson.GetBytes(body, "reasoning"); reasoning.IsObject() && len(reasoning.Map()) == 0 {
			body, _ = sjson.DeleteBytes(body, "reasoning")
		}
	}
	return body
}

// summaryFormatSupported reports whether a protocol carries summary visibility
// intent that this package can read or write.
func summaryFormatSupported(format string) bool {
	switch format {
	case "openai", "openai-response", "codex", "claude", "gemini", "antigravity", "interactions":
		return true
	default:
		return false
	}
}

// claudeThinkingAcceptsDisplay reports whether the body carries an active
// thinking block that can hold a display field.
func claudeThinkingAcceptsDisplay(body []byte) bool {
	switch strings.ToLower(strings.TrimSpace(gjson.GetBytes(body, "thinking.type").String())) {
	case "adaptive":
		return true
	case "enabled":
		// This runs before ApplyThinking normalizes the request, so a missing
		// budget_tokens is an unfinished body rather than inactive thinking.
		// Only an explicit non-positive budget means thinking is off.
		budget := gjson.GetBytes(body, "thinking.budget_tokens")
		return budget.Type != gjson.Number || budget.Int() > 0
	default:
		return false
	}
}

// applyOpenAIChatSummaryConfig writes summary visibility intent for the Chat
// Completions protocol.
//
// Four dialects share this protocol and only OpenAI's is authoritative. OpenAI
// documents no reasoning-visibility field at all (Chat Completions never returns
// reasoning text) and rejects unknown body parameters, so reasoning_effort is the
// only field that is always safe to write here. OpenRouter's documented
// "reason but hide" bits (reasoning.exclude and its legacy include_reasoning
// alias) are updated only when the body already carries them, which is exactly
// when the upstream is known to understand them.
func applyOpenAIChatSummaryConfig(body []byte, model string, enabled bool) []byte {
	if gjson.GetBytes(body, "reasoning").IsObject() {
		body, _ = sjson.SetBytes(body, "reasoning.exclude", !enabled)
	}
	if gjson.GetBytes(body, "include_reasoning").IsBool() {
		body, _ = sjson.SetBytes(body, "include_reasoning", enabled)
	}
	if !enabled {
		// Chat has no portable way to keep reasoning while hiding its summary.
		// reasoning_effort:"none" would disable reasoning instead of hiding it,
		// and Google documents that it is not even honored on Gemini 2.5 Pro or
		// 3 models, so leave the effort the client asked for untouched.
		return body
	}
	effort := gjson.GetBytes(body, "reasoning_effort")
	if effort.Type != gjson.String || strings.TrimSpace(effort.String()) == "" || strings.EqualFold(strings.TrimSpace(effort.String()), "none") {
		body, _ = sjson.SetBytes(body, "reasoning_effort", openAIChatSummaryEffort(body, model))
	}
	return body
}

// openAIChatSummaryEffort picks an active reasoning effort that the target model
// documents. Chat exposes reasoning only while an effort is active, so a summary
// request has to select one when the client left it unset.
func openAIChatSummaryEffort(body []byte, model string) string {
	baseModel := ParseSuffix(model).ModelName
	if baseModel == "" {
		baseModel = ParseSuffix(gjson.GetBytes(body, "model").String()).ModelName
	}
	modelInfo := registry.LookupModelInfo(baseModel, "openai")
	if modelInfo == nil || modelInfo.Thinking == nil || len(modelInfo.Thinking.Levels) == 0 {
		return "medium"
	}

	levels := make([]string, 0, len(modelInfo.Thinking.Levels))
	for _, level := range modelInfo.Thinking.Levels {
		normalized := strings.ToLower(strings.TrimSpace(level))
		if normalized == "" || normalized == "none" {
			continue
		}
		if normalized == "medium" {
			return "medium"
		}
		levels = append(levels, normalized)
	}
	if len(levels) == 0 {
		return "medium"
	}
	return levels[len(levels)/2]
}

func extractOpenAIExplicitSummaryConfig(body []byte) (SummaryConfig, bool) {
	// Google's documented Chat Completions extension is the authoritative
	// explicit visibility control when present, ahead of CPA compatibility
	// aliases and Chat's reasoning_effort fallback.
	for _, path := range []string{
		"extra_body.google.thinking_config.include_thoughts",
		"extra_body.google.thinking_config.includeThoughts",
		"extra_body.google.thinkingConfig.include_thoughts",
		"extra_body.google.thinkingConfig.includeThoughts",
		"extra_body.extra_body.google.thinking_config.include_thoughts",
		"extra_body.extra_body.google.thinking_config.includeThoughts",
		"google.thinking_config.include_thoughts",
		"google.thinking_config.includeThoughts",
		"thinking.includeThoughts",
		"thinking.include_thoughts",
		"reasoning.includeThoughts",
		"reasoning.include_thoughts",
		"generationConfig.thinkingConfig.includeThoughts",
		"generationConfig.thinkingConfig.include_thoughts",
		"generation_config.thinking_config.include_thoughts",
		"generation_config.thinking_config.includeThoughts",
	} {
		if config, ok := summaryBoolConfig(body, path); ok {
			return config, true
		}
	}

	for _, path := range []string{
		"reasoning.summary",
		"reasoning.generate_summary",
	} {
		if config, ok := responsesSummaryConfig(body, path); ok {
			return config, true
		}
	}

	// reasoning.exclude is OpenRouter's documented "reason but hide" bit, not an
	// OpenAI wire field; include_reasoning is its documented legacy alias
	// (include_reasoning: false is equivalent to reasoning: {exclude: true}).
	// Only accept actual JSON booleans.
	if exclude := gjson.GetBytes(body, "reasoning.exclude"); exclude.IsBool() {
		if exclude.Bool() {
			return SummaryConfig{Mode: SummaryDisabled}, true
		}
		return SummaryConfig{Mode: SummaryEnabled, Detail: "auto"}, true
	}
	if include := gjson.GetBytes(body, "include_reasoning"); include.IsBool() {
		if include.Bool() {
			return SummaryConfig{Mode: SummaryEnabled, Detail: "auto"}, true
		}
		return SummaryConfig{Mode: SummaryDisabled}, true
	}
	// OpenRouter's reasoning.enabled turns reasoning on "with no exclusions", so
	// it also decides visibility when no dedicated bit was sent.
	if enabled := gjson.GetBytes(body, "reasoning.enabled"); enabled.IsBool() {
		if enabled.Bool() {
			return SummaryConfig{Mode: SummaryEnabled, Detail: "auto"}, true
		}
		return SummaryConfig{Mode: SummaryDisabled}, true
	}
	return SummaryConfig{}, false
}

func firstSummaryBoolConfig(body []byte, paths []string) (SummaryConfig, bool) {
	for _, path := range paths {
		if config, ok := summaryBoolConfig(body, path); ok {
			return config, true
		}
	}
	return SummaryConfig{}, false
}

func summaryBoolConfig(body []byte, path string) (SummaryConfig, bool) {
	switch value := gjson.GetBytes(body, path); value.Type {
	case gjson.True:
		return SummaryConfig{Mode: SummaryEnabled, Detail: "auto"}, true
	case gjson.False:
		return SummaryConfig{Mode: SummaryDisabled}, true
	default:
		return SummaryConfig{}, false
	}
}

func responsesSummaryConfig(body []byte, path string) (SummaryConfig, bool) {
	value := gjson.GetBytes(body, path)
	if value.Raw == "" {
		return SummaryConfig{}, false
	}
	if value.Type == gjson.Null {
		return SummaryConfig{Mode: SummaryDisabled}, true
	}
	if value.Type != gjson.String {
		return SummaryConfig{}, false
	}

	raw := strings.ToLower(strings.TrimSpace(value.String()))
	switch raw {
	case "auto", "concise", "detailed":
		return SummaryConfig{Mode: SummaryEnabled, Detail: raw}, true
	case "none":
		// Compatibility with clients that expose a none enum; the OpenAI wire
		// representation disables summaries by omitting the field.
		return SummaryConfig{Mode: SummaryDisabled}, true
	default:
		return SummaryConfig{}, false
	}
}

func claudeSummaryConfig(body []byte, path string) (SummaryConfig, bool) {
	value := gjson.GetBytes(body, path)
	if value.Type != gjson.String {
		return SummaryConfig{}, false
	}
	switch strings.ToLower(strings.TrimSpace(value.String())) {
	case "summarized":
		return SummaryConfig{Mode: SummaryEnabled, Detail: "auto"}, true
	case "omitted":
		return SummaryConfig{Mode: SummaryDisabled}, true
	default:
		return SummaryConfig{}, false
	}
}

func interactionsSummaryConfig(body []byte, path string) (SummaryConfig, bool) {
	value := gjson.GetBytes(body, path)
	if value.Type != gjson.String {
		return SummaryConfig{}, false
	}
	switch strings.ToLower(strings.TrimSpace(value.String())) {
	case "auto":
		return SummaryConfig{Mode: SummaryEnabled, Detail: "auto"}, true
	case "none":
		return SummaryConfig{Mode: SummaryDisabled}, true
	default:
		return SummaryConfig{}, false
	}
}

func enableClaudeThinkingForSummary(body []byte, model string) []byte {
	baseModel := ParseSuffix(model).ModelName
	if baseModel == "" {
		baseModel = ParseSuffix(gjson.GetBytes(body, "model").String()).ModelName
	}
	modelInfo := registry.LookupModelInfo(baseModel, "claude")
	if modelInfo == nil || modelInfo.Thinking == nil {
		return body
	}

	if len(modelInfo.Thinking.Levels) > 0 {
		body, _ = sjson.SetBytes(body, "thinking.type", "adaptive")
		body, _ = sjson.DeleteBytes(body, "thinking.budget_tokens")
		return body
	}

	budget := modelInfo.Thinking.Min
	if budget <= 0 {
		return body
	}
	if maxTokens := gjson.GetBytes(body, "max_tokens"); maxTokens.Exists() && maxTokens.Int() <= int64(budget) {
		return body
	}
	body, _ = sjson.SetBytes(body, "thinking.type", "enabled")
	body, _ = sjson.SetBytes(body, "thinking.budget_tokens", budget)
	return body
}

func normalizedSummaryDetail(detail string) string {
	switch strings.ToLower(strings.TrimSpace(detail)) {
	case "concise":
		return "concise"
	case "detailed":
		return "detailed"
	default:
		return "auto"
	}
}
