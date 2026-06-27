// Package thinking provides unified thinking configuration processing.
package thinking

import (
	"strings"
	"sync"

	"github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/registry"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

type pluginProviderApplier struct {
	owner    string
	priority int
	applier  ProviderApplier
}

var providerAppliersMu sync.RWMutex

// nativeProviderAppliers maps built-in provider names to their implementations.
var nativeProviderAppliers = map[string]ProviderApplier{
	"gemini":      nil,
	"claude":      nil,
	"openai":      nil,
	"codex":       nil,
	"antigravity": nil,
	"kimi":        nil,
	"xai":         nil,
}

// pluginProviderAppliers maps plugin-owned provider names to their implementations.
var pluginProviderAppliers = map[string]pluginProviderApplier{}

// GetProviderApplier returns the ProviderApplier for the given provider name.
// Returns nil if the provider is not registered.
func GetProviderApplier(provider string) ProviderApplier {
	provider = normalizedProviderName(provider)
	if provider == "" {
		return nil
	}
	providerAppliersMu.RLock()
	defer providerAppliersMu.RUnlock()
	if nativeApplier, okNative := nativeProviderAppliers[provider]; okNative {
		return nativeApplier
	}
	return pluginProviderAppliers[provider].applier
}

// RegisterProvider registers a provider applier by name.
func RegisterProvider(name string, applier ProviderApplier) {
	name = normalizedProviderName(name)
	if name == "" {
		return
	}
	providerAppliersMu.Lock()
	defer providerAppliersMu.Unlock()
	nativeProviderAppliers[name] = applier
}

// RegisterPluginProvider registers a plugin-owned provider applier.
func RegisterPluginProvider(owner string, name string, priority int, applier ProviderApplier) bool {
	owner = strings.TrimSpace(owner)
	name = normalizedProviderName(name)
	if owner == "" || name == "" || applier == nil {
		return false
	}
	providerAppliersMu.Lock()
	defer providerAppliersMu.Unlock()
	if _, native := nativeProviderAppliers[name]; native {
		return false
	}
	current, exists := pluginProviderAppliers[name]
	if exists && (current.priority > priority || (current.priority == priority && current.owner <= owner)) {
		return false
	}
	pluginProviderAppliers[name] = pluginProviderApplier{
		owner:    owner,
		priority: priority,
		applier:  applier,
	}
	return true
}

// UnregisterPluginProviders removes all provider appliers owned by one plugin.
func UnregisterPluginProviders(owner string) {
	owner = strings.TrimSpace(owner)
	if owner == "" {
		return
	}
	providerAppliersMu.Lock()
	defer providerAppliersMu.Unlock()
	for provider, record := range pluginProviderAppliers {
		if record.owner == owner {
			delete(pluginProviderAppliers, provider)
		}
	}
}

// ClearPluginProviders removes all plugin-owned provider appliers.
func ClearPluginProviders() {
	providerAppliersMu.Lock()
	defer providerAppliersMu.Unlock()
	pluginProviderAppliers = map[string]pluginProviderApplier{}
}

func normalizedProviderName(provider string) string {
	return strings.ToLower(strings.TrimSpace(provider))
}

// IsUserDefinedModel reports whether the model is a user-defined model that should
// have thinking configuration passed through without validation.
//
// User-defined models are configured via config file's models[] array
// (e.g., openai-compatibility.*.models[], *-api-key.models[]). These models
// are marked with UserDefined=true at registration time.
//
// User-defined models should have their thinking configuration applied directly,
// letting the upstream service validate the configuration.
func IsUserDefinedModel(modelInfo *registry.ModelInfo) bool {
	if modelInfo == nil {
		return true
	}
	return modelInfo.UserDefined
}

// ApplyThinking applies thinking configuration to a request body.
//
// This is the unified entry point for all providers. It follows the processing
// order defined in FR25: route check → model capability query → config extraction
// → validation → application.
//
// Suffix Priority: When the model name includes a thinking suffix (e.g., "gemini-2.5-pro(8192)"),
// the suffix configuration takes priority over any thinking parameters in the request body.
// This enables users to override thinking settings via the model name without modifying their
// request payload.
//
// Parameters:
//   - body: Original request body JSON
//   - model: Model name, optionally with thinking suffix (e.g., "claude-sonnet-4-5(16384)")
//   - fromFormat: Source request format (e.g., openai, codex, gemini)
//   - toFormat: Target provider format for the request body (gemini, antigravity, claude, openai, codex, kimi, xai)
//   - providerKey: Provider identifier used for registry model lookups (may differ from toFormat, e.g., openrouter -> openai)
//
// Returns:
//   - Modified request body JSON with thinking configuration applied
//   - Error if validation fails (ThinkingError). On error, the original body
//     is returned (not nil) to enable defensive programming patterns.
//
// Passthrough behavior (returns original body without error):
//   - Unknown provider (not in providerAppliers map)
//   - modelInfo.Thinking is nil (model doesn't support thinking)
//
// Note: Unknown models (modelInfo is nil) are treated as user-defined models: we skip
// validation and still apply the thinking config so the upstream can validate it.
//
// Example:
//
//	// With suffix - suffix config takes priority
//	result, err := thinking.ApplyThinking(body, "gemini-2.5-pro(8192)", "gemini", "gemini", "gemini")
//
//	// Without suffix - uses body config
//	result, err := thinking.ApplyThinking(body, "gemini-2.5-pro", "gemini", "gemini", "gemini")
func ApplyThinking(body []byte, model string, fromFormat string, toFormat string, providerKey string) ([]byte, error) {
	providerFormat := strings.ToLower(strings.TrimSpace(toFormat))
	providerKey = strings.ToLower(strings.TrimSpace(providerKey))
	if providerKey == "" {
		providerKey = providerFormat
	}
	fromFormat = strings.ToLower(strings.TrimSpace(fromFormat))
	if fromFormat == "" {
		fromFormat = providerFormat
	}
	// 1. Route check: Get provider applier
	applier := GetProviderApplier(providerFormat)
	if applier == nil {
		log.WithFields(log.Fields{
			"provider": providerFormat,
			"model":    model,
		}).Debug("thinking: unknown provider, passthrough |")
		return body, nil
	}

	// 2. Parse suffix and get modelInfo
	suffixResult := ParseSuffix(model)
	baseModel := suffixResult.ModelName
	// Use provider-specific lookup to handle capability differences across providers.
	modelInfo := registry.LookupModelInfo(baseModel, providerKey)

	// 3. Model capability check
	// Unknown models are treated as user-defined so thinking config can still be applied.
	// The upstream service is responsible for validating the configuration.
	if IsUserDefinedModel(modelInfo) {
		return applyUserDefinedModel(body, modelInfo, fromFormat, providerFormat, suffixResult)
	}
	if modelInfo.Thinking == nil {
		config := extractThinkingConfig(body, providerFormat)
		if hasThinkingConfig(config) {
			log.WithFields(log.Fields{
				"model":    baseModel,
				"provider": providerFormat,
			}).Debug("thinking: model does not support thinking, stripping config |")
			return StripThinkingConfig(body, providerFormat), nil
		}
		log.WithFields(log.Fields{
			"provider": providerFormat,
			"model":    baseModel,
		}).Debug("thinking: model does not support thinking, passthrough |")
		return body, nil
	}

	// 4. Get config: suffix priority over body
	var config ThinkingConfig
	if suffixResult.HasSuffix {
		config = parseSuffixToConfig(suffixResult.RawSuffix, providerFormat, model)
		log.WithFields(log.Fields{
			"provider": providerFormat,
			"model":    model,
			"mode":     config.Mode,
			"budget":   config.Budget,
			"level":    config.Level,
		}).Debug("thinking: config from model suffix |")
	} else {
		config = extractThinkingConfig(body, providerFormat)
		if hasThinkingConfig(config) {
			log.WithFields(log.Fields{
				"provider": providerFormat,
				"model":    modelInfo.ID,
				"mode":     config.Mode,
				"budget":   config.Budget,
				"level":    config.Level,
			}).Debug("thinking: original config from request |")
		}
	}

	if !hasThinkingConfig(config) {
		log.WithFields(log.Fields{
			"provider": providerFormat,
			"model":    modelInfo.ID,
		}).Debug("thinking: no config found, passthrough |")
		return body, nil
	}

	// 5. Validate and normalize configuration
	validated, err := ValidateConfig(config, modelInfo, fromFormat, providerFormat, suffixResult.HasSuffix)
	if err != nil {
		log.WithFields(log.Fields{
			"provider": providerFormat,
			"model":    modelInfo.ID,
			"error":    err.Error(),
		}).Warn("thinking: validation failed |")
		// Return original body on validation failure (defensive programming).
		// This ensures callers who ignore the error won't receive nil body.
		// The upstream service will decide how to handle the unmodified request.
		return body, err
	}

	// Defensive check: ValidateConfig should never return (nil, nil)
	if validated == nil {
		log.WithFields(log.Fields{
			"provider": providerFormat,
			"model":    modelInfo.ID,
		}).Warn("thinking: ValidateConfig returned nil config without error, passthrough |")
		return body, nil
	}

	log.WithFields(log.Fields{
		"provider": providerFormat,
		"model":    modelInfo.ID,
		"mode":     validated.Mode,
		"budget":   validated.Budget,
		"level":    validated.Level,
	}).Debug("thinking: processed config to apply |")

	// 6. Apply configuration using provider-specific applier
	return applier.Apply(body, *validated, modelInfo)
}

// parseSuffixToConfig converts a raw suffix string to ThinkingConfig.
//
// Parsing priority:
//  1. Special values: "none" → ModeNone, "auto"/"-1" → ModeAuto
//  2. Level names: "minimal", "low", "medium", "high", "xhigh" → ModeLevel
//  3. Numeric values: positive integers → ModeBudget, 0 → ModeNone
//
// If none of the above match, returns empty ThinkingConfig (treated as no config).
func parseSuffixToConfig(rawSuffix, provider, model string) ThinkingConfig {
	// 1. Try special values first (none, auto, -1)
	if mode, ok := ParseSpecialSuffix(rawSuffix); ok {
		switch mode {
		case ModeNone:
			return ThinkingConfig{Mode: ModeNone, Budget: 0}
		case ModeAuto:
			return ThinkingConfig{Mode: ModeAuto, Budget: -1}
		}
	}

	// 2. Try level parsing (minimal, low, medium, high, xhigh)
	if level, ok := ParseLevelSuffix(rawSuffix); ok {
		return ThinkingConfig{Mode: ModeLevel, Level: level}
	}

	// 3. Try numeric parsing
	if budget, ok := ParseNumericSuffix(rawSuffix); ok {
		if budget == 0 {
			return ThinkingConfig{Mode: ModeNone, Budget: 0}
		}
		return ThinkingConfig{Mode: ModeBudget, Budget: budget}
	}

	// Unknown suffix format - return empty config
	log.WithFields(log.Fields{
		"provider":   provider,
		"model":      model,
		"raw_suffix": rawSuffix,
	}).Debug("thinking: unknown suffix format, treating as no config |")
	return ThinkingConfig{}
}

// applyUserDefinedModel applies thinking configuration for user-defined models
// without ThinkingSupport validation.
func applyUserDefinedModel(body []byte, modelInfo *registry.ModelInfo, fromFormat, toFormat string, suffixResult SuffixResult) ([]byte, error) {
	// Get model ID for logging
	modelID := ""
	if modelInfo != nil {
		modelID = modelInfo.ID
	} else {
		modelID = suffixResult.ModelName
	}

	// Get config: suffix priority over body
	var config ThinkingConfig
	if suffixResult.HasSuffix {
		config = parseSuffixToConfig(suffixResult.RawSuffix, toFormat, modelID)
		log.WithFields(log.Fields{
			"provider": toFormat,
			"model":    modelID,
			"mode":     config.Mode,
			"budget":   config.Budget,
			"level":    config.Level,
		}).Debug("thinking: config from model suffix |")
	} else {
		config = extractThinkingConfig(body, fromFormat)
		if !hasThinkingConfig(config) && fromFormat != toFormat {
			config = extractThinkingConfig(body, toFormat)
		}
		if hasThinkingConfig(config) {
			log.WithFields(log.Fields{
				"provider": toFormat,
				"model":    modelID,
				"mode":     config.Mode,
				"budget":   config.Budget,
				"level":    config.Level,
			}).Debug("thinking: original config from request |")
		}
	}

	if !hasThinkingConfig(config) {
		log.WithFields(log.Fields{
			"model":    modelID,
			"provider": toFormat,
		}).Debug("thinking: user-defined model, passthrough (no config) |")
		return body, nil
	}

	applier := GetProviderApplier(toFormat)
	if applier == nil {
		log.WithFields(log.Fields{
			"model":    modelID,
			"provider": toFormat,
		}).Debug("thinking: user-defined model, passthrough (unknown provider) |")
		return body, nil
	}

	config = normalizeUserDefinedConfig(config, fromFormat, toFormat)
	log.WithFields(log.Fields{
		"provider": toFormat,
		"model":    modelID,
		"mode":     config.Mode,
		"budget":   config.Budget,
		"level":    config.Level,
	}).Debug("thinking: processed config to apply |")
	return applier.Apply(body, config, modelInfo)
}

func normalizeUserDefinedConfig(config ThinkingConfig, fromFormat, toFormat string) ThinkingConfig {
	if config.Mode != ModeLevel {
		return config
	}
	if toFormat == "claude" {
		return config
	}
	if !isBudgetCapableProvider(toFormat) {
		return config
	}
	budget, ok := ConvertLevelToBudget(string(config.Level))
	if !ok {
		return config
	}
	config.Mode = ModeBudget
	config.Budget = budget
	config.Level = ""
	return config
}

// extractThinkingConfig extracts provider-specific thinking config from request body.
func extractThinkingConfig(body []byte, provider string) ThinkingConfig {
	if len(body) == 0 || !gjson.ValidBytes(body) {
		return ThinkingConfig{}
	}

	switch provider {
	case "claude":
		return extractClaudeConfig(body)
	case "gemini", "antigravity":
		return extractGeminiConfig(body, provider)
	case "openai":
		return extractOpenAIConfig(body)
	case "codex", "xai":
		return extractCodexConfig(body)
	case "kimi":
		// Kimi uses OpenAI-compatible reasoning_effort format
		return extractOpenAIConfig(body)
	default:
		return ThinkingConfig{}
	}
}

func hasThinkingConfig(config ThinkingConfig) bool {
	return config.Mode != ModeBudget || config.Budget != 0 || config.Level != ""
}

// ExtractReasoningEffort returns the request's thinking setting as a canonical
// reasoning_effort label for usage logging. Model suffixes have the same
// priority as ApplyThinking: a valid suffix overrides body fields.
func ExtractReasoningEffort(body []byte, provider, model string) string {
	if effort := reasoningEffortFromSuffix(ParseSuffix(model)); effort != "" {
		return effort
	}

	provider = strings.ToLower(strings.TrimSpace(provider))
	config := extractThinkingConfig(body, provider)
	if !hasThinkingConfig(config) {
		switch provider {
		case "openai-response":
			config = extractCodexConfig(body)
		case "openai":
			config = extractCodexConfig(body)
		}
	}
	return reasoningEffortFromConfig(config)
}

// ExtractTranslatedReasoningEffort returns the final provider payload's thinking
// setting as a canonical reasoning_effort label for usage logging.
func ExtractTranslatedReasoningEffort(body []byte, provider string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	config := extractThinkingConfig(body, provider)
	if !hasThinkingConfig(config) {
		switch provider {
		case "openai", "openai-response":
			config = extractCodexConfig(body)
			if !hasThinkingConfig(config) {
				config = extractOpenAIConfig(body)
			}
		}
	}
	return reasoningEffortFromConfig(config)
}

func reasoningEffortFromSuffix(suffix SuffixResult) string {
	if !suffix.HasSuffix {
		return ""
	}
	return reasoningEffortFromConfig(parseSuffixToConfig(suffix.RawSuffix, "", suffix.ModelName))
}

func reasoningEffortFromConfig(config ThinkingConfig) string {
	if !hasThinkingConfig(config) {
		return ""
	}
	switch config.Mode {
	case ModeNone:
		return string(LevelNone)
	case ModeAuto:
		return string(LevelAuto)
	case ModeLevel:
		return strings.ToLower(strings.TrimSpace(string(config.Level)))
	case ModeBudget:
		level, ok := ConvertBudgetToLevel(config.Budget)
		if !ok {
			return ""
		}
		return level
	default:
		return ""
	}
}

// extractClaudeConfig extracts thinking configuration from Claude format request body.
//
// Claude API format:
//   - thinking.type: "enabled" or "disabled"
//   - thinking.budget_tokens: integer (-1=auto, 0=disabled, >0=budget)
//
// Priority: thinking.type="disabled" takes precedence over budget_tokens.
// When type="enabled" without budget_tokens, returns ModeAuto to indicate
// the user wants thinking enabled but didn't specify a budget.
func extractClaudeConfig(body []byte) ThinkingConfig {
	thinkingType := gjson.GetBytes(body, "thinking.type").String()
	if thinkingType == "disabled" {
		return ThinkingConfig{Mode: ModeNone, Budget: 0}
	}
	if thinkingType == "adaptive" || thinkingType == "auto" {
		// Claude adaptive thinking uses output_config.effort (low/medium/high/max).
		// We only treat it as a thinking config when effort is explicitly present;
		// otherwise we passthrough and let upstream defaults apply.
		if effort := gjson.GetBytes(body, "output_config.effort"); effort.Exists() && effort.Type == gjson.String {
			value := strings.ToLower(strings.TrimSpace(effort.String()))
			if value == "" {
				return ThinkingConfig{}
			}
			switch value {
			case "none":
				return ThinkingConfig{Mode: ModeNone, Budget: 0}
			case "auto":
				return ThinkingConfig{Mode: ModeAuto, Budget: -1}
			default:
				return ThinkingConfig{Mode: ModeLevel, Level: ThinkingLevel(value)}
			}
		}
		return ThinkingConfig{}
	}

	// Check budget_tokens
	if budget := gjson.GetBytes(body, "thinking.budget_tokens"); budget.Exists() {
		value := int(budget.Int())
		switch value {
		case 0:
			return ThinkingConfig{Mode: ModeNone, Budget: 0}
		case -1:
			return ThinkingConfig{Mode: ModeAuto, Budget: -1}
		default:
			return ThinkingConfig{Mode: ModeBudget, Budget: value}
		}
	}

	// If type="enabled" but no budget_tokens, treat as auto (user wants thinking but no budget specified)
	if thinkingType == "enabled" {
		return ThinkingConfig{Mode: ModeAuto, Budget: -1}
	}

	return ThinkingConfig{}
}

// extractGeminiConfig extracts thinking configuration from Gemini format request body.
//
// Gemini API format:
//   - generationConfig.thinkingConfig.thinkingLevel: "none", "auto", or level name (Gemini 3)
//   - generationConfig.thinkingConfig.thinkingBudget: integer (Gemini 2.5)
//
// For antigravity providers, the path is prefixed with "request.".
//
// Priority: thinkingLevel is checked first (Gemini 3 format), then thinkingBudget (Gemini 2.5 format).
// This allows newer Gemini 3 level-based configs to take precedence.
func extractGeminiConfig(body []byte, provider string) ThinkingConfig {
	prefix := "generationConfig.thinkingConfig"
	if provider == "antigravity" {
		prefix = "request.generationConfig.thinkingConfig"
	}

	// Check thinkingLevel first (Gemini 3 format takes precedence)
	level := gjson.GetBytes(body, prefix+".thinkingLevel")
	if !level.Exists() {
		// Google official Gemini Python SDK sends snake_case field names
		level = gjson.GetBytes(body, prefix+".thinking_level")
	}
	if level.Exists() {
		value := level.String()
		switch value {
		case "none":
			return ThinkingConfig{Mode: ModeNone, Budget: 0}
		case "auto":
			return ThinkingConfig{Mode: ModeAuto, Budget: -1}
		default:
			return ThinkingConfig{Mode: ModeLevel, Level: ThinkingLevel(value)}
		}
	}

	// Check thinkingBudget (Gemini 2.5 format)
	budget := gjson.GetBytes(body, prefix+".thinkingBudget")
	if !budget.Exists() {
		// Google official Gemini Python SDK sends snake_case field names
		budget = gjson.GetBytes(body, prefix+".thinking_budget")
	}
	if budget.Exists() {
		value := int(budget.Int())
		switch value {
		case 0:
			return ThinkingConfig{Mode: ModeNone, Budget: 0}
		case -1:
			return ThinkingConfig{Mode: ModeAuto, Budget: -1}
		default:
			return ThinkingConfig{Mode: ModeBudget, Budget: value}
		}
	}

	return ThinkingConfig{}
}

// extractOpenAIConfig extracts thinking configuration from OpenAI format request body.
//
// OpenAI API format:
//   - reasoning_effort: "none", "low", "medium", "high" (discrete levels)
//
// OpenAI uses level-based thinking configuration only, no numeric budget support.
// The "none" value is treated specially to return ModeNone.
func extractOpenAIConfig(body []byte) ThinkingConfig {
	// Check reasoning_effort (OpenAI Chat Completions format)
	if effort := gjson.GetBytes(body, "reasoning_effort"); effort.Exists() {
		value := effort.String()
		if value == "none" {
			return ThinkingConfig{Mode: ModeNone, Budget: 0}
		}
		return ThinkingConfig{Mode: ModeLevel, Level: ThinkingLevel(value)}
	}

	return ThinkingConfig{}
}

// extractCodexConfig extracts thinking configuration from Codex format request body.
//
// Codex API format (OpenAI Responses API):
//   - reasoning.effort: "none", "low", "medium", "high"
//
// This is similar to OpenAI but uses nested field "reasoning.effort" instead of "reasoning_effort".
func extractCodexConfig(body []byte) ThinkingConfig {
	// Check reasoning.effort (Codex / OpenAI Responses API format)
	if effort := gjson.GetBytes(body, "reasoning.effort"); effort.Exists() {
		value := effort.String()
		if value == "none" {
			return ThinkingConfig{Mode: ModeNone, Budget: 0}
		}
		return ThinkingConfig{Mode: ModeLevel, Level: ThinkingLevel(value)}
	}

	return ThinkingConfig{}
}

func extractIFlowConfig(body []byte) ThinkingConfig {
	effort := gjson.GetBytes(body, "reasoning.effort")
	if !effort.Exists() {
		effort = gjson.GetBytes(body, "reasoning\\.effort")
	}
	if !effort.Exists() {
		return ThinkingConfig{}
	}
	value := strings.TrimSpace(effort.String())
	if value == "" {
		return ThinkingConfig{}
	}
	if value == "none" {
		return ThinkingConfig{Mode: ModeNone, Budget: 0}
	}
	return ThinkingConfig{Mode: ModeLevel, Level: ThinkingLevel(value)}
}
