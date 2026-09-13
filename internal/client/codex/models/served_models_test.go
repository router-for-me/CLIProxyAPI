package models

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

// servedSummaryFor registers the given models under a throwaway client and returns
// the summary of the model named slug.
func servedSummaryFor(t *testing.T, slug string, models []*registry.ModelInfo) ServedModelSummary {
	t.Helper()
	clientID := "served-models-summary-test"
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient(clientID, "openai", models)
	t.Cleanup(func() { modelRegistry.UnregisterClient(clientID) })

	for _, summary := range SummarizeServedModels(modelRegistry.GetAvailableModels("openai"), nil, "") {
		if summary.Slug == slug {
			return summary
		}
	}
	t.Fatalf("model %q is missing from the served summaries", slug)
	return ServedModelSummary{}
}

func summariseLevels(levels []ServedReasoningLevel) string {
	efforts := make([]string, 0, len(levels))
	for _, level := range levels {
		efforts = append(efforts, level.Effort)
	}
	return strings.Join(efforts, ",")
}

// A model the registry knows nothing about is composed from the default template
// alone, so the template stays the reported source of the context window and the
// reasoning levels, and clients receive them unchanged.
func TestSummarizeServedModels_WithoutModelMetadataFallsBackToTheTemplate(t *testing.T) {
	summary := servedSummaryFor(t, "served-plain-model", []*registry.ModelInfo{
		{ID: "served-plain-model", Object: "model", OwnedBy: "deepseek", Type: "openai"},
	})

	if !summary.DefaultTemplate || summary.TemplateSlug != "gpt-5.5" {
		t.Fatalf("template provenance = %q/%v, want the default template", summary.TemplateSlug, summary.DefaultTemplate)
	}
	for _, field := range []string{"context_window", "max_context_window", "supported_reasoning_levels"} {
		if _, ok := summary.ServedFields[field]; ok {
			t.Errorf("field %q reported with a served value, but the template supplies it: %v", field, summary.ServedFields)
		}
	}
	if summariseLevels(summary.SupportedReasoningLevels) != "low,medium,high,xhigh" {
		t.Errorf("reasoning levels = %v, want the template levels", summary.SupportedReasoningLevels)
	}
}

// Model metadata outranks the template, so the fields it supplies must be reported
// with the value clients receive: overriding them in the catalog does not reach
// Codex clients.
func TestSummarizeServedModels_ReportsModelMetadataAsTheSource(t *testing.T) {
	summary := servedSummaryFor(t, "served-metadata-model", []*registry.ModelInfo{
		{
			ID:               "served-metadata-model",
			Object:           "model",
			OwnedBy:          "deepseek",
			Type:             "openai",
			ContextLength:    128000,
			MaxContextLength: 200000,
			Thinking:         &registry.ThinkingSupport{Levels: []string{"low", "xhigh"}},
		},
	})

	if summary.ContextWindow != 200000 || summary.MaxContextWindow != 200000 {
		t.Errorf("context window = %d/%d, want the model metadata value 200000", summary.ContextWindow, summary.MaxContextWindow)
	}
	if summariseLevels(summary.SupportedReasoningLevels) != "low,xhigh" {
		t.Errorf("reasoning levels = %v, want the model metadata levels", summary.SupportedReasoningLevels)
	}
	if summary.DefaultReasoningLevel != "low" {
		t.Errorf("default reasoning level = %q, want low", summary.DefaultReasoningLevel)
	}
	if got := summary.ServedFields["context_window"]; got != 200000 {
		t.Errorf("served context_window = %v, want 200000", got)
	}
	if got := summary.ServedFields["max_context_window"]; got != 200000 {
		t.Errorf("served max_context_window = %v, want 200000", got)
	}
	if got := summary.ServedFields["default_reasoning_level"]; got != "low" {
		t.Errorf("served default_reasoning_level = %v, want low", got)
	}
	if _, ok := summary.ServedFields["supported_reasoning_levels"]; !ok {
		t.Errorf("supported_reasoning_levels is missing from the served fields: %v", summary.ServedFields)
	}
}

// A catalog entry the model resolves to through its metadata model id supplies the
// whole entry, so the server recomputes nothing.
func TestSummarizeServedModels_KeepsCatalogEntryWhenMetadataModelIDMatches(t *testing.T) {
	summary := servedSummaryFor(t, "served-alias-model", []*registry.ModelInfo{
		{
			ID:              "served-alias-model",
			Object:          "model",
			OwnedBy:         "openai",
			Type:            "openai",
			MetadataModelID: "gpt-5.6-sol",
		},
	})

	if summary.DefaultTemplate || summary.TemplateSlug != "gpt-5.6-sol" {
		t.Fatalf("template provenance = %q/%v, want the catalog entry gpt-5.6-sol", summary.TemplateSlug, summary.DefaultTemplate)
	}
	if len(summary.ServedFields) != 0 {
		t.Errorf("served fields = %v, want none", summary.ServedFields)
	}
	if summary.ContextWindow != 272000 || summary.MaxContextWindow != 872000 {
		t.Errorf("context window = %d/%d, want the catalog values 272000/872000", summary.ContextWindow, summary.MaxContextWindow)
	}
}

func TestSummarizeServedModels_ComparesNumbersAcrossJSONDecoding(t *testing.T) {
	if !servedModelValuesEqual(float64(272000), 272000) {
		t.Error("a catalog float64 and a built int must compare equal")
	}
	if !servedModelValuesEqual(map[string]any{"id": "a", "name": "b"}, map[string]any{"name": "b", "id": "a"}) {
		t.Error("object keys must compare independently of their order")
	}
	if servedModelValuesEqual([]any{}, nil) {
		t.Error("an empty array differs from a missing field")
	}
}
