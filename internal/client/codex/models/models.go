// Package models builds model catalogs for official Codex clients.
package models

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

type codexClientModelsPayload struct {
	Models []map[string]any `json:"models"`
}

// ProvidersForModelFunc returns the providers registered for a model.
type ProvidersForModelFunc func(string) []string

var (
	codexClientModelTemplatesMu       sync.Mutex
	codexClientModelTemplatesLoaded   bool
	codexClientModelTemplatesRevision uint64
	codexClientModelTemplates         map[string]map[string]any
	codexClientDefaultTemplate        map[string]any
	codexClientModelTemplatesErr      error
)

var codexClientAllowedReasoningLevels = map[string]struct{}{
	"none":    {},
	"minimal": {},
	"low":     {},
	"medium":  {},
	"high":    {},
	"xhigh":   {},
	"max":     {},
	"ultra":   {},
}

var codexClientLegacyAllowedReasoningLevels = map[string]struct{}{
	"none":    {},
	"minimal": {},
	"low":     {},
	"medium":  {},
	"high":    {},
	"xhigh":   {},
}

// BuildResponse builds a Codex client model response from available models.
func BuildResponse(availableModels []map[string]any, providersForModel ProvidersForModelFunc, optimizeMultiAgentV2 bool) map[string]any {
	return BuildResponseForClient(availableModels, providersForModel, optimizeMultiAgentV2, "")
}

// BuildResponseForClient builds a Codex client model response from available models
// tailored for a specific client version.
func BuildResponseForClient(availableModels []map[string]any, providersForModel ProvidersForModelFunc, optimizeMultiAgentV2 bool, clientVersion string) map[string]any {
	return map[string]any{
		"models": buildCodexClientModels(availableModels, providersForModel, optimizeMultiAgentV2, clientVersion),
	}
}

func buildCodexClientModels(models []map[string]any, providersForModel ProvidersForModelFunc, optimizeMultiAgentV2 bool, clientVersion string) []map[string]any {
	templates, defaultTemplate, err := loadCodexClientModelTemplates()
	if err != nil || defaultTemplate == nil {
		return nil
	}

	result := make([]map[string]any, 0, len(models))
	for _, model := range models {
		id := strings.TrimSpace(stringModelValue(model, "id"))
		if id == "" {
			continue
		}

		if template, ok := templates[id]; ok {
			entry := cloneCodexClientModelMap(template)
			applyCodexClientDisplayName(entry, model)
			applyCodexClientMaxContextLengthOverride(entry, model)
			applyCodexClientMaxTokens(entry, model)
			applyCodexClientSearchToolSupport(entry, id, true, providersForModel)
			sanitizeCodexClientReasoningMetadata(entry, clientVersion)
			applyCodexClientVisibilityOverride(entry, id)
			if optimizeMultiAgentV2 {
				entry["multi_agent_version"] = "v2"
			}
			result = append(result, entry)
			continue
		}

		entry := cloneCodexClientModelMap(defaultTemplate)
		applyCodexClientModelMetadata(entry, id, model, optimizeMultiAgentV2, clientVersion)
		applyCodexClientMaxTokens(entry, model)
		applyCodexClientSearchToolSupport(entry, id, false, providersForModel)
		sanitizeCodexClientReasoningMetadata(entry, clientVersion)
		applyCodexClientVisibilityOverride(entry, id)
		result = append(result, entry)
	}

	applyCodexClientNonTemplatePriorities(result, templates)

	sort.SliceStable(result, func(i, j int) bool {
		return codexClientModelPriority(result[i]) < codexClientModelPriority(result[j])
	})

	return result
}

func maxCodexClientTemplatePriority(templates map[string]map[string]any) int {
	maxPriority := 0
	for _, template := range templates {
		priority := codexClientModelPriority(template)
		if priority > maxPriority {
			maxPriority = priority
		}
	}
	return maxPriority
}

func applyCodexClientNonTemplatePriorities(result []map[string]any, templates map[string]map[string]any) {
	if len(result) == 0 {
		return
	}

	basePriority := maxCodexClientTemplatePriority(templates)
	type nonTemplateEntry struct {
		index       int
		displayName string
		slug        string
	}

	pending := make([]nonTemplateEntry, 0)
	for index, entry := range result {
		slug := stringModelValue(entry, "slug")
		if _, ok := templates[slug]; ok {
			continue
		}
		displayName := stringModelValue(entry, "display_name")
		if displayName == "" {
			displayName = slug
		}
		pending = append(pending, nonTemplateEntry{
			index:       index,
			displayName: displayName,
			slug:        slug,
		})
	}

	sort.SliceStable(pending, func(i, j int) bool {
		left := strings.ToLower(pending[i].displayName)
		right := strings.ToLower(pending[j].displayName)
		if left == right {
			return pending[i].slug < pending[j].slug
		}
		return left < right
	})

	for rank, entry := range pending {
		result[entry.index]["priority"] = basePriority + 100*(rank+1)
	}
}

