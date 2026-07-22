package codexintegration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

const defaultCatalogTemplate = "gpt-5.5"

// ModelProvidersFunc returns the registered providers for a model ID.
type ModelProvidersFunc func(string) []string

// Catalog is a complete Codex model catalog plus deterministic diagnostic revisions.
// Marshal intentionally writes only the client-compatible models payload; revisions
// are persisted in the integration journal rather than sent to Codex.
type Catalog struct {
	Models          []map[string]any
	Revision        string
	SourceRevision  uint64
	MappingRevision string
}

type catalogPayload struct {
	Models []map[string]any `json:"models"`
}

// Marshal returns deterministic, client-compatible catalog JSON.
func (c Catalog) Marshal() ([]byte, error) {
	data, err := json.MarshalIndent(catalogPayload{Models: c.Models}, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal Codex catalog: %w", err)
	}
	return append(data, '\n'), nil
}

// CompileCatalog builds a complete official catalog with stable third-party overlays.
func CompileCatalog(models []map[string]any, providersForModel ModelProvidersFunc, integration config.CodexIntegrationConfig) (Catalog, error) {
	rawTemplates, sourceRevision := registry.GetCodexClientModelsSnapshot()
	templates, defaultTemplate, err := parseCatalogTemplates(rawTemplates)
	if err != nil {
		return Catalog{}, err
	}

	available := make(map[string]map[string]any, len(models))
	for _, model := range models {
		id := catalogString(model, "id")
		if id == "" {
			continue
		}
		available[id] = cloneCatalogMap(model)
	}

	official := make([]map[string]any, 0, len(models))
	availableIDs := make([]string, 0, len(available))
	for id := range available {
		availableIDs = append(availableIDs, id)
	}
	sort.Strings(availableIDs)
	for _, id := range availableIDs {
		providers := normalizedProviders(providersForModel, id)
		if !containsProvider(providers, "codex") {
			continue
		}
		entry := compileOfficialCatalogEntry(id, available[id], templates, defaultTemplate)
		if !onlyProvider(providers, "codex") {
			entry["supports_search_tool"] = false
			delete(entry, "web_search_tool_type")
		}
		official = append(official, entry)
	}

	if !integration.Enabled {
		ordered := orderCatalogEntries(official, nil)
		return finalizeCatalog(ordered, sourceRevision, integration)
	}

	if !catalogContainsSlug(official, "gpt-5.6-sol") {
		return Catalog{}, fmt.Errorf("official Codex model %q is unavailable", "gpt-5.6-sol")
	}

	overlays := make([]map[string]any, 0, len(integration.Models))
	for _, mapping := range integration.Models {
		if !mapping.Visible {
			continue
		}
		model, ok := available[mapping.UpstreamModel]
		if !ok || !containsProvider(normalizedProviders(providersForModel, mapping.UpstreamModel), mapping.Provider) {
			return Catalog{}, fmt.Errorf("Codex Integration model %q target %s/%s is unavailable", mapping.Slug, mapping.Provider, mapping.UpstreamModel)
		}
		overlays = append(overlays, compileOverlayCatalogEntry(mapping, model, defaultTemplate))
	}

	featured := []string{"gpt-5.6-sol"}
	featuredModels := make([]config.CodexIntegrationModel, 0, len(integration.Models))
	for _, mapping := range integration.Models {
		if mapping.Visible && mapping.Featured {
			featuredModels = append(featuredModels, mapping)
		}
	}
	sort.SliceStable(featuredModels, func(i, j int) bool {
		if featuredModels[i].Priority == featuredModels[j].Priority {
			return featuredModels[i].Slug < featuredModels[j].Slug
		}
		return featuredModels[i].Priority < featuredModels[j].Priority
	})
	for _, mapping := range featuredModels {
		featured = append(featured, mapping.Slug)
	}
	if len(featured) != 5 {
		return Catalog{}, fmt.Errorf("Codex Integration featured surface has %d models, want 5", len(featured))
	}

	entries := append(official, overlays...)
	ordered := orderCatalogEntries(entries, featured)
	catalog, err := finalizeCatalog(ordered, sourceRevision, integration)
	if err != nil {
		return Catalog{}, err
	}
	if err = ValidateCatalog(catalog, integration); err != nil {
		return Catalog{}, err
	}
	return catalog, nil
}

func parseCatalogTemplates(raw []byte) (map[string]map[string]any, map[string]any, error) {
	var payload catalogPayload
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, nil, fmt.Errorf("decode Codex catalog templates: %w", err)
	}
	templates := make(map[string]map[string]any, len(payload.Models))
	for _, model := range payload.Models {
		slug := catalogString(model, "slug")
		if slug != "" {
			templates[slug] = cloneCatalogMap(model)
		}
	}
	defaultTemplate := templates[defaultCatalogTemplate]
	if defaultTemplate == nil {
		return nil, nil, fmt.Errorf("Codex catalog templates missing %q", defaultCatalogTemplate)
	}
	return templates, cloneCatalogMap(defaultTemplate), nil
}

