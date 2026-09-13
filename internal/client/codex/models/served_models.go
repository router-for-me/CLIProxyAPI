package models

import (
	"bytes"
	"encoding/json"
)

// ServedModelSummary describes one model the server hands to Codex clients: the
// catalog entry it is built from plus the fields a management UI lists.
type ServedModelSummary struct {
	// Slug is the model id Codex clients request.
	Slug string `json:"slug"`
	// TemplateSlug is the catalog entry the served entry was built from.
	TemplateSlug string `json:"template_slug"`
	// DefaultTemplate reports that no catalog entry matches the model id, so the
	// default template supplied the entry.
	DefaultTemplate bool `json:"default_template"`
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
	// ServedFields maps every field whose served value differs from the entry the
	// catalog supplies for the model to the value Codex clients receive. The server
	// decides those fields for the client from model metadata, provider capabilities
	// and visibility rules, so the catalog value is not what clients see. A
	// management UI overlays them on the catalog entry to show what clients
	// currently receive.
	ServedFields map[string]any `json:"served_fields,omitempty"`
}

// ServedReasoningLevel is one reasoning effort the served entry offers.
type ServedReasoningLevel struct {
	// Effort is the effort name clients request, for example "high".
	Effort string `json:"effort"`
	// Description explains the effort; the served entry may omit it.
	Description string `json:"description,omitempty"`
}

// SummarizeServedModels reports how each available model is served to Codex
// clients. Summaries follow the same construction as BuildResponseForClient, so a
// model without a matching catalog entry is reported with DefaultTemplate set and
// the default template as its TemplateSlug.
func SummarizeServedModels(availableModels []map[string]any, providersForModel ProvidersForModelFunc, clientVersion string) []ServedModelSummary {
	response := BuildResponseForClient(availableModels, providersForModel, false, clientVersion)
	entries, ok := response["models"].([]map[string]any)
	if !ok {
		return nil
	}
	templates, defaultTemplate, err := loadCodexClientModelTemplates()
	if err != nil {
		return nil
	}
	defaultSlug := stringModelValue(defaultTemplate, "slug")

	summaries := make([]ServedModelSummary, 0, len(entries))
	for _, entry := range entries {
		slug := stringModelValue(entry, "slug")
		if slug == "" {
			continue
		}
		summary := ServedModelSummary{
			Slug:                  slug,
			TemplateSlug:          codexClientMetadataModelID(slug),
			DisplayName:           stringModelValue(entry, "display_name"),
			Description:           stringModelValue(entry, "description"),
			ContextWindow:         intModelValue(entry, "context_window"),
			MaxContextWindow:      intModelValue(entry, "max_context_window"),
			Visibility:            stringModelValue(entry, "visibility"),
			DefaultReasoningLevel: stringModelValue(entry, "default_reasoning_level"),
			Priority:              intModelValue(entry, "priority"),
		}
		// TemplateSlug and DefaultTemplate describe how the served entry was built.
		// ServedFields compares against the entry a management UI lists for the
		// model: the entry keyed by the served slug, and the template the model
		// falls back to when the catalog has no entry for it.
		baseEntry := defaultTemplate
		if matched, ok := templates[summary.TemplateSlug]; ok {
			baseEntry = matched
		} else {
			summary.TemplateSlug = defaultSlug
			summary.DefaultTemplate = true
		}
		if matched, ok := templates[slug]; ok {
			baseEntry = matched
		}
		summary.SupportedReasoningLevels = servedReasoningLevels(entry)
		summary.ServedFields = servedFieldValues(baseEntry, entry)
		if providersForModel != nil {
			summary.Providers = providersForModel(slug)
		}
		summaries = append(summaries, summary)
	}
	return summaries
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

// servedFieldValues reports the fields whose served value differs from base, the
// entry the catalog supplies for the model, together with the value clients
// receive. A differing field is one the server decided for the client instead of
// reading it from the catalog entry: overriding it in the catalog may not reach
// clients, so a management UI shows the served value as the field's default. A
// field the served entry no longer carries keeps a nil value, which reports that
// clients receive no value for it.
func servedFieldValues(base, entry map[string]any) map[string]any {
	if len(base) == 0 || len(entry) == 0 {
		return nil
	}
	keys := make(map[string]struct{}, len(base)+len(entry))
	for key := range base {
		keys[key] = struct{}{}
	}
	for key := range entry {
		keys[key] = struct{}{}
	}

	fields := make(map[string]any, 4)
	for key := range keys {
		// The slug identifies the entry, so it is never a served field.
		if key == "slug" {
			continue
		}
		if !servedModelValuesEqual(base[key], entry[key]) {
			fields[key] = entry[key]
		}
	}
	if len(fields) == 0 {
		return nil
	}
	return fields
}

// servedModelValuesEqual compares two catalog values by their JSON encoding, so a
// number the catalog decoded as float64 equals the same number built as an int, and
// object keys compare independently of their order.
func servedModelValuesEqual(left, right any) bool {
	leftJSON, errLeft := json.Marshal(left)
	if errLeft != nil {
		return false
	}
	rightJSON, errRight := json.Marshal(right)
	if errRight != nil {
		return false
	}
	return bytes.Equal(leftJSON, rightJSON)
}
