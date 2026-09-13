package models

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
		if _, matched := templates[summary.TemplateSlug]; !matched {
			summary.TemplateSlug = defaultSlug
			summary.DefaultTemplate = true
		}
		if providersForModel != nil {
			summary.Providers = providersForModel(slug)
		}
		summaries = append(summaries, summary)
	}
	return summaries
}
