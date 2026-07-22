package codexintegration

import (
	"bytes"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestCompileCatalogBuildsFeaturedStableSurface(t *testing.T) {
	integration := config.DefaultCodexIntegrationConfig()
	integration.Enabled = true
	models, providers := catalogTestModels()

	catalog, err := CompileCatalog(models, providers, integration)
	if err != nil {
		t.Fatalf("CompileCatalog() error = %v", err)
	}

	wantFeatured := []string{
		"gpt-5.6-sol",
		"xai/grok-4.5",
		"antigravity/gemini-3.6-flash",
		"antigravity/gemini-3.1-pro",
		"antigravity/claude-opus-4-6-thinking",
	}
	if len(catalog.Models) < len(wantFeatured) {
		t.Fatalf("len(Models) = %d, want at least %d", len(catalog.Models), len(wantFeatured))
	}
	for i, want := range wantFeatured {
		if got := catalogString(catalog.Models[i], "slug"); got != want {
			t.Fatalf("Models[%d].slug = %q, want %q", i, got, want)
		}
		if got := catalogInt(catalog.Models[i], "priority"); got != i+1 {
			t.Fatalf("Models[%d].priority = %d, want %d", i, got, i+1)
		}
	}

	bySlug := catalogBySlug(catalog.Models)
	for _, slug := range []string{"gpt-5.5", "gpt-5.4", "xai/grok-build-0.1"} {
		if bySlug[slug] == nil {
			t.Fatalf("catalog missing %q", slug)
		}
	}
	if got, _ := bySlug["antigravity/gemini-3.1-pro"]["supports_search_tool"].(bool); got {
		t.Fatal("AntiGravity Gemini advertises hosted search")
	}
	if _, exists := bySlug["gemini-pro-agent"]; exists {
		t.Fatal("catalog exposed unqualified AntiGravity upstream model")
	}
	for _, model := range catalog.Models {
		if catalogString(model, "visibility") == "list" && catalogString(model, "multi_agent_version") != "v1" {
			t.Fatalf("model %q multi_agent_version = %#v, want v1", catalogString(model, "slug"), model["multi_agent_version"])
		}
	}
	if catalog.Revision == "" || catalog.SourceRevision == 0 || catalog.MappingRevision == "" {
		t.Fatalf("catalog revisions incomplete: %#v", catalog)
	}
	if err := ValidateCatalog(catalog, integration); err != nil {
		t.Fatalf("ValidateCatalog() error = %v", err)
	}
}

func TestCompileCatalogRejectsMissingMappedUpstream(t *testing.T) {
	integration := config.DefaultCodexIntegrationConfig()
	integration.Enabled = true
	models, providers := catalogTestModels()
	filtered := models[:0]
	for _, model := range models {
		if catalogString(model, "id") != "gemini-pro-agent" {
			filtered = append(filtered, model)
		}
	}

	missingProviders := func(model string) []string {
		if model == "gemini-pro-agent" {
			return nil
		}
		return providers(model)
	}
	if _, err := CompileCatalog(filtered, missingProviders, integration); err == nil {
		t.Fatal("CompileCatalog() error = nil, want missing upstream error")
	}
}

func TestCompileCatalogIsDeterministicAcrossRegistryOrder(t *testing.T) {
	integration := config.DefaultCodexIntegrationConfig()
	integration.Enabled = true
	models, providers := catalogTestModels()

	first, err := CompileCatalog(models, providers, integration)
	if err != nil {
		t.Fatalf("CompileCatalog(first) error = %v", err)
	}
	for left, right := 0, len(models)-1; left < right; left, right = left+1, right-1 {
		models[left], models[right] = models[right], models[left]
	}
	second, err := CompileCatalog(models, providers, integration)
	if err != nil {
		t.Fatalf("CompileCatalog(second) error = %v", err)
	}
	firstJSON, err := first.Marshal()
	if err != nil {
		t.Fatalf("first.Marshal() error = %v", err)
	}
	secondJSON, err := second.Marshal()
	if err != nil {
		t.Fatalf("second.Marshal() error = %v", err)
	}
	if !bytes.Equal(firstJSON, secondJSON) {
		t.Fatalf("catalog output changed with registry order\nfirst: %s\nsecond: %s", firstJSON, secondJSON)
	}
}

func catalogTestModels() ([]map[string]any, ModelProvidersFunc) {
	models := []map[string]any{
		{"id": "gpt-5.5", "display_name": "GPT-5.5"},
		{"id": "gpt-5.6-sol", "display_name": "GPT-5.6 Sol"},
		{"id": "gpt-5.4", "display_name": "GPT-5.4"},
		{"id": "grok-4.5", "display_name": "Grok 4.5", "context_length": 262144},
		{"id": "grok-build-0.1", "display_name": "Grok Build", "context_length": 262144},
		{"id": "gemini-3.6-flash", "display_name": "Gemini 3.6 Flash", "context_length": 1048576},
		{"id": "gemini-pro-agent", "display_name": "Gemini Pro Agent", "context_length": 1048576},
		{"id": "claude-opus-4-6-thinking", "display_name": "Claude Opus", "context_length": 200000},
	}
	providers := map[string][]string{
		"gpt-5.5":                  {"codex"},
		"gpt-5.6-sol":              {"codex"},
		"gpt-5.4":                  {"codex"},
		"grok-4.5":                 {"xai"},
		"grok-build-0.1":           {"xai"},
		"gemini-3.6-flash":         {"antigravity"},
		"gemini-pro-agent":         {"antigravity"},
		"claude-opus-4-6-thinking": {"antigravity"},
	}
	return models, func(model string) []string { return providers[model] }
}

func catalogBySlug(models []map[string]any) map[string]map[string]any {
	result := make(map[string]map[string]any, len(models))
	for _, model := range models {
		result[catalogString(model, "slug")] = model
	}
	return result
}
