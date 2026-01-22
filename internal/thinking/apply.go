// Package thinking provides unified thinking configuration processing.
package thinking

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

// providerAppliers maps provider names to their ProviderApplier implementations.
var providerAppliers = map[string]ProviderApplier{
	"gemini":      nil,
	"gemini-cli":  nil,
	"claude":      nil,
	"openai":      nil,
	"codex":       nil,
	"iflow":       nil,
	"antigravity": nil,
}

// GetProviderApplier returns the ProviderApplier for the given provider name.
// Returns nil if the provider is not registered.
func GetProviderApplier(provider string) ProviderApplier {
	return providerAppliers[provider]
}

// RegisterProvider registers a provider applier by name.
func RegisterProvider(name string, applier ProviderApplier) {
	providerAppliers[name] = applier
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
//   - toFormat: Target provider format for the request body (gemini, gemini-cli, antigravity, claude, openai, codex, iflow)
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
	// Try provider-specific applier first (e.g., "cerebras"), fallback to format applier (e.g., "openai")
	// This allows providers like Cerebras (which use OpenAI format) to have custom thinking behavior
	applier := GetProviderApplier(providerKey)
	if applier == nil {
		applier = GetProviderApplier(providerFormat)
	}
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
		return applyUserDefinedModel(body, modelInfo, fromFormat, providerFormat, providerKey, suffixResult)
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
func applyUserDefinedModel(body []byte, modelInfo *registry.ModelInfo, fromFormat, toFormat, providerKey string, suffixResult SuffixResult) ([]byte, error) {
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
	} else {
		config = extractThinkingConfig(body, toFormat)
	}

	if !hasThinkingConfig(config) {
		log.WithFields(log.Fields{
			"model":    modelID,
			"provider": toFormat,
		}).Debug("thinking: user-defined model, passthrough (no config) |")
		return body, nil
	}

	applier := GetProviderApplier(providerKey)
	if applier == nil {
		applier = GetProviderApplier(toFormat)
	}
	if applier == nil {
		log.WithFields(log.Fields{
			"model":    modelID,
			"provider": toFormat,
			"key":      providerKey,
		}).Debug("thinking: user-defined model, passthrough (unknown provider) |")
		return body, nil
	}

	log.WithFields(log.Fields{
		"provider": toFormat,
		"key":      providerKey,
		"model":    modelID,
		"mode":     config.Mode,
		"budget":   config.Budget,
		"level":    config.Level,
	}).Debug("thinking: applying config for user-defined model (skip validation)")

	config = normalizeUserDefinedConfig(config, fromFormat, toFormat)
	return applier.Apply(body, config, modelInfo)
}

func normalizeUserDefinedConfig(config ThinkingConfig, fromFormat, toFormat string) ThinkingConfig {
	if config.Mode != ModeLevel {
		return config
	}
	if !isBudgetBasedProvider(toFormat) || !isLevelBasedProvider(fromFormat) {
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
	case "gemini", "gemini-cli", "antigravity":
		return extractGeminiConfig(body, provider)
	case "openai":
		return extractOpenAIConfig(body)
	case "codex":
		return extractCodexConfig(body)
	case "iflow":
		config := extractIFlowConfig(body)
		if hasThinkingConfig(config) {
			return config
		}
		return extractOpenAIConfig(body)
	default:
		return ThinkingConfig{}
	}
}

func hasThinkingConfig(config ThinkingConfig) bool {
	return config.Mode != ModeBudget || config.Budget != 0 || config.Level != ""
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
// For gemini-cli and antigravity providers, the path is prefixed with "request.".
//
// Priority: thinkingLevel is checked first (Gemini 3 format), then thinkingBudget (Gemini 2.5 format).
// This allows newer Gemini 3 level-based configs to take precedence.
func extractGeminiConfig(body []byte, provider string) ThinkingConfig {
	prefix := "generationConfig.thinkingConfig"
	if provider == "gemini-cli" || provider == "antigravity" {
		prefix = "request.generationConfig.thinkingConfig"
	}

	// Check thinkingLevel first (Gemini 3 format takes precedence)
	if level := gjson.GetBytes(body, prefix+".thinkingLevel"); level.Exists() {
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
	if budget := gjson.GetBytes(body, prefix+".thinkingBudget"); budget.Exists() {
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

// extractIFlowConfig extracts thinking configuration from iFlow format request body.
//
// iFlow API format (supports multiple model families):
//   - GLM format: chat_template_kwargs.enable_thinking (boolean)
//   - MiniMax format: reasoning_split (boolean)
//
// Returns ModeBudget with Budget=1 as a sentinel value indicating "enabled".
// The actual budget/configuration is determined by the iFlow applier based on model capabilities.
// Budget=1 is used because iFlow models don't use numeric budgets; they only support on/off.
func extractIFlowConfig(body []byte) ThinkingConfig {
	// GLM format: chat_template_kwargs.enable_thinking
	if enabled := gjson.GetBytes(body, "chat_template_kwargs.enable_thinking"); enabled.Exists() {
		if enabled.Bool() {
			// Budget=1 is a sentinel meaning "enabled" (iFlow doesn't use numeric budgets)
			return ThinkingConfig{Mode: ModeBudget, Budget: 1}
		}
		return ThinkingConfig{Mode: ModeNone, Budget: 0}
	}

	// MiniMax format: reasoning_split
	if split := gjson.GetBytes(body, "reasoning_split"); split.Exists() {
		if split.Bool() {
			// Budget=1 is a sentinel meaning "enabled" (iFlow doesn't use numeric budgets)
			return ThinkingConfig{Mode: ModeBudget, Budget: 1}
		}
		return ThinkingConfig{Mode: ModeNone, Budget: 0}
	}

	return ThinkingConfig{}
}
