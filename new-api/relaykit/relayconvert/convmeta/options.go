package convmeta

import "github.com/QuantumNous/new-api/relaykit/types"

// Options is the per-request snapshot of host configuration that converters
// consult. The host fills it from its settings system when constructing the
// Meta (see relaycommon.RelayInfo.ConvOptions); relaykit users fill it
// directly. Zero value = every adaptation disabled, no defaults applied.
type Options struct {
	Claude ClaudeOptions
	Gemini GeminiOptions

	// ToolLossPolicy controls whether a cross-protocol conversion may omit or
	// approximate built-in-tool semantics. The zero value uses the allow
	// policy: conversion succeeds and every loss is returned as a diagnostic.
	// safe/strict rejection is request-phase opt-in only; response and stream
	// conversion never reject regardless of this field.
	ToolLossPolicy types.ConversionLossPolicy

	// OpenRouterDialect marks the upstream as OpenRouter's OpenAI-compatible
	// surface, which accepts extra fields (reasoning config, cache_control on
	// system parts) that converters emit only for that dialect. The host sets
	// it from the channel type.
	OpenRouterDialect bool

	// PreserveThinkingSuffix reports models whose -thinking/-nothinking/effort
	// suffix must be kept on the outgoing model name (host blacklist lookup).
	// Nil means "never preserve".
	PreserveThinkingSuffix func(modelName string) bool

	// PreserveEffortTail reports real model IDs whose names already end in an
	// effort-like token (for example qwen-max). Nil means "never preserve".
	PreserveEffortTail func(modelName string) bool
}

type ClaudeOptions struct {
	// ThinkingAdapterEnabled controls whether suffix-derived reasoning intent
	// is rendered onto Claude thinking / output_config. Suffix parsing itself
	// is the host entry layer's job (standalone users call Parse* themselves).
	ThinkingAdapterEnabled bool
	// ThinkingAdapterBudgetTokensPercentage sizes thinking budget_tokens as a
	// fraction of max_tokens when the adapter fires.
	ThinkingAdapterBudgetTokensPercentage float64
	// DefaultMaxTokens returns the max_tokens to inject when the source
	// request carries none. The Claude Messages API requires max_tokens
	// (omitting it is a 400), so when this hook is nil and no other path
	// supplies a value, OpenAI→Claude request conversion fails with an
	// explicit error instead of emitting a request the upstream is
	// guaranteed to reject. The new-api host always provides this hook;
	// standalone relaykit users must supply one or guarantee max_tokens on
	// every request.
	DefaultMaxTokens func(modelName string) int
	// WebSearchToolVersion selects the Claude hosted web-search tool version
	// emitted by cross-protocol conversion. Empty keeps the compatibility
	// baseline web_search_20250305.
	WebSearchToolVersion string
}

type GeminiOptions struct {
	// ThinkingAdapterEnabled controls whether suffix-derived reasoning intent
	// is rendered onto Gemini thinkingConfig. Suffix parsing itself is the
	// host entry layer's job (standalone users call Parse* themselves).
	ThinkingAdapterEnabled bool
	// ThinkingAdapterBudgetTokensPercentage sizes thinkingBudget as a fraction
	// of maxOutputTokens when the adapter fires.
	ThinkingAdapterBudgetTokensPercentage float64
	// FunctionCallThoughtSignatureEnabled attaches thoughtSignature bypass
	// values to function-call parts.
	FunctionCallThoughtSignatureEnabled bool
	// SupportsImagine reports whether the model supports image generation
	// (switches response modalities). Nil means "never".
	SupportsImagine func(modelName string) bool
	// SafetySetting returns the harm threshold for a category. Nil or empty
	// return means no safetySettings are attached.
	SafetySetting func(category string) string
}

func (o *ClaudeOptions) DefaultMaxTokensFor(modelName string) (int, bool) {
	if o == nil || o.DefaultMaxTokens == nil {
		return 0, false
	}
	return o.DefaultMaxTokens(modelName), true
}

func (o *GeminiOptions) SupportsImagineModel(modelName string) bool {
	return o != nil && o.SupportsImagine != nil && o.SupportsImagine(modelName)
}

func (o *GeminiOptions) SafetySettingFor(category string) string {
	if o == nil || o.SafetySetting == nil {
		return ""
	}
	return o.SafetySetting(category)
}

func (o *Options) ShouldPreserveThinkingSuffix(modelName string) bool {
	return o != nil && o.PreserveThinkingSuffix != nil && o.PreserveThinkingSuffix(modelName)
}

func (o *Options) ShouldPreserveEffortTail(modelName string) bool {
	return o != nil && o.PreserveEffortTail != nil && o.PreserveEffortTail(modelName)
}

func (o *Options) EffectiveToolLossPolicy() types.ConversionLossPolicy {
	if o == nil || o.ToolLossPolicy == "" {
		return types.ConversionLossPolicyAllow
	}
	return o.ToolLossPolicy
}