func loadCodexClientModelTemplates() (map[string]map[string]any, map[string]any, error) {
	raw, revision := registry.GetCodexClientModelsSnapshot()
	return loadCodexClientModelTemplatesSnapshot(raw, revision)
}

func loadCodexClientModelTemplatesSnapshot(raw []byte, revision uint64) (map[string]map[string]any, map[string]any, error) {
	codexClientModelTemplatesMu.Lock()
	defer codexClientModelTemplatesMu.Unlock()
	if codexClientModelTemplatesLoaded && codexClientModelTemplatesRevision == revision {
		return codexClientModelTemplates, codexClientDefaultTemplate, codexClientModelTemplatesErr
	}

	var payload codexClientModelsPayload
	err := json.Unmarshal(raw, &payload)
	var templates map[string]map[string]any
	var defaultTemplate map[string]any
	if err == nil {
		templates = make(map[string]map[string]any, len(payload.Models))
		for _, model := range payload.Models {
			slug := strings.TrimSpace(stringModelValue(model, "slug"))
			if slug == "" {
				continue
			}
			templates[slug] = cloneCodexClientModelMap(model)
			if slug == "gpt-5.5" {
				defaultTemplate = cloneCodexClientModelMap(model)
			}
		}
	}

	codexClientModelTemplatesLoaded = true
	codexClientModelTemplatesRevision = revision
	codexClientModelTemplates = templates
	codexClientDefaultTemplate = defaultTemplate
	codexClientModelTemplatesErr = err
	return codexClientModelTemplates, codexClientDefaultTemplate, codexClientModelTemplatesErr
}

func applyCodexClientDisplayName(entry map[string]any, model map[string]any) {
	if displayName := stringModelValue(model, "display_name"); displayName != "" {
		entry["display_name"] = displayName
	}
}

func applyCodexClientMaxContextLengthOverride(entry map[string]any, model map[string]any) {
	if maxContextLength := intModelValue(model, "max_context_length"); maxContextLength > 0 {
		entry["context_window"] = maxContextLength
		entry["max_context_window"] = maxContextLength
	}
}

func applyCodexClientMaxTokens(entry map[string]any, model map[string]any) {
	if maxCompletionTokens := intModelValue(model, "max_completion_tokens"); maxCompletionTokens > 0 {
		entry["max_tokens"] = maxCompletionTokens
	}
}

func applyCodexClientSearchToolSupport(entry map[string]any, id string, templateModel bool, providersForModel ProvidersForModelFunc) {
	supportsSearch, _ := entry["supports_search_tool"].(bool)
	if !supportsSearch {
		return
	}

	if !templateModel {
		entry["supports_search_tool"] = false
		return
	}

	if providersForModel == nil {
		return
	}

	providers := providersForModel(id)
	if len(providers) == 0 {
		entry["supports_search_tool"] = false
		return
	}
	for _, provider := range providers {
		if !strings.EqualFold(strings.TrimSpace(provider), "codex") {
			entry["supports_search_tool"] = false
			return
		}
	}
}

func applyCodexClientModelMetadata(entry map[string]any, id string, model map[string]any, optimizeMultiAgentV2 bool, clientVersion string) {
	info := registry.LookupModelInfo(id)

	displayName := stringModelValue(model, "display_name")
	description := stringModelValue(model, "description")
	contextWindow := intModelValue(model, "context_length")
	thinkingSupport := codexClientThinkingSupport(model)

	if info != nil {
		if info.DisplayName != "" {
			displayName = info.DisplayName
		}
		if info.Description != "" {
			description = info.Description
		}
		if contextWindow <= 0 && info.ContextLength > 0 {
			contextWindow = info.ContextLength
		}
		if info.Type == registry.OpenAIImageModelType {
			entry["visibility"] = "hide"
			delete(entry, "input_modalities")
			delete(entry, "supports_image_detail_original")
		} else {
			applyCodexClientInputModalitiesMetadata(entry, info.SupportedInputModalities)
		}
		if thinkingSupport == nil {
			thinkingSupport = info.Thinking
		}
	}
	applyCodexClientThinkingMetadata(entry, thinkingSupport, clientVersion)

	if maxContextWindow := intModelValue(model, "max_context_length"); maxContextWindow > 0 {
		contextWindow = maxContextWindow
	}

	if displayName == "" {
		displayName = id
	}
	if description == "" {
		description = id
	}

	entry["slug"] = id
	entry["display_name"] = displayName
	entry["description"] = description
	entry["prefer_websockets"] = false
	if optimizeMultiAgentV2 {
		entry["multi_agent_version"] = "v2"
	}
	entry["service_tiers"] = []any{}
	delete(entry, "apply_patch_tool_type")
	delete(entry, "upgrade")
	delete(entry, "availability_nux")

	if contextWindow > 0 {
		entry["context_window"] = contextWindow
		entry["max_context_window"] = contextWindow
	}

	if baseInstructions := stringModelValue(model, "base_instructions"); baseInstructions != "" {
		entry["base_instructions"] = baseInstructions
	}
	if plans, ok := model["available_in_plans"]; ok {
		entry["available_in_plans"] = cloneCodexClientModelValue(plans)
	}
}