func compileOfficialCatalogEntry(id string, model map[string]any, templates map[string]map[string]any, defaultTemplate map[string]any) map[string]any {
	entry := cloneCatalogMap(templates[id])
	if entry == nil {
		entry = cloneCatalogMap(defaultTemplate)
		entry["slug"] = id
		entry["display_name"] = id
		entry["description"] = id
		entry["prefer_websockets"] = false
		entry["service_tiers"] = []any{}
		delete(entry, "apply_patch_tool_type")
		delete(entry, "upgrade")
		delete(entry, "availability_nux")
	}
	if displayName := catalogString(model, "display_name"); displayName != "" {
		entry["display_name"] = displayName
	}
	applyCatalogModelMetadata(entry, id, "codex", model)
	applyCatalogVisibility(entry, id)
	if catalogString(entry, "visibility") == "list" {
		entry["multi_agent_version"] = config.DefaultCodexMultiAgentMode
	}
	return entry
}

func compileOverlayCatalogEntry(mapping config.CodexIntegrationModel, model map[string]any, defaultTemplate map[string]any) map[string]any {
	entry := cloneCatalogMap(defaultTemplate)
	entry["slug"] = mapping.Slug
	entry["display_name"] = mapping.DisplayName
	if catalogString(entry, "display_name") == "" {
		entry["display_name"] = mapping.Slug
	}
	entry["description"] = fmt.Sprintf("%s via %s", entry["display_name"], mapping.Provider)
	entry["visibility"] = "list"
	entry["priority"] = mapping.Priority
	entry["prefer_websockets"] = false
	entry["multi_agent_version"] = config.DefaultCodexMultiAgentMode
	entry["supports_parallel_tool_calls"] = mapping.SupportsParallelTools
	entry["supports_search_tool"] = mapping.SupportsWebSearch
	entry["service_tiers"] = []any{}
	delete(entry, "additional_speed_tiers")
	delete(entry, "apply_patch_tool_type")
	delete(entry, "availability_nux")
	delete(entry, "default_service_tier")
	delete(entry, "upgrade")
	if !mapping.SupportsWebSearch {
		delete(entry, "web_search_tool_type")
	}
	applyCatalogInputModalities(entry, mapping.InputModalities)
	applyCatalogModelMetadata(entry, mapping.UpstreamModel, mapping.Provider, model)
	return entry
}

func applyCatalogModelMetadata(entry map[string]any, modelID, provider string, model map[string]any) {
	contextWindow := catalogInt(model, "context_length")
	if info := registry.LookupModelInfo(modelID, provider); info != nil {
		if contextWindow <= 0 {
			contextWindow = info.ContextLength
		}
		if info.Thinking != nil {
			applyCatalogThinking(entry, info.Thinking.Levels)
		}
	}
	if contextWindow > 0 {
		entry["context_window"] = contextWindow
		entry["max_context_window"] = contextWindow
	}
}

func applyCatalogInputModalities(entry map[string]any, modalities []string) {
	values := make([]any, 0, 2)
	seen := make(map[string]struct{}, 2)
	hasImage := false
	for _, raw := range modalities {
		value := strings.ToLower(strings.TrimSpace(raw))
		if value != "text" && value != "image" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		values = append(values, value)
		hasImage = hasImage || value == "image"
	}
	if len(values) > 0 {
		entry["input_modalities"] = values
	}
	if hasImage {
		entry["supports_image_detail_original"] = true
	} else {
		delete(entry, "supports_image_detail_original")
	}
}

func applyCatalogThinking(entry map[string]any, rawLevels []string) {
	levels := make([]any, 0, len(rawLevels))
	seen := make(map[string]struct{}, len(rawLevels))
	for _, raw := range rawLevels {
		level := strings.ToLower(strings.TrimSpace(raw))
		switch level {
		case "none", "low", "medium", "high", "xhigh", "max", "ultra":
		default:
			continue
		}
		if _, ok := seen[level]; ok {
			continue
		}
		seen[level] = struct{}{}
		levels = append(levels, map[string]any{"effort": level, "description": catalogReasoningDescription(level)})
	}
	if len(levels) == 0 {
		return
	}
	defaultLevel := catalogString(levels[0].(map[string]any), "effort")
	if _, ok := seen["medium"]; ok {
		defaultLevel = "medium"
	} else if defaultLevel == "none" && len(levels) > 1 {
		defaultLevel = catalogString(levels[1].(map[string]any), "effort")
	}
	entry["supported_reasoning_levels"] = levels
	entry["default_reasoning_level"] = defaultLevel
}

