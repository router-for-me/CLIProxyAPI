// Package thinking provides unified thinking configuration processing.
package thinking

import (
	"strings"
	"sync"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
	log "github.com/sirupsen/logrus"
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
	"kimi-ai":     nil,
	"kimi.ai":     nil,
	"kimi.com":    nil,
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

func getPluginProviderApplier(provider string) ProviderApplier {
	provider = normalizedProviderName(provider)
	if provider == "" {
		return nil
	}
	providerAppliersMu.RLock()
	defer providerAppliersMu.RUnlock()
	return pluginProviderAppliers[provider].applier
}

func resolveProviderApplier(providerFormat, providerKey string) (ProviderApplier, bool) {
	if applier := getPluginProviderApplier(providerKey); applier != nil {
		return applier, true
	}
	if applier := getPluginProviderApplier(providerFormat); applier != nil {
		return applier, true
	}
	return GetProviderApplier(providerFormat), false
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
func IsUserDefinedModel(modelInfo *registry.ModelInfo) bool {
	if modelInfo == nil {
		return true
	}
	return modelInfo.UserDefined
}

// ApplyThinking applies thinking configuration to a request body.
func ApplyThinking(body []byte, model string, fromFormat string, toFormat string, providerKey string) ([]byte, error) {
	summaryConfig := ExtractSummaryConfig(body, toFormat)
	return applyThinking(body, nil, model, fromFormat, toFormat, providerKey, nil, false, summaryConfig)
}

// ApplyThinkingDoc applies thinking configuration to doc in place.
func ApplyThinkingDoc(doc *ojson.Doc, model, fromFormat, toFormat, providerKey string) error {
	if doc == nil {
		return nil
	}
	summaryConfig := ExtractSummaryConfigDoc(doc, toFormat)
	_, err := applyThinkingDocCore(doc, doc.Len() > 0, nil, model, fromFormat, toFormat, providerKey, nil, false, summaryConfig)
	return err
}

// ApplyThinkingWithSummary applies canonical thinking effort while preserving
// summary visibility extracted from the original source request.
func ApplyThinkingWithSummary(body []byte, model string, fromFormat string, toFormat string, providerKey string, summaryConfig SummaryConfig) ([]byte, error) {
	return applyThinking(body, nil, model, fromFormat, toFormat, providerKey, nil, false, summaryConfig)
}

// ApplyThinkingWithSummaryDoc applies canonical thinking effort onto doc in place
// while preserving summary visibility extracted from the original source request.
func ApplyThinkingWithSummaryDoc(doc *ojson.Doc, model, fromFormat, toFormat, providerKey string, summaryConfig SummaryConfig) error {
	if doc == nil {
		return nil
	}
	_, err := applyThinkingDocCore(doc, doc.Len() > 0, nil, model, fromFormat, toFormat, providerKey, nil, false, summaryConfig)
	return err
}

// ApplyThinkingWithSourceAndSummary applies thinking using the original source
// request when translation has already changed the target protocol.
func ApplyThinkingWithSourceAndSummary(body, sourceBody []byte, model, fromFormat, toFormat, providerKey string, summaryConfig SummaryConfig, normalizedUpdatesChanged ...bool) ([]byte, error) {
	return applyThinking(body, sourceBody, model, fromFormat, toFormat, providerKey, nil, false, summaryConfig, normalizedUpdatesChanged...)
}

// ApplyThinkingWithSourceAndSummaryDoc applies thinking onto doc in place using
// the original source request.
func ApplyThinkingWithSourceAndSummaryDoc(doc *ojson.Doc, sourceBody []byte, model, fromFormat, toFormat, providerKey string, summaryConfig SummaryConfig, normalizedUpdatesChanged ...bool) error {
	if doc == nil {
		return nil
	}
	_, err := applyThinkingDocCore(doc, doc.Len() > 0, sourceBody, model, fromFormat, toFormat, providerKey, nil, false, summaryConfig, normalizedUpdatesChanged...)
	return err
}

// ApplyThinkingWithFallbackModelInfoAndSummary applies thinking using the global
// registry first and falls back to fallbackModelInfo when the registry has no
// entry for the model.
func ApplyThinkingWithFallbackModelInfoAndSummary(body, sourceBody []byte, model, fromFormat, toFormat, providerKey string, fallbackModelInfo *registry.ModelInfo, summaryConfig SummaryConfig, normalizedUpdatesChanged ...bool) ([]byte, error) {
	return applyThinking(body, sourceBody, model, fromFormat, toFormat, providerKey, fallbackModelInfo, false, summaryConfig, normalizedUpdatesChanged...)
}

// ApplyThinkingWithFallbackModelInfoAndSummaryDoc applies thinking onto doc in place
// using the global registry first and falling back to fallbackModelInfo.
func ApplyThinkingWithFallbackModelInfoAndSummaryDoc(doc *ojson.Doc, sourceBody []byte, model, fromFormat, toFormat, providerKey string, fallbackModelInfo *registry.ModelInfo, summaryConfig SummaryConfig, normalizedUpdatesChanged ...bool) error {
	if doc == nil {
		return nil
	}
	_, err := applyThinkingDocCore(doc, doc.Len() > 0, sourceBody, model, fromFormat, toFormat, providerKey, fallbackModelInfo, false, summaryConfig, normalizedUpdatesChanged...)
	return err
}

// ApplyThinkingWithModelInfo applies thinking with the exact configured model
// definition selected for an API-key execution attempt while preserving summary
// visibility from the original source body.
func ApplyThinkingWithModelInfo(body, sourceBody []byte, model string, fromFormat string, toFormat string, providerKey string, modelInfo *registry.ModelInfo) ([]byte, error) {
	summaryConfig := ExtractSummaryConfig(sourceBody, fromFormat)
	if len(sourceBody) == 0 {
		summaryConfig = ExtractSummaryConfig(body, toFormat)
	}
	return ApplyThinkingWithModelInfoAndSummary(body, sourceBody, model, fromFormat, toFormat, providerKey, modelInfo, summaryConfig)
}

// ApplyThinkingWithModelInfoDoc applies thinking onto doc in place with the exact
// configured model definition.
func ApplyThinkingWithModelInfoDoc(doc *ojson.Doc, sourceBody []byte, model string, fromFormat string, toFormat string, providerKey string, modelInfo *registry.ModelInfo) error {
	if doc == nil {
		return nil
	}
	summaryConfig := ExtractSummaryConfig(sourceBody, fromFormat)
	if len(sourceBody) == 0 {
		summaryConfig = ExtractSummaryConfigDoc(doc, toFormat)
	}
	return ApplyThinkingWithModelInfoAndSummaryDoc(doc, sourceBody, model, fromFormat, toFormat, providerKey, modelInfo, summaryConfig)
}

// ApplyThinkingWithModelInfoAndSummary applies the exact configured model
// definition with a summary intent already resolved across source translation
// and plugin normalization.
func ApplyThinkingWithModelInfoAndSummary(body, sourceBody []byte, model string, fromFormat string, toFormat string, providerKey string, modelInfo *registry.ModelInfo, summaryConfig SummaryConfig, normalizedUpdatesChanged ...bool) ([]byte, error) {
	return applyThinking(body, sourceBody, model, fromFormat, toFormat, providerKey, modelInfo, true, summaryConfig, normalizedUpdatesChanged...)
}

// ApplyThinkingWithModelInfoAndSummaryDoc applies the exact configured model
// definition onto doc in place with a pre-resolved summary intent.
func ApplyThinkingWithModelInfoAndSummaryDoc(doc *ojson.Doc, sourceBody []byte, model string, fromFormat string, toFormat string, providerKey string, modelInfo *registry.ModelInfo, summaryConfig SummaryConfig, normalizedUpdatesChanged ...bool) error {
	if doc == nil {
		return nil
	}
	_, err := applyThinkingDocCore(doc, doc.Len() > 0, sourceBody, model, fromFormat, toFormat, providerKey, modelInfo, true, summaryConfig, normalizedUpdatesChanged...)
	return err
}

func applyThinking(body, sourceBody []byte, model string, fromFormat string, toFormat string, providerKey string, resolvedModelInfo *registry.ModelInfo, modelInfoResolved bool, summaryConfig SummaryConfig, normalizedUpdatesChanged ...bool) ([]byte, error) {
	docValid := len(body) > 0 && ojson.ValidBytes(body)
	doc := ojson.NewDoc(body)
	modified, err := applyThinkingDocCore(doc, docValid, sourceBody, model, fromFormat, toFormat, providerKey, resolvedModelInfo, modelInfoResolved, summaryConfig, normalizedUpdatesChanged...)
	if !modified {
		return body, err
	}
	return doc.Bytes(), err
}

func applyThinkingDocCore(doc *ojson.Doc, docValid bool, sourceBody []byte, model string, fromFormat string, toFormat string, providerKey string, resolvedModelInfo *registry.ModelInfo, modelInfoResolved bool, summaryConfig SummaryConfig, normalizedUpdatesChanged ...bool) (bool, error) {
	if doc == nil {
		return false, nil
	}
	modified := false
	providerFormat := strings.ToLower(strings.TrimSpace(toFormat))
	if providerFormat == "openai-response" {
		providerFormat = "codex"
	}
	providerKey = strings.ToLower(strings.TrimSpace(providerKey))
	if providerKey == "" {
		providerKey = providerFormat
	}
	fromFormat = strings.ToLower(strings.TrimSpace(fromFormat))
	if fromFormat == "" {
		fromFormat = providerFormat
	}
	// 1. Parse suffix and get modelInfo
	suffixResult := ParseSuffix(model)
	baseModel := suffixResult.ModelName
	modelInfo := resolvedModelInfo
	if !modelInfoResolved {
		if lookedUp := registry.LookupModelInfo(baseModel, providerKey); lookedUp != nil {
			modelInfo = lookedUp
		}
	}

	// Resolve source intent before stripping unsupported target input items.
	updatesChanged := len(normalizedUpdatesChanged) > 0 && normalizedUpdatesChanged[0]
	var sourceConfig ThinkingConfig
	if isResponsesFormat(fromFormat) {
		if !updatesChanged || providerFormat == "codex" || providerFormat == "xai" {
			if !updatesChanged && len(sourceBody) > 0 {
				sourceConfig = extractCodexUsageConfig(sourceBody)
			} else {
				sourceConfig = extractCodexUsageConfigDoc(doc, docValid)
			}
		}
	}
	responseTarget := providerFormat == "codex" || providerFormat == "xai"
	supportsUpdates := modelInfo != nil && modelInfo.SupportConfigurationUpdate
	if responseTarget && !supportsUpdates {
		if stripConfigurationUpdatesDoc(doc, docValid) {
			modified = true
		}
	}
	nativeResponses := responseTarget && isResponsesFormat(fromFormat) && supportsUpdates

	// 2. Route check: Get provider applier after target cleanup.
	applier, isPluginApplier := resolveProviderApplier(providerFormat, providerKey)
	if applier == nil {
		log.WithFields(log.Fields{
			"provider": providerFormat,
			"model":    model,
		}).Debug("thinking: unknown provider, passthrough |")
		return modified, nil
	}

	// 3. Model capability check
	if nativeResponses {
		normalized, errNormalize := normalizeNativeResponsesEffortDoc(doc, docValid, modelInfo, applier, fromFormat, providerFormat, HasThinkingSuffix(model))
		modified = modified || normalized
		if errNormalize != nil {
			return modified, errNormalize
		}
	}
	if !suffixResult.HasSuffix && len(sourceBody) > 0 && isResponsesFormat(fromFormat) && !docValid && hasThinkingConfig(extractConfigurationUpdateConfig(sourceBody)) {
		// Do not rebuild a malformed target from a separate source update.
		return modified, nil
	}
	if nativeResponses && !suffixResult.HasSuffix {
		// Native Responses preserves the baseline and update timeline after validation.
		if log.IsLevelEnabled(log.DebugLevel) && (modelInfo.Thinking != nil || modelInfo.UserDefined) {
			if config := extractCodexUsageConfigDoc(doc, docValid); hasThinkingConfig(config) {
				fields := log.Fields{
					"provider": providerFormat,
					"model":    modelInfo.ID,
					"mode":     config.Mode,
					"budget":   config.Budget,
					"level":    config.Level,
				}
				if baseline := extractCodexConfigFromGetter(doc.Get); baseline.Mode == ModeLevel {
					fields["baseline_level"] = baseline.Level
				}
				entry := log.WithFields(fields)
				entry.Debug("thinking: original config from request |")
				entry.Debug("thinking: processed config to apply |")
			}
		}
		return modified, nil
	}
	if !isPluginApplier && fromFormat == "claude" && providerFormat == "claude" && !suffixResult.HasSuffix && (!docValid || !doc.Get("thinking").Exists()) {
		// Native Claude effort is independent of thinking activation.
		if applySummaryConfigForProviderDoc(doc, docValid, providerFormat, baseModel, providerKey, modelInfo, summaryConfig) {
			modified = true
		}
		return modified, nil
	}
	if IsUserDefinedModel(modelInfo) || (isPluginApplier && modelInfo.Thinking == nil) {
		userMod, errUser := applyUserDefinedModelDoc(doc, docValid, modelInfo, fromFormat, providerFormat, providerKey, suffixResult, sourceConfig, nativeResponses, summaryConfig)
		return modified || userMod, errUser
	}
	if modelInfo.Thinking == nil {
		config := extractThinkingConfigDoc(doc, docValid, providerFormat)
		if hasThinkingConfig(config) || summaryConfig.Mode != SummaryUnspecified {
			log.WithFields(log.Fields{
				"model":    baseModel,
				"provider": providerFormat,
			}).Debug("thinking: model does not support thinking, stripping config |")
			if responseTarget {
				if stripResponsesEffortDoc(doc, docValid) {
					modified = true
				}
				return modified, nil
			}
			if docValid && stripThinkingFieldsDoc(doc, providerFormat) {
				modified = true
			}
			return modified, nil
		}
		log.WithFields(log.Fields{
			"provider": providerFormat,
			"model":    baseModel,
		}).Debug("thinking: model does not support thinking, passthrough |")
		return modified, nil
	}

	// 4. Get config: suffix priority over body
	var config ThinkingConfig
	if suffixResult.HasSuffix {
		config = parseSuffixToConfig(suffixResult.RawSuffix, providerFormat, model)
		if !hasThinkingConfig(config) && !nativeResponses && strings.TrimSpace(suffixResult.RawSuffix) != "" && !strings.EqualFold(strings.TrimSpace(modelInfo.ID), strings.TrimSpace(model)) {
			config = ThinkingConfig{Mode: ModeLevel, Level: ThinkingLevel(strings.TrimSpace(suffixResult.RawSuffix))}
		}
		log.WithFields(log.Fields{
			"provider": providerFormat,
			"model":    model,
			"mode":     config.Mode,
			"budget":   config.Budget,
			"level":    config.Level,
		}).Debug("thinking: config from model suffix |")
	} else {
		config = sourceConfig
		if !hasThinkingConfig(config) && !updatesChanged && modelInfoResolved && len(sourceBody) > 0 {
			config = extractSourceThinkingConfig(sourceBody, fromFormat)
		}
		if !hasThinkingConfig(config) {
			config = extractThinkingConfigDoc(doc, docValid, providerFormat)
		}
		if !hasThinkingConfig(config) && fromFormat != providerFormat {
			config = extractThinkingConfigDoc(doc, docValid, fromFormat)
		}
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
		if nativeResponses {
			return modified, nil
		}
		if modelInfoResolved && providerFormat == "claude" && fromFormat != providerFormat && ExtractSummaryConfig(sourceBody, fromFormat).Mode == SummaryEnabled {
			if stripInferredClaudeSummaryActivationDoc(doc, docValid, modelInfo) {
				modified = true
			}
		}
		if applySummaryConfigForProviderDoc(doc, docValid, providerFormat, baseModel, providerKey, modelInfo, summaryConfig) {
			modified = true
		}
		return modified, nil
	}
	if modelInfoResolved && config.Mode == ModeLevel && modelInfo != nil && modelInfo.Thinking != nil && shouldMapConfiguredHighIntent(fromFormat, providerFormat, modelInfo) {
		config.Level = mapConfiguredHighIntent(config.Level, modelInfo)
	}

	// 5. Validate and normalize configuration
	validated, err := ValidateConfig(config, modelInfo, fromFormat, providerFormat, suffixResult.HasSuffix)
	if err != nil {
		log.WithFields(log.Fields{
			"provider": providerFormat,
			"model":    modelInfo.ID,
			"error":    err.Error(),
		}).Warn("thinking: validation failed |")
		return modified, err
	}

	if validated == nil {
		log.WithFields(log.Fields{
			"provider": providerFormat,
			"model":    modelInfo.ID,
		}).Warn("thinking: ValidateConfig returned nil config without error, passthrough |")
		return modified, nil
	}

	log.WithFields(log.Fields{
		"provider": providerFormat,
		"model":    modelInfo.ID,
		"mode":     validated.Mode,
		"budget":   validated.Budget,
		"level":    validated.Level,
	}).Debug("thinking: processed config to apply |")

	// 6. Apply configuration using provider-specific applier, then restore the
	// target summary intent that was explicit before suffix processing.
	appliedMod, errApply := invokeProviderApplierOnDoc(applier, doc, &docValid, *validated, modelInfo)
	if appliedMod {
		modified = true
	}
	if errApply != nil {
		return modified, errApply
	}
	if thinkingIsFullyDisabled(*validated) || nativeResponses {
		return modified, nil
	}
	if providerFormat == "claude" && thinkingRequestedOff(config) {
		if doc.Get("thinking.block_binding").Exists() && util.DeleteJSONDocIfExists(doc, "thinking.block_binding") == nil {
			modified = true
		}
		summaryConfig = SummaryConfig{Mode: SummaryDisabled}
	}
	if applySummaryConfigForProviderDoc(doc, docValid, providerFormat, baseModel, providerKey, modelInfo, summaryConfig) {
		modified = true
	}
	return modified, nil
}

func invokeProviderApplierOnDoc(applier ProviderApplier, doc *ojson.Doc, docValid *bool, config ThinkingConfig, modelInfo *registry.ModelInfo) (bool, error) {
	if docApplier, ok := applier.(ProviderDocApplier); ok {
		if !*docValid {
			*doc = *ojson.NewDoc([]byte("{}"))
			*docValid = true
		}
		if errApply := docApplier.ApplyDoc(doc, config, modelInfo); errApply != nil {
			return false, errApply
		}
		return true, nil
	}
	raw := doc.Bytes()
	applied, errApply := applier.Apply(raw, config, modelInfo)
	if errApply != nil {
		return false, errApply
	}
	*doc = *ojson.NewDoc(applied)
	*docValid = len(applied) > 0 && ojson.ValidBytes(applied)
	return true, nil
}

func thinkingIsFullyDisabled(config ThinkingConfig) bool {
	return config.Mode == ModeNone && config.Budget == 0 && config.Level == ""
}

func thinkingRequestedOff(config ThinkingConfig) bool {
	return config.Mode == ModeNone || (config.Mode == ModeBudget && config.Budget == 0)
}

func shouldMapConfiguredHighIntent(fromFormat, toFormat string, modelInfo *registry.ModelInfo) bool {
	fromFormat = strings.ToLower(strings.TrimSpace(fromFormat))
	toFormat = strings.ToLower(strings.TrimSpace(toFormat))
	if fromFormat != toFormat {
		return true
	}
	if modelInfo == nil {
		return false
	}
	modelType := strings.ToLower(strings.TrimSpace(modelInfo.Type))
	return modelType != "" && !isSameProviderFamily(toFormat, modelType)
}

func mapConfiguredHighIntent(level ThinkingLevel, modelInfo *registry.ModelInfo) ThinkingLevel {
	if modelInfo == nil || modelInfo.Thinking == nil || len(modelInfo.Thinking.Levels) == 0 {
		return level
	}
	level = ThinkingLevel(strings.ToLower(strings.TrimSpace(string(level))))
	var candidates []ThinkingLevel
	switch level {
	case LevelXHigh:
		candidates = []ThinkingLevel{LevelXHigh, LevelMax, LevelHigh}
	case LevelMax:
		candidates = []ThinkingLevel{LevelMax, LevelXHigh, LevelHigh}
	default:
		return level
	}
	for _, candidate := range candidates {
		if isLevelSupported(string(candidate), modelInfo.Thinking.Levels) {
			return candidate
		}
	}
	return level
}

func extractSourceThinkingConfig(body []byte, provider string) ThinkingConfig {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if provider == "openai-response" {
		return extractCodexConfig(body)
	}
	return extractThinkingConfig(body, provider)
}

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

	log.WithFields(log.Fields{
		"provider":   provider,
		"model":      model,
		"raw_suffix": rawSuffix,
	}).Debug("thinking: unknown suffix format, treating as no config |")
	return ThinkingConfig{}
}

func applyUserDefinedModelDoc(doc *ojson.Doc, docValid bool, modelInfo *registry.ModelInfo, fromFormat, toFormat, providerKey string, suffixResult SuffixResult, sourceConfig ThinkingConfig, nativeResponses bool, summaryConfig SummaryConfig) (bool, error) {
	modelID := ""
	if modelInfo != nil {
		modelID = modelInfo.ID
	} else {
		modelID = suffixResult.ModelName
	}

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
		config = sourceConfig
		if !hasThinkingConfig(config) {
			config = extractThinkingConfigDoc(doc, docValid, fromFormat)
		}
		if !hasThinkingConfig(config) && fromFormat != toFormat {
			config = extractThinkingConfigDoc(doc, docValid, toFormat)
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
		return applySummaryConfigForProviderDoc(doc, docValid, toFormat, modelID, providerKey, modelInfo, summaryConfig), nil
	}

	applier, isPluginApplier := resolveProviderApplier(toFormat, providerKey)
	if applier == nil {
		log.WithFields(log.Fields{
			"model":    modelID,
			"provider": toFormat,
		}).Debug("thinking: user-defined model, passthrough (unknown provider) |")
		return false, nil
	}

	if !isPluginApplier {
		config = normalizeUserDefinedConfig(config, fromFormat, toFormat)
	}
	log.WithFields(log.Fields{
		"provider": toFormat,
		"model":    modelID,
		"mode":     config.Mode,
		"budget":   config.Budget,
		"level":    config.Level,
	}).Debug("thinking: processed config to apply |")
	modified, err := invokeProviderApplierOnDoc(applier, doc, &docValid, config, modelInfo)
	if err != nil {
		return modified, err
	}
	if thinkingIsFullyDisabled(config) || nativeResponses {
		return modified, nil
	}
	if applySummaryConfigForProviderDoc(doc, docValid, toFormat, modelID, providerKey, modelInfo, summaryConfig) {
		modified = true
	}
	return modified, nil
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
	if len(body) == 0 || !ojson.ValidBytes(body) {
		return ThinkingConfig{}
	}
	return extractThinkingConfigFromGetter(func(path string) ojson.Result {
		return ojson.GetBytesNoCopy(body, path)
	}, provider)
}

func extractThinkingConfigDoc(doc *ojson.Doc, docValid bool, provider string) ThinkingConfig {
	if doc == nil || !docValid {
		return ThinkingConfig{}
	}
	return extractThinkingConfigFromGetter(doc.Get, provider)
}

func extractThinkingConfigFromGetter(get func(string) ojson.Result, provider string) ThinkingConfig {
	switch provider {
	case "claude":
		return extractClaudeConfigFromGetter(get)
	case "gemini", "antigravity":
		return extractGeminiConfigFromGetter(get, provider)
	case "interactions":
		return extractInteractionsConfigFromGetter(get)
	case "openai":
		return extractOpenAIConfigFromGetter(get)
	case "codex", "xai":
		return extractCodexConfigFromGetter(get)
	case "kimi", "kimi-ai", "kimi.ai", "kimi.com":
		return extractKimiConfigFromGetter(get)
	default:
		return ThinkingConfig{}
	}
}

func hasThinkingConfig(config ThinkingConfig) bool {
	return config.Mode != ModeBudget || config.Budget != 0 || config.Level != ""
}

// ExtractReasoningEffort returns the source request's thinking setting as a
// canonical reasoning_effort label for usage logging.
func ExtractReasoningEffort(body []byte, provider, model string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	if isResponsesFormat(provider) {
		if effort := reasoningEffortFromConfig(extractConfigurationUpdateConfig(body)); effort != "" {
			return effort
		}
	}
	if effort := reasoningEffortFromSuffix(ParseSuffix(model)); effort != "" {
		return effort
	}

	config := extractThinkingConfigForUsage(body, provider)
	if !hasThinkingConfig(config) {
		switch provider {
		case "openai-response":
			config = extractCodexUsageConfig(body)
		case "openai":
			config = extractCodexUsageConfig(body)
		}
	}
	return reasoningEffortFromConfig(config)
}

// ExtractTranslatedReasoningEffort returns the final provider payload's thinking
// setting as a canonical reasoning_effort label for usage logging.
func ExtractTranslatedReasoningEffort(body []byte, provider string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	config := extractThinkingConfigForUsage(body, provider)
	if !hasThinkingConfig(config) {
		switch provider {
		case "openai", "openai-response":
			config = extractCodexUsageConfig(body)
			if !hasThinkingConfig(config) {
				config = extractOpenAIConfig(body)
			}
		}
	}
	return reasoningEffortFromConfig(config)
}

func extractThinkingConfigForUsage(body []byte, provider string) ThinkingConfig {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "codex", "xai", "openai-response":
		return extractCodexUsageConfig(body)
	case "claude":
		if ojson.GetBytesNoCopy(body, "thinking.type").String() == "between_tools" {
			return ThinkingConfig{Mode: ModeNone, Budget: 0}
		}
		config := extractThinkingConfig(body, provider)
		if config.Mode == ModeNone && hasThinkingConfig(config) {
			return config
		}
		// The last effort directive, not the cached top-level placeholder, is the turn's effort.
		if directive := extractClaudeEffortDirectiveConfig(body); hasThinkingConfig(directive) {
			return directive
		}
		return config
	default:
		return extractThinkingConfig(body, provider)
	}
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

func extractClaudeConfig(body []byte) ThinkingConfig {
	return extractClaudeConfigFromGetter(func(path string) ojson.Result {
		return ojson.GetBytesNoCopy(body, path)
	})
}

func extractClaudeConfigFromGetter(get func(string) ojson.Result) ThinkingConfig {
	thinkingType := get("thinking.type").String()
	if thinkingType == "disabled" {
		return ThinkingConfig{Mode: ModeNone, Budget: 0}
	}
	if !get("thinking").Exists() || thinkingType == "adaptive" || thinkingType == "auto" {
		if effort := get("output_config.effort"); effort.Exists() && effort.Type == ojson.String {
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

	if budget := get("thinking.budget_tokens"); budget.Exists() {
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

	if thinkingType == "enabled" {
		if effort := get("output_config.effort"); effort.Exists() && effort.Type == ojson.String {
			value := strings.ToLower(strings.TrimSpace(effort.String()))
			if value != "" {
				switch value {
				case "none":
					return ThinkingConfig{Mode: ModeNone, Budget: 0}
				case "auto":
					return ThinkingConfig{Mode: ModeAuto, Budget: -1}
				default:
					return ThinkingConfig{Mode: ModeLevel, Level: ThinkingLevel(value)}
				}
			}
		}
		return ThinkingConfig{Mode: ModeAuto, Budget: -1}
	}

	return ThinkingConfig{}
}

func extractGeminiConfig(body []byte, provider string) ThinkingConfig {
	return extractGeminiConfigFromGetter(func(path string) ojson.Result {
		return ojson.GetBytesNoCopy(body, path)
	}, provider)
}

func extractGeminiConfigFromGetter(get func(string) ojson.Result, provider string) ThinkingConfig {
	prefix := "generationConfig.thinkingConfig"
	if provider == "antigravity" {
		prefix = "request.generationConfig.thinkingConfig"
	}

	level := get(prefix + ".thinkingLevel")
	if !level.Exists() {
		level = get(prefix + ".thinking_level")
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

	budget := get(prefix + ".thinkingBudget")
	if !budget.Exists() {
		budget = get(prefix + ".thinking_budget")
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

func extractInteractionsConfig(body []byte) ThinkingConfig {
	return extractInteractionsConfigFromGetter(func(path string) ojson.Result {
		return ojson.GetBytesNoCopy(body, path)
	})
}

func extractInteractionsConfigFromGetter(get func(string) ojson.Result) ThinkingConfig {
	for _, path := range []string{
		"generation_config.thinking_level",
		"generation_config.thinkingLevel",
		"generation_config.thinking_config.thinking_level",
		"generation_config.thinking_config.thinkingLevel",
		"generation_config.thinkingConfig.thinking_level",
		"generation_config.thinkingConfig.thinkingLevel",
	} {
		level := get(path)
		if !level.Exists() {
			continue
		}
		value := strings.ToLower(strings.TrimSpace(level.String()))
		switch value {
		case "none":
			return ThinkingConfig{Mode: ModeNone, Budget: 0}
		case "auto":
			return ThinkingConfig{Mode: ModeAuto, Budget: -1}
		default:
			return ThinkingConfig{Mode: ModeLevel, Level: ThinkingLevel(value)}
		}
	}

	for _, path := range []string{
		"generation_config.thinking_budget",
		"generation_config.thinkingBudget",
		"generation_config.thinking_config.thinking_budget",
		"generation_config.thinking_config.thinkingBudget",
		"generation_config.thinkingConfig.thinking_budget",
		"generation_config.thinkingConfig.thinkingBudget",
	} {
		budget := get(path)
		if !budget.Exists() {
			continue
		}
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

func extractOpenAIConfig(body []byte) ThinkingConfig {
	return extractOpenAIConfigFromGetter(func(path string) ojson.Result {
		return ojson.GetBytesNoCopy(body, path)
	})
}

func extractOpenAIConfigFromGetter(get func(string) ojson.Result) ThinkingConfig {
	if effort := get("reasoning_effort"); effort.Exists() {
		value := effort.String()
		if value == "none" {
			return ThinkingConfig{Mode: ModeNone, Budget: 0}
		}
		return ThinkingConfig{Mode: ModeLevel, Level: ThinkingLevel(value)}
	}
	return ThinkingConfig{}
}

func extractKimiConfig(body []byte) ThinkingConfig {
	return extractKimiConfigFromGetter(func(path string) ojson.Result {
		return ojson.GetBytesNoCopy(body, path)
	})
}

func extractKimiConfigFromGetter(get func(string) ojson.Result) ThinkingConfig {
	thinkingType := get("thinking.type")
	if thinkingType.Exists() {
		switch strings.ToLower(strings.TrimSpace(thinkingType.String())) {
		case "disabled":
			return ThinkingConfig{Mode: ModeNone, Budget: 0}
		case "enabled":
			if !get("thinking.effort").Exists() {
				return ThinkingConfig{}
			}
		}
	}

	if effort := get("thinking.effort"); effort.Exists() {
		value := strings.ToLower(strings.TrimSpace(effort.String()))
		switch value {
		case "":
			return ThinkingConfig{}
		case "none":
			return ThinkingConfig{Mode: ModeNone, Budget: 0}
		case "auto":
			return ThinkingConfig{Mode: ModeAuto, Budget: -1}
		default:
			return ThinkingConfig{Mode: ModeLevel, Level: ThinkingLevel(value)}
		}
	}

	if thinkingType.Exists() {
		return ThinkingConfig{}
	}

	return extractOpenAIConfigFromGetter(get)
}

func extractCodexConfig(body []byte) ThinkingConfig {
	return extractCodexConfigFromGetter(func(path string) ojson.Result {
		return ojson.GetBytesNoCopy(body, path)
	})
}

func extractCodexConfigFromGetter(get func(string) ojson.Result) ThinkingConfig {
	if effort := get("reasoning.effort"); effort.Exists() {
		value := effort.String()
		if value == "none" {
			return ThinkingConfig{Mode: ModeNone, Budget: 0}
		}
		return ThinkingConfig{Mode: ModeLevel, Level: ThinkingLevel(value)}
	}
	return ThinkingConfig{}
}

func extractCodexUsageConfig(body []byte) ThinkingConfig {
	if len(body) == 0 || !ojson.ValidBytes(body) {
		return ThinkingConfig{}
	}
	if config := extractConfigurationUpdateConfig(body); hasThinkingConfig(config) {
		return config
	}
	return extractCodexConfig(body)
}

func extractCodexUsageConfigDoc(doc *ojson.Doc, docValid bool) ThinkingConfig {
	if doc == nil || !docValid {
		return ThinkingConfig{}
	}
	if config := extractConfigurationUpdateConfigDoc(doc, docValid); hasThinkingConfig(config) {
		return config
	}
	return extractCodexConfigFromGetter(doc.Get)
}