func codexClientThinkingSupport(model map[string]any) *registry.ThinkingSupport {
	raw, ok := model["thinking"]
	if !ok || raw == nil {
		return nil
	}
	switch thinking := raw.(type) {
	case *registry.ThinkingSupport:
		return thinking
	case registry.ThinkingSupport:
		return &thinking
	}
	data, errMarshal := json.Marshal(raw)
	if errMarshal != nil {
		return nil
	}
	var thinking registry.ThinkingSupport
	if errUnmarshal := json.Unmarshal(data, &thinking); errUnmarshal != nil {
		return nil
	}
	return &thinking
}

func applyCodexClientVisibilityOverride(entry map[string]any, id string) {
	switch strings.TrimSpace(id) {
	case "grok-imagine-image-quality", "gpt-image-1.5", "gpt-image-2", "grok-imagine-image", "grok-imagine-image-2.0", "grok-imagine-video", "grok-imagine-video-1.5", "grok-imagine-video-1.5-preview":
		entry["visibility"] = "hide"
	}
}

func applyCodexClientInputModalitiesMetadata(entry map[string]any, modalities []string) {
	if len(modalities) == 0 {
		return
	}
	// Codex client only accepts text/image input modalities.
	codexModalities := make([]any, 0, 2)
	seen := make(map[string]struct{}, 2)
	supportsImage := false
	for _, raw := range modalities {
		switch modality := strings.ToLower(strings.TrimSpace(raw)); modality {
		case "text", "image":
			if _, ok := seen[modality]; ok {
				continue
			}
			seen[modality] = struct{}{}
			codexModalities = append(codexModalities, modality)
			if modality == "image" {
				supportsImage = true
			}
		}
	}
	if len(codexModalities) == 0 {
		return
	}
	entry["input_modalities"] = codexModalities
	if supportsImage {
		entry["supports_image_detail_original"] = true
	} else {
		delete(entry, "supports_image_detail_original")
	}
}

func applyCodexClientThinkingMetadata(entry map[string]any, thinking *registry.ThinkingSupport, clientVersion string) {
	if thinking == nil {
		return
	}

	levels := make([]any, 0, len(thinking.Levels))
	defaultLevel := ""
	firstLevel := ""
	for _, rawLevel := range thinking.Levels {
		level := normalizeCodexClientReasoningLevel(rawLevel, clientVersion)
		if level == "" {
			continue
		}
		if firstLevel == "" {
			firstLevel = level
		}
		if (defaultLevel == "" && level != "none") || level == "medium" {
			defaultLevel = level
		}
		levels = append(levels, map[string]any{
			"effort":      level,
			"description": codexClientReasoningDescription(level),
		})
	}
	if len(levels) == 0 {
		entry["supported_reasoning_levels"] = levels
		delete(entry, "default_reasoning_level")
		return
	}
	if defaultLevel == "" {
		defaultLevel = firstLevel
	}

	entry["supported_reasoning_levels"] = levels
	entry["default_reasoning_level"] = defaultLevel
}

func sanitizeCodexClientReasoningMetadata(entry map[string]any, clientVersion string) {
	rawLevels, ok := entry["supported_reasoning_levels"].([]any)
	if !ok {
		return
	}

	levels := make([]any, 0, len(rawLevels))
	allowedDefaults := make(map[string]struct{}, len(rawLevels))
	for _, rawLevelEntry := range rawLevels {
		levelEntry, ok := rawLevelEntry.(map[string]any)
		if !ok {
			continue
		}
		level := normalizeCodexClientReasoningLevel(stringModelValue(levelEntry, "effort"), clientVersion)
		if level == "" {
			continue
		}
		clonedEntry := cloneCodexClientModelMap(levelEntry)
		clonedEntry["effort"] = level
		levels = append(levels, clonedEntry)
		allowedDefaults[level] = struct{}{}
	}

	if len(levels) == 0 {
		entry["supported_reasoning_levels"] = levels
		delete(entry, "default_reasoning_level")
		return
	}

	defaultLevel := normalizeCodexClientReasoningLevel(stringModelValue(entry, "default_reasoning_level"), clientVersion)
	if _, ok := allowedDefaults[defaultLevel]; !ok {
		defaultLevel = stringModelValue(levels[0].(map[string]any), "effort")
	}

	entry["supported_reasoning_levels"] = levels
	entry["default_reasoning_level"] = defaultLevel
}

