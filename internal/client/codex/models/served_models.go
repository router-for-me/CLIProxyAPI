package models

import (
	"encoding/json"
	"sort"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

// ServedModelSummary describes one model the server hands to Codex clients: the entry
// it serves plus the fields a management UI lists.
type ServedModelSummary struct {
	// Slug is the model id Codex clients request.
	Slug string `json:"slug"`
	// Providers lists the providers that currently supply the model.
	Providers []string `json:"providers,omitempty"`
	// DisplayName is the name the served entry carries.
	DisplayName string `json:"display_name"`
	// Description is the description the served entry carries.
	Description string `json:"description"`
	// ContextWindow is the context window the served entry carries.
	ContextWindow int `json:"context_window"`
	// MaxContextWindow is the maximum context window the served entry carries.
	MaxContextWindow int `json:"max_context_window"`
	// Visibility is the visibility the served entry carries.
	Visibility string `json:"visibility"`
	// DefaultReasoningLevel is the default reasoning level the served entry carries.
	DefaultReasoningLevel string `json:"default_reasoning_level"`
	// Priority is the served position. A model without a catalog entry of its own
	// is ordered after every catalog entry.
	Priority int `json:"priority"`
	// SupportedReasoningLevels lists the reasoning efforts the served entry offers.
	SupportedReasoningLevels []ServedReasoningLevel `json:"supported_reasoning_levels"`
}

// ServedReasoningLevel is one reasoning effort the served entry offers.
type ServedReasoningLevel struct {
	// Effort is the effort name clients request, for example "high".
	Effort string `json:"effort"`
	// Description explains the effort; the served entry may omit it.
	Description string `json:"description,omitempty"`
}

// CodexClientModelSets is what the server hands to Codex clients in the two stages a
// management UI needs: the entries assembled from the catalog templates and the runtime
// model metadata, which are the default configuration, and the entries the local
// override layer produces on top of them.
type CodexClientModelSets struct {
	// Defaults holds one assembled entry per servable model, before overrides.
	Defaults []map[string]any
	// Served holds the entries the override layer produces, in served order.
	Served []map[string]any
	// Summaries describes each served model, in the same order.
	Summaries []ServedModelSummary
	// Origins maps each default slug to where its entry comes from.
	Origins map[string]registry.CodexClientModelsOrigin
	// Issues lists the override entries that could not be applied.
	Issues []registry.CodexClientModelsOverrideIssue
}

// BuildCodexClientModelSets assembles the default entry of every model the server can
// serve, applies the local override layer and describes both. The summaries follow the
// order the models are served in.
func BuildCodexClientModelSets(
	availableModels []map[string]any,
	providersForModel ProvidersForModelFunc,
	optimizeMultiAgentV2 bool,
	clientVersion string,
) CodexClientModelSets {
	// Capability metadata comes from the same registry the served response uses, so the sets
	// described here match what clients receive.
	assembled := assembleCodexClientModelDefaults(
		availableModels,
		providersForModel,
		registry.GetGlobalRegistry().GetResponsesWebSearchCapability,
		optimizeMultiAgentV2,
		clientVersion,
	)
	if len(assembled.bySlug) == 0 {
		return CodexClientModelSets{}
	}

	doc := registry.GetCodexClientModelsOverride()
	servedBySlug, issues := registry.ResolveCodexClientModelOverrides(codexClientModelResolveBase(assembled), doc)

	defaults := make([]map[string]any, 0, len(assembled.order))
	served := make([]map[string]any, 0, len(assembled.order))
	for _, slug := range assembled.order {
		defaults = append(defaults, assembled.bySlug[slug])
		entry, ok := servedBySlug[slug]
		if !ok {
			continue
		}
		served = append(served, entry)
	}
	sortCodexClientModelsByPriority(defaults)
	sortCodexClientModelsByPriority(served)

	sets := CodexClientModelSets{
		Defaults: defaults,
		Served:   served,
		Origins:  codexClientModelOrigins(assembled, doc),
		Issues:   issues,
	}
	sets.Summaries = make([]ServedModelSummary, 0, len(served))
	for _, entry := range served {
		sets.Summaries = append(sets.Summaries, summarizeServedModel(entry, providersForModel))
	}
	return sets
}

// codexClientModelResolveBase is the map inheritance sources resolve against: the
// entries the server assembled plus, for models it does not serve, the catalog template
// they would be assembled from. A source can therefore name an official model this
// machine happens to serve through no provider.
func codexClientModelResolveBase(assembled codexClientModelDefaults) map[string]map[string]any {
	base := make(map[string]map[string]any, len(assembled.bySlug)+len(assembled.templates))
	for slug, entry := range assembled.bySlug {
		base[slug] = entry
	}
	for slug, template := range assembled.templates {
		if _, exists := base[slug]; !exists {
			base[slug] = template
		}
	}
	return base
}

func sortCodexClientModelsByPriority(entries []map[string]any) {
	sort.SliceStable(entries, func(i, j int) bool {
		return codexClientModelPriority(entries[i]) < codexClientModelPriority(entries[j])
	})
}

// summarizeServedModel describes one served entry for a management UI.
func summarizeServedModel(entry map[string]any, providersForModel ProvidersForModelFunc) ServedModelSummary {
	slug := stringModelValue(entry, "slug")
	summary := ServedModelSummary{
		Slug:                  slug,
		DisplayName:           stringModelValue(entry, "display_name"),
		Description:           stringModelValue(entry, "description"),
		ContextWindow:         intModelValue(entry, "context_window"),
		MaxContextWindow:      intModelValue(entry, "max_context_window"),
		Visibility:            stringModelValue(entry, "visibility"),
		DefaultReasoningLevel: stringModelValue(entry, "default_reasoning_level"),
		Priority:              intModelValue(entry, "priority"),
	}
	summary.SupportedReasoningLevels = servedReasoningLevels(entry)
	if providersForModel != nil {
		summary.Providers = providersForModel(slug)
	}
	return summary
}

// codexClientModelOrigins reports where the catalog entry of each slug comes from. A
// model no provider serves has no entry of its own, so an override for it has no effect
// and is reported as such.
func codexClientModelOrigins(assembled codexClientModelDefaults, doc map[string]json.RawMessage) map[string]registry.CodexClientModelsOrigin {
	origins := make(map[string]registry.CodexClientModelsOrigin, len(assembled.bySlug)+len(doc))
	for slug := range assembled.bySlug {
		origins[slug] = registry.CodexClientModelsOriginBase
	}
	for slug := range assembled.bySlug {
		if _, matched := assembled.templates[codexClientMetadataModelID(slug)]; !matched {
			origins[slug] = registry.CodexClientModelsOriginServed
		}
	}
	for slug, patch := range doc {
		if codexClientModelPatchIsNull(patch) {
			origins[slug] = registry.CodexClientModelsOriginRemoved
			continue
		}
		if _, served := assembled.bySlug[slug]; served {
			origins[slug] = registry.CodexClientModelsOriginOverride
			continue
		}
		origins[slug] = registry.CodexClientModelsOriginUnserved
	}
	return origins
}

// codexClientModelPatchIsNull reports whether an override entry keeps its model out of
// the served list.
func codexClientModelPatchIsNull(patch json.RawMessage) bool {
	var decoded any
	if errUnmarshal := json.Unmarshal(patch, &decoded); errUnmarshal != nil {
		return false
	}
	return decoded == nil
}

// servedReasoningLevels reads the reasoning efforts the served entry offers, in the
// order the entry lists them.
func servedReasoningLevels(entry map[string]any) []ServedReasoningLevel {
	rawLevels, ok := entry["supported_reasoning_levels"].([]any)
	if !ok {
		return nil
	}
	levels := make([]ServedReasoningLevel, 0, len(rawLevels))
	for _, rawLevel := range rawLevels {
		levelEntry, okEntry := rawLevel.(map[string]any)
		if !okEntry {
			continue
		}
		effort := stringModelValue(levelEntry, "effort")
		if effort == "" {
			continue
		}
		levels = append(levels, ServedReasoningLevel{
			Effort:      effort,
			Description: stringModelValue(levelEntry, "description"),
		})
	}
	return levels
}