func catalogReasoningDescription(level string) string {
	switch level {
	case "none":
		return "No reasoning"
	case "low":
		return "Fast responses with lighter reasoning"
	case "medium":
		return "Balances speed and reasoning depth for everyday tasks"
	case "high":
		return "Greater reasoning depth for complex problems"
	case "xhigh":
		return "Extra high reasoning depth for complex problems"
	case "max", "ultra":
		return "Maximum available reasoning depth for complex problems"
	default:
		return level
	}
}

func orderCatalogEntries(entries []map[string]any, featured []string) []map[string]any {
	bySlug := make(map[string]map[string]any, len(entries))
	for _, entry := range entries {
		slug := catalogString(entry, "slug")
		if slug != "" {
			bySlug[slug] = entry
		}
	}
	ordered := make([]map[string]any, 0, len(bySlug))
	for i, slug := range featured {
		if entry := bySlug[slug]; entry != nil {
			entry["priority"] = i + 1
			ordered = append(ordered, entry)
			delete(bySlug, slug)
		}
	}
	rest := make([]map[string]any, 0, len(bySlug))
	for _, entry := range bySlug {
		rest = append(rest, entry)
	}
	sort.SliceStable(rest, func(i, j int) bool {
		leftPriority, rightPriority := catalogInt(rest[i], "priority"), catalogInt(rest[j], "priority")
		if leftPriority == rightPriority {
			return catalogString(rest[i], "slug") < catalogString(rest[j], "slug")
		}
		return leftPriority < rightPriority
	})
	for i, entry := range rest {
		entry["priority"] = 100 + i
		ordered = append(ordered, entry)
	}
	return ordered
}

func finalizeCatalog(models []map[string]any, sourceRevision uint64, integration config.CodexIntegrationConfig) (Catalog, error) {
	mappingJSON, err := json.Marshal(integration.Models)
	if err != nil {
		return Catalog{}, fmt.Errorf("marshal Codex model mappings: %w", err)
	}
	mappingHash := sha256.Sum256(mappingJSON)
	modelsJSON, err := json.Marshal(models)
	if err != nil {
		return Catalog{}, fmt.Errorf("marshal compiled Codex models: %w", err)
	}
	revisionInput := append([]byte(fmt.Sprintf("%d:", sourceRevision)), mappingHash[:]...)
	revisionInput = append(revisionInput, modelsJSON...)
	revisionHash := sha256.Sum256(revisionInput)
	return Catalog{
		Models:          models,
		Revision:        hex.EncodeToString(revisionHash[:]),
		SourceRevision:  sourceRevision,
		MappingRevision: hex.EncodeToString(mappingHash[:]),
	}, nil
}

func normalizedProviders(providersForModel ModelProvidersFunc, model string) []string {
	if providersForModel == nil {
		return nil
	}
	raw := providersForModel(model)
	providers := make([]string, 0, len(raw))
	for _, provider := range raw {
		provider = strings.ToLower(strings.TrimSpace(provider))
		if provider != "" {
			providers = append(providers, provider)
		}
	}
	sort.Strings(providers)
	return providers
}

func containsProvider(providers []string, target string) bool {
	for _, provider := range providers {
		if provider == target {
			return true
		}
	}
	return false
}

func onlyProvider(providers []string, target string) bool {
	return len(providers) > 0 && containsProvider(providers, target) && len(providers) == 1
}

func catalogContainsSlug(models []map[string]any, slug string) bool {
	for _, model := range models {
		if catalogString(model, "slug") == slug {
			return true
		}
	}
	return false
}

func applyCatalogVisibility(entry map[string]any, id string) {
	switch strings.TrimSpace(id) {
	case "grok-imagine-image-quality", "gpt-image-1.5", "gpt-image-2", "grok-imagine-image", "grok-imagine-video", "grok-imagine-video-1.5-preview":
		entry["visibility"] = "hide"
	}
}

func catalogString(model map[string]any, key string) string {
	value, _ := model[key].(string)
	return strings.TrimSpace(value)
}

func catalogInt(model map[string]any, key string) int {
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

func cloneCatalogMap(model map[string]any) map[string]any {
	if model == nil {
		return nil
	}
	clone := make(map[string]any, len(model))
	for key, value := range model {
		clone[key] = cloneCatalogValue(value)
	}
	return clone
}

func cloneCatalogValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneCatalogMap(typed)
	case []any:
		clone := make([]any, len(typed))
		for i, entry := range typed {
			clone[i] = cloneCatalogValue(entry)
		}
		return clone
	case []string:
		return append([]string(nil), typed...)
	default:
		return value
	}
}
