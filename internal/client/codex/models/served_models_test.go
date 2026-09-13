package models

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

// servedSetsFor registers the given models under a throwaway client and returns the sets
// the server builds for them.
func servedSetsFor(t *testing.T, models []*registry.ModelInfo) CodexClientModelSets {
	t.Helper()
	clientID := "served-models-sets-test"
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient(clientID, "openai", models)
	t.Cleanup(func() { modelRegistry.UnregisterClient(clientID) })
	return BuildCodexClientModelSets(modelRegistry.GetAvailableModels("openai"), nil, false, "")
}

func servedSummaryFor(t *testing.T, slug string, models []*registry.ModelInfo) ServedModelSummary {
	t.Helper()
	for _, summary := range servedSetsFor(t, models).Summaries {
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

// A model the registry knows nothing about is composed from the default template alone,
// so it carries the capabilities of that template.
func TestBuildCodexClientModelSets_WithoutModelMetadataUsesTheDefaultTemplate(t *testing.T) {
	summary := servedSummaryFor(t, "served-plain-model", []*registry.ModelInfo{
		{ID: "served-plain-model", Object: "model", OwnedBy: "deepseek", Type: "openai"},
	})

	if summariseLevels(summary.SupportedReasoningLevels) != "low,medium,high,xhigh" {
		t.Errorf("reasoning levels = %v, want the template levels", summary.SupportedReasoningLevels)
	}
}

// Model metadata shapes the assembled entry, so the summary reports the values clients
// receive rather than what the catalog template says.
func TestBuildCodexClientModelSets_ReportsModelMetadata(t *testing.T) {
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
}

// A model whose metadata model id names a catalog entry is assembled from that entry.
func TestBuildCodexClientModelSets_UsesTheCatalogEntryTheMetadataNames(t *testing.T) {
	summary := servedSummaryFor(t, "served-alias-model", []*registry.ModelInfo{
		{
			ID:              "served-alias-model",
			Object:          "model",
			OwnedBy:         "openai",
			Type:            "openai",
			MetadataModelID: "gpt-5.6-sol",
		},
	})

	if summary.ContextWindow != 272000 || summary.MaxContextWindow != 872000 {
		t.Errorf("context window = %d/%d, want the catalog values 272000/872000", summary.ContextWindow, summary.MaxContextWindow)
	}
}

// The local override layer is applied to the assembled entries, which are the default
// configuration: the default entry keeps the assembled value and only the served entry
// carries the override.
func TestBuildCodexClientModelSets_AppliesTheLocalOverrideLayer(t *testing.T) {
	syncCodexClientModelOverrideForTest(t, `{"served-override-model":{"display_name":"Local Name"}}`)

	sets := servedSetsFor(t, []*registry.ModelInfo{
		{ID: "served-override-model", Object: "model", OwnedBy: "deepseek", Type: "openai"},
	})

	defaults, ok := codexClientModelEntryBySlug(sets.Defaults, "served-override-model")
	if !ok {
		t.Fatal("the default entry is missing from the sets")
	}
	if got := stringModelValue(defaults, "display_name"); got != "served-override-model" {
		t.Fatalf("default display_name = %v, want the assembled value", got)
	}
	served, ok := codexClientModelEntryBySlug(sets.Served, "served-override-model")
	if !ok {
		t.Fatal("the served entry is missing from the sets")
	}
	if got := stringModelValue(served, "display_name"); got != "Local Name" {
		t.Fatalf("served display_name = %v, want the override", got)
	}
	if got := sets.Origins["served-override-model"]; got != registry.CodexClientModelsOriginOverride {
		t.Fatalf("origin = %q, want %q", got, registry.CodexClientModelsOriginOverride)
	}
}

// A null override keeps a model out of the served list without removing its default
// entry, and an override for a model no provider serves is reported as having no effect.
func TestBuildCodexClientModelSets_ReportsOriginsAndIssues(t *testing.T) {
	syncCodexClientModelOverrideForTest(t, `{"served-hidden-model":null,"nowhere-model":{"display_name":"Nowhere"}}`)

	sets := servedSetsFor(t, []*registry.ModelInfo{
		{ID: "served-hidden-model", Object: "model", OwnedBy: "deepseek", Type: "openai"},
	})

	if _, ok := codexClientModelEntryBySlug(sets.Defaults, "served-hidden-model"); !ok {
		t.Fatal("a hidden model lost its default entry")
	}
	if _, ok := codexClientModelEntryBySlug(sets.Served, "served-hidden-model"); ok {
		t.Fatal("a model a null override hides is still served")
	}
	if got := sets.Origins["served-hidden-model"]; got != registry.CodexClientModelsOriginRemoved {
		t.Fatalf("origin = %q, want %q", got, registry.CodexClientModelsOriginRemoved)
	}
	if got := sets.Origins["nowhere-model"]; got != registry.CodexClientModelsOriginUnserved {
		t.Fatalf("origin = %q, want %q", got, registry.CodexClientModelsOriginUnserved)
	}
	if len(sets.Issues) != 1 || sets.Issues[0].Slug != "nowhere-model" {
		t.Fatalf("issues = %#v, want one issue for the model that is not served", sets.Issues)
	}
}

// syncCodexClientModelOverrideForTest points the override layer at a temporary file and
// installs the given document for the rest of the test.
func syncCodexClientModelOverrideForTest(t *testing.T, document string) {
	t.Helper()
	dir := t.TempDir()
	registry.SyncCodexClientModelsOverrideFile(filepath.Join(dir, "config.yaml"))
	t.Cleanup(func() {
		registry.SyncCodexClientModelsOverrideFile("")
		_ = registry.ClearCodexClientModelsOverride()
	})
	if err := registry.SetCodexClientModelsOverride([]byte(document)); err != nil {
		t.Fatalf("set override: %v", err)
	}
}

func codexClientModelEntryBySlug(entries []map[string]any, slug string) (map[string]any, bool) {
	for _, entry := range entries {
		if stringModelValue(entry, "slug") == slug {
			return entry, true
		}
	}
	return nil, false
}

var _ = json.Marshal
