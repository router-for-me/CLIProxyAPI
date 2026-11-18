package thinking

import (
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
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
	if !summaryFormatSupported(normalized) || len(body) == 0 || !ojson.ValidBytes(body) {
		return SummaryConfig{}
	}
	return extractSummaryConfigFromGetter(func(path string) ojson.Result {
		return ojson.GetBytesNoCopy(body, path)
	}, normalized)
}

// ExtractSummaryConfigDoc reads protocol-specific summary visibility intent from doc.
func ExtractSummaryConfigDoc(doc *ojson.Doc, format string) SummaryConfig {
	return extractSummaryConfigDoc(doc, true, format)
}

func extractSummaryConfigDoc(doc *ojson.Doc, docValid bool, format string) SummaryConfig {
	normalized := strings.ToLower(strings.TrimSpace(format))
	if doc == nil || !docValid || !summaryFormatSupported(normalized) {
		return SummaryConfig{}
	}
	return extractSummaryConfigFromGetter(doc.Get, normalized)
}

func extractSummaryConfigFromGetter(get func(string) ojson.Result, normalized string) SummaryConfig {
	switch normalized {
	case "openai":
		if config, ok := extractOpenAIExplicitSummaryConfigFromGetter(get); ok {
			return config
		}
		if effort := get("reasoning_effort"); effort.Type == ojson.String {
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
		if config, ok := responsesSummaryConfigFromGetter(get, "reasoning.summary"); ok {
			return config
		}
		if config, ok := responsesSummaryConfigFromGetter(get, "reasoning.generate_summary"); ok {
			return config
		}
	case "claude":
		// Anthropic only accepts display alongside active adaptive/manual thinking.
		if !claudeThinkingAcceptsDisplayFromGetter(get) {
			return SummaryConfig{}
		}
		if config, ok := claudeSummaryConfigFromGetter(get, "thinking.display"); ok {
			return config
		}
	case "gemini":
		if config, ok := firstSummaryBoolConfigFromGetter(get, []string{
			"generationConfig.thinkingConfig.includeThoughts",
			"generationConfig.thinkingConfig.include_thoughts",
			"generation_config.thinking_config.include_thoughts",
			"generation_config.thinking_config.includeThoughts",
		}); ok {
			return config
		}
	case "antigravity":
		if config, ok := firstSummaryBoolConfigFromGetter(get, []string{
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
			if config, ok := interactionsSummaryConfigFromGetter(get, path); ok {
				return config
			}
		}
		// Existing Interactions translators accept the OpenAI-style top-level
		// compatibility object. Keep the official generation_config selector
		// authoritative when both are present.
		if config, ok := interactionsSummaryConfigFromGetter(get, "reasoning.summary"); ok {
			return config
		}
		if config, ok := firstSummaryBoolConfigFromGetter(get, []string{
			"generation_config.thinking_config.include_thoughts",
			"generation_config.thinking_config.includeThoughts",
			"generation_config.thinkingConfig.include_thoughts",
			"generation_config.thinkingConfig.includeThoughts",
		}); ok {
			return config
		}
	}

	return SummaryConfig{}
}

// ExtractExplicitSummaryConfig reads only explicit visibility controls from a
// provider payload. Unlike ExtractSummaryConfig, OpenAI Chat reasoning_effort
// is not treated as a summary proxy. This lets executor post-processing tell
// whether a request normalizer retained or removed the translated target field.
func ExtractExplicitSummaryConfig(body []byte, format string) SummaryConfig {
	normalized := strings.ToLower(strings.TrimSpace(format))
	if normalized != "openai" {
		return ExtractSummaryConfig(body, normalized)
	}
	if len(body) == 0 || !ojson.ValidBytes(body) {
		return SummaryConfig{}
	}
	config, _ := extractOpenAIExplicitSummaryConfigFromGetter(func(path string) ojson.Result {
		return ojson.GetBytesNoCopy(body, path)
	})
	return config
}

// ExtractExplicitSummaryConfigDoc reads only explicit visibility controls from doc.
func ExtractExplicitSummaryConfigDoc(doc *ojson.Doc, format string) SummaryConfig {
	normalized := strings.ToLower(strings.TrimSpace(format))
	if normalized != "openai" {
		return ExtractSummaryConfigDoc(doc, normalized)
	}
	if doc == nil {
		return SummaryConfig{}
	}
	config, _ := extractOpenAIExplicitSummaryConfigFromGetter(doc.Get)
	return config
}

// ExtractTranslatedSummaryConfig reads source visibility intent using the
// source/target protocol pair. OpenAI Chat reasoning_effort controls depth,
// not Claude display visibility, so it is ignored only for Chat-to-Claude.
func ExtractTranslatedSummaryConfig(body []byte, sourceFormat, targetFormat string) SummaryConfig {
	source := strings.ToLower(strings.TrimSpace(sourceFormat))
	target := strings.ToLower(strings.TrimSpace(targetFormat))
	if target == "claude" && source == "openai" {
		return ExtractExplicitSummaryConfig(body, source)
	}
	return ExtractSummaryConfig(body, source)
}

// ApplyTranslatedSummaryToClaude copies an explicit source visibility choice
// onto a Claude body. Chat reasoning_effort is not a visibility field, so it
// stays unspecified and the Claude cloak can apply its default display.
func ApplyTranslatedSummaryToClaude(out, source []byte, sourceFormat, model string) []byte {
	config := ExtractTranslatedSummaryConfig(source, sourceFormat, "claude")
	if config.Mode == SummaryUnspecified {
		return out
	}
	return ApplySummaryConfigForModel(out, "claude", model, config)
}

// ApplySummaryConfig writes canonical summary intent in the target protocol.
func ApplySummaryConfig(body []byte, format string, config SummaryConfig) []byte {
	return ApplySummaryConfigForModel(body, format, "", config)
}

// ApplySummaryConfigForModel writes canonical summary intent in the target
// protocol and uses target model capabilities when a valid target request must
// activate thinking before it can request summaries.
func ApplySummaryConfigForModel(body []byte, format, model string, config SummaryConfig) []byte {
	return applySummaryConfigForModel(body, format, model, nil, config)
}

// ApplySummaryConfigForModelDoc writes canonical summary intent onto doc in place.
func ApplySummaryConfigForModelDoc(doc *ojson.Doc, format, model string, config SummaryConfig) {
	_ = applySummaryConfigForProviderDoc(doc, true, format, model, "", nil, config)
}

// CanRepresentSummaryConfigForModelDoc reports whether ApplySummaryConfigForModelDoc
// would produce an explicit summary control on doc, without mutating or serializing doc.
func CanRepresentSummaryConfigForModelDoc(doc *ojson.Doc, format, model string, config SummaryConfig) bool {
	normalized := strings.ToLower(strings.TrimSpace(format))
	if doc == nil || doc.Len() == 0 || config.Mode == SummaryUnspecified || !summaryFormatSupported(normalized) {
		return false
	}
	enabled := config.Mode == SummaryEnabled
	switch normalized {
	case "gemini", "antigravity", "interactions":
		return true
	case "openai-response", "codex":
		return enabled
	case "openai":
		return doc.Get("reasoning.exclude").IsBool() || doc.Get("include_reasoning").IsBool() ||
			doc.Get("reasoning.enabled").IsBool() || extractExplicitSummaryNonBoolOpenAIFromGetter(doc.Get)
	case "claude":
		if claudeThinkingAcceptsDisplayFromGetter(doc.Get) {
			return true
		}
		if !enabled || doc.Get("thinking.type").Exists() {
			return false
		}
		baseModel := ParseSuffix(model).ModelName
		if baseModel == "" {
			baseModel = ParseSuffix(doc.Get("model").String()).ModelName
		}
		modelInfo := registry.LookupModelInfo(baseModel, "claude")
		if modelInfo == nil || modelInfo.Thinking == nil {
			return false
		}
		if len(modelInfo.Thinking.Levels) > 0 {
			return true
		}
		budget := modelInfo.Thinking.Min
		if budget <= 0 {
			return false
		}
		if maxTokens := doc.Get("max_tokens"); maxTokens.Exists() && maxTokens.Int() <= int64(budget) {
			return false
		}
		return true
	default:
		return false
	}
}

func extractExplicitSummaryNonBoolOpenAIFromGetter(get func(string) ojson.Result) bool {
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
		if _, ok := summaryBoolConfigFromGetter(get, path); ok {
			return true
		}
	}
	for _, path := range []string{"reasoning.summary", "reasoning.generate_summary"} {
		if _, ok := responsesSummaryConfigFromGetter(get, path); ok {
			return true
		}
	}
	return false
}

// applySummaryConfigForModel uses the resolved model definition when execution
// selected a configured API-key model whose capability is not globally visible.
func applySummaryConfigForModel(body []byte, format, model string, modelInfo *registry.ModelInfo, config SummaryConfig) []byte {
	return applySummaryConfigForProvider(body, format, model, "", modelInfo, config)
}

// applySummaryConfigForProvider uses the execution provider identity for Chat
// dialects whose visibility controls are not part of the OpenAI wire format.
func applySummaryConfigForProvider(body []byte, format, model, provider string, modelInfo *registry.ModelInfo, config SummaryConfig) []byte {
	normalized := strings.ToLower(strings.TrimSpace(format))
	if config.Mode == SummaryUnspecified || !summaryFormatSupported(normalized) || len(body) == 0 || !ojson.ValidBytes(body) {
		return body
	}
	doc := ojson.NewDoc(body)
	if !applySummaryConfigForProviderDoc(doc, true, normalized, model, provider, modelInfo, config) {
		return body
	}
	return doc.Bytes()
}

func applySummaryConfigForProviderDoc(doc *ojson.Doc, docValid bool, format, model, provider string, modelInfo *registry.ModelInfo, config SummaryConfig) bool {
	normalized := strings.ToLower(strings.TrimSpace(format))
	if doc == nil || !docValid || config.Mode == SummaryUnspecified || !summaryFormatSupported(normalized) {
		return false
	}

	enabled := config.Mode == SummaryEnabled
	modified := false
	switch normalized {
	case "openai":
		return applyOpenAIChatSummaryConfigDoc(doc, provider, enabled)
	case "claude":
		if enabled && !doc.Get("thinking.type").Exists() {
			if enableClaudeThinkingForSummaryDoc(doc, model, modelInfo) {
				modified = true
			}
		}
		if !claudeThinkingAcceptsDisplayFromGetter(doc.Get) {
			return modified
		}
		value := "omitted"
		if enabled {
			value = "summarized"
			if strings.EqualFold(strings.TrimSpace(doc.Get("thinking.display").String()), "updates") {
				value = "updates"
			}
		}
		if doc.Set("thinking.display", value) == nil {
			modified = true
		}
	case "gemini":
		if doc.Set("generationConfig.thinkingConfig.includeThoughts", enabled) == nil {
			modified = true
		}
		for _, path := range []string{
			"generationConfig.thinkingConfig.include_thoughts",
			"generation_config.thinking_config.include_thoughts",
			"generation_config.thinking_config.includeThoughts",
		} {
			if doc.Get(path).Exists() && doc.Delete(path) == nil {
				modified = true
			}
		}
	case "antigravity":
		if doc.Set("request.generationConfig.thinkingConfig.includeThoughts", enabled) == nil {
			modified = true
		}
		for _, path := range []string{
			"request.generationConfig.thinkingConfig.include_thoughts",
			"request.generationConfig.thinking_config.includeThoughts",
			"request.generationConfig.thinking_config.include_thoughts",
		} {
			if doc.Get(path).Exists() && doc.Delete(path) == nil {
				modified = true
			}
		}
	case "interactions":
		// Google Interactions only accepts auto or none. OpenAI's concise and
		// detailed selectors therefore collapse to the supported enabled value.
		value := "none"
		if enabled {
			value = "auto"
		}
		if doc.Set("generation_config.thinking_summaries", value) == nil {
			modified = true
		}
		if doc.Get("generation_config.thinkingSummaries").Exists() && doc.Delete("generation_config.thinkingSummaries") == nil {
			modified = true
		}
	case "openai-response", "codex":
		if enabled {
			if doc.Set("reasoning.summary", normalizedSummaryDetail(config.Detail)) == nil {
				modified = true
			}
			if doc.Get("reasoning.generate_summary").Exists() && doc.Delete("reasoning.generate_summary") == nil {
				modified = true
			}
			break
		}
		// Omitting the field is the documented way to disable summaries; an
		// explicit null is not accepted by every Responses-compatible backend.
		if doc.Get("reasoning.summary").Exists() && util.DeleteJSONDocIfExists(doc, "reasoning.summary") == nil {
			modified = true
		}
		if doc.Get("reasoning.generate_summary").Exists() && util.DeleteJSONDocIfExists(doc, "reasoning.generate_summary") == nil {
			modified = true
		}
		if reasoning := doc.Get("reasoning"); reasoning.IsObject() && len(reasoning.Map()) == 0 {
			if doc.Delete("reasoning") == nil {
				modified = true
			}
		}
	}
	return modified
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

func claudeThinkingAcceptsDisplayFromGetter(get func(string) ojson.Result) bool {
	switch strings.ToLower(strings.TrimSpace(get("thinking.type").String())) {
	case "adaptive":
		return true
	case "enabled":
		budget := get("thinking.budget_tokens")
		if budget.Type != ojson.Number {
			return true
		}
		value := budget.Int()
		return value == -1 || value > 0
	default:
		return false
	}
}

func applyOpenAIChatSummaryConfigDoc(doc *ojson.Doc, provider string, enabled bool) bool {
	modified := false
	if isOpenRouterProvider(provider) || doc.Get("reasoning.exclude").IsBool() {
		if doc.Set("reasoning.exclude", !enabled) == nil {
			modified = true
		}
	}
	if doc.Get("include_reasoning").IsBool() {
		if doc.Set("include_reasoning", enabled) == nil {
			modified = true
		}
	}
	return modified
}

func isOpenRouterProvider(provider string) bool {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "openrouter" {
		return true
	}
	for _, part := range strings.FieldsFunc(provider, func(r rune) bool {
		return r == '-' || r == '_' || r == '/' || r == '.' || r == ':'
	}) {
		if part == "openrouter" {
			return true
		}
	}
	return false
}

func extractOpenAIExplicitSummaryConfigFromGetter(get func(string) ojson.Result) (SummaryConfig, bool) {
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
		if config, ok := summaryBoolConfigFromGetter(get, path); ok {
			return config, true
		}
	}

	for _, path := range []string{
		"reasoning.summary",
		"reasoning.generate_summary",
	} {
		if config, ok := responsesSummaryConfigFromGetter(get, path); ok {
			return config, true
		}
	}

	if exclude := get("reasoning.exclude"); exclude.IsBool() {
		if exclude.Bool() {
			return SummaryConfig{Mode: SummaryDisabled}, true
		}
		return SummaryConfig{Mode: SummaryEnabled, Detail: "auto"}, true
	}
	if include := get("include_reasoning"); include.IsBool() {
		if include.Bool() {
			return SummaryConfig{Mode: SummaryEnabled, Detail: "auto"}, true
		}
		return SummaryConfig{Mode: SummaryDisabled}, true
	}
	if enabled := get("reasoning.enabled"); enabled.IsBool() {
		if enabled.Bool() {
			return SummaryConfig{Mode: SummaryEnabled, Detail: "auto"}, true
		}
		return SummaryConfig{Mode: SummaryDisabled}, true
	}
	return SummaryConfig{}, false
}

func firstSummaryBoolConfigFromGetter(get func(string) ojson.Result, paths []string) (SummaryConfig, bool) {
	for _, path := range paths {
		if config, ok := summaryBoolConfigFromGetter(get, path); ok {
			return config, true
		}
	}
	return SummaryConfig{}, false
}

func summaryBoolConfigFromGetter(get func(string) ojson.Result, path string) (SummaryConfig, bool) {
	switch value := get(path); value.Type {
	case ojson.True:
		return SummaryConfig{Mode: SummaryEnabled, Detail: "auto"}, true
	case ojson.False:
		return SummaryConfig{Mode: SummaryDisabled}, true
	default:
		return SummaryConfig{}, false
	}
}

func responsesSummaryConfigFromGetter(get func(string) ojson.Result, path string) (SummaryConfig, bool) {
	value := get(path)
	if value.Raw == "" {
		return SummaryConfig{}, false
	}
	if value.Type == ojson.Null {
		return SummaryConfig{Mode: SummaryDisabled}, true
	}
	if value.Type != ojson.String {
		return SummaryConfig{}, false
	}

	raw := strings.ToLower(strings.TrimSpace(value.String()))
	switch raw {
	case "auto", "concise", "detailed":
		return SummaryConfig{Mode: SummaryEnabled, Detail: raw}, true
	case "none":
		return SummaryConfig{Mode: SummaryDisabled}, true
	default:
		return SummaryConfig{}, false
	}
}

func claudeSummaryConfigFromGetter(get func(string) ojson.Result, path string) (SummaryConfig, bool) {
	value := get(path)
	if value.Type != ojson.String {
		return SummaryConfig{}, false
	}
	switch strings.ToLower(strings.TrimSpace(value.String())) {
	case "summarized", "updates":
		return SummaryConfig{Mode: SummaryEnabled, Detail: "auto"}, true
	case "omitted":
		return SummaryConfig{Mode: SummaryDisabled}, true
	default:
		return SummaryConfig{}, false
	}
}

func interactionsSummaryConfigFromGetter(get func(string) ojson.Result, path string) (SummaryConfig, bool) {
	value := get(path)
	if value.Type != ojson.String {
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

func stripInferredClaudeSummaryActivationDoc(doc *ojson.Doc, docValid bool, modelInfo *registry.ModelInfo) bool {
	if doc == nil || !docValid || modelInfo == nil || modelInfo.Thinking == nil || len(modelInfo.Thinking.Levels) > 0 || modelInfo.Thinking.Min <= 0 {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(doc.Get("thinking.type").String()), "adaptive") {
		return false
	}

	modified := false
	for _, path := range []string{
		"thinking.type",
		"thinking.budget_tokens",
		"thinking.display",
		"output_config.effort",
	} {
		if doc.Get(path).Exists() && util.DeleteJSONDocIfExists(doc, path) == nil {
			modified = true
		}
	}
	for _, path := range []string{"thinking", "output_config"} {
		if object := doc.Get(path); object.Exists() && object.IsObject() && len(object.Map()) == 0 {
			if doc.Delete(path) == nil {
				modified = true
			}
		}
	}
	return modified
}

func enableClaudeThinkingForSummaryDoc(doc *ojson.Doc, model string, resolvedModelInfo *registry.ModelInfo) bool {
	modelInfo := resolvedModelInfo
	if modelInfo == nil {
		baseModel := ParseSuffix(model).ModelName
		if baseModel == "" {
			baseModel = ParseSuffix(doc.Get("model").String()).ModelName
		}
		modelInfo = registry.LookupModelInfo(baseModel, "claude")
	}
	if modelInfo == nil || modelInfo.Thinking == nil {
		return false
	}

	if len(modelInfo.Thinking.Levels) > 0 {
		_ = doc.Set("thinking.type", "adaptive")
		_ = util.DeleteJSONDocIfExists(doc, "thinking.budget_tokens")
		return true
	}

	budget := modelInfo.Thinking.Min
	if budget <= 0 {
		return false
	}
	if maxTokens := doc.Get("max_tokens"); maxTokens.Exists() && maxTokens.Int() <= int64(budget) {
		return false
	}
	_ = doc.Set("thinking.type", "enabled")
	_ = doc.Set("thinking.budget_tokens", budget)
	return true
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