func normalizeCodexClientReasoningLevel(rawLevel string, clientVersion string) string {
	level := strings.ToLower(strings.TrimSpace(rawLevel))
	if !supportsExtendedReasoningLevels(clientVersion) {
		if _, ok := codexClientLegacyAllowedReasoningLevels[level]; !ok {
			return ""
		}
		return level
	}
	if _, ok := codexClientAllowedReasoningLevels[level]; !ok {
		return ""
	}
	return level
}

// supportsExtendedReasoningLevels reports whether the given clientVersion supports
// extended reasoning effort levels ("max" and "ultra"), which require Codex CLI >= 0.144.0.
// If the version is empty or unparseable, it returns true to preserve modern features.
func supportsExtendedReasoningLevels(clientVersion string) bool {
	clientVersion = strings.TrimSpace(clientVersion)
	if clientVersion == "" {
		return true
	}
	cmp, ok := compareDottedVersions(clientVersion, "0.144.0")
	if !ok {
		return true
	}
	return cmp >= 0
}

func parseDottedVersion(version string) []int64 {
	version = strings.TrimSpace(version)
	if strings.HasPrefix(version, "v") || strings.HasPrefix(version, "V") {
		version = version[1:]
	}
	if idx := strings.IndexAny(version, "-+"); idx != -1 {
		version = version[:idx]
	}
	parts := strings.Split(version, ".")
	nums := make([]int64, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		num, errParse := strconv.ParseInt(part, 10, 64)
		if errParse != nil || num < 0 {
			return nil
		}
		nums = append(nums, num)
	}
	return nums
}

func compareDottedVersions(a, b string) (int, bool) {
	numsA := parseDottedVersion(a)
	numsB := parseDottedVersion(b)
	if len(numsA) == 0 || len(numsB) == 0 {
		return 0, false
	}
	maxLen := len(numsA)
	if len(numsB) > maxLen {
		maxLen = len(numsB)
	}
	for i := 0; i < maxLen; i++ {
		var valA, valB int64
		if i < len(numsA) {
			valA = numsA[i]
		}
		if i < len(numsB) {
			valB = numsB[i]
		}
		if valA < valB {
			return -1, true
		}
		if valA > valB {
			return 1, true
		}
	}
	return 0, true
}

func codexClientReasoningDescription(level string) string {
	switch level {
	case "none":
		return "No reasoning"
	case "minimal":
		return "Fastest responses with minimal reasoning"
	case "low":
		return "Fast responses with lighter reasoning"
	case "medium":
		return "Balances speed and reasoning depth for everyday tasks"
	case "high":
		return "Greater reasoning depth for complex problems"
	case "xhigh":
		return "Extra high reasoning depth for complex problems"
	case "max":
		return "Maximum available reasoning depth for complex problems"
	default:
		return level
	}
}

func codexClientModelPriority(model map[string]any) int {
	if priority, ok := model["priority"].(int); ok {
		return priority
	}
	if priority, ok := model["priority"].(float64); ok {
		return int(priority)
	}
	return 100
}

func stringModelValue(model map[string]any, key string) string {
	if model == nil {
		return ""
	}
	value, ok := model[key]
	if !ok {
		return ""
	}
	if s, ok := value.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func intModelValue(model map[string]any, key string) int {
	if model == nil {
		return 0
	}
	switch value := model[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return 0
	}
}

func cloneCodexClientModelMap(model map[string]any) map[string]any {
	if model == nil {
		return nil
	}
	cloned := make(map[string]any, len(model))
	for key, value := range model {
		cloned[key] = cloneCodexClientModelValue(value)
	}
	return cloned
}

func cloneCodexClientModelValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneCodexClientModelMap(typed)
	case []any:
		cloned := make([]any, len(typed))
		for i, entry := range typed {
			cloned[i] = cloneCodexClientModelValue(entry)
		}
		return cloned
	case []string:
		return append([]string(nil), typed...)
	default:
		return value
	}
}
