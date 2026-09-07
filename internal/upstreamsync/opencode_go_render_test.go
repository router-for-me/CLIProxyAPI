package upstreamsync

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// TestRenderOpenCodeGo verifies an opencode-go row renders into the
// config.OpenCodeGo section with entries, headers, per-model wire formats,
// and the stamped row id intact.
func TestRenderOpenCodeGo(t *testing.T) {
	p := store.UpstreamProvider{
		ID:           7,
		ProviderType: TypeOpenCodeGo,
		Name:         "ocgo",
		BaseURL:      "https://opencode.ai/zen/go/v1",
		Headers:      map[string]string{"User-Agent": "opencode"},
		APIKeyEntries: []store.UpstreamProviderAPIKey{
			{ID: 3, APIKey: "sk-1", Name: "main"},
			{ID: 4, APIKey: "sk-2", Name: "off", Disabled: true},
		},
		Models: []store.UpstreamProviderModel{
			{Name: "glm-5.2", Alias: "glm-5.2"},
			{Name: "minimax-m3", Alias: "minimax-m3", WireFormat: "anthropic"},
		},
	}
	cfg := RenderConfig([]store.UpstreamProvider{p})
	if len(cfg.OpenCodeGo) != 1 {
		t.Fatalf("want 1 opencode-go row, got %d", len(cfg.OpenCodeGo))
	}
	row := cfg.OpenCodeGo[0]
	if row.Name != "ocgo" || row.BaseURL != "https://opencode.ai/zen/go/v1" {
		t.Fatalf("row identity lost: %+v", row)
	}
	if row.UpstreamProviderID != 7 {
		t.Fatalf("row id not stamped: %d", row.UpstreamProviderID)
	}
	if row.Headers["User-Agent"] != "opencode" {
		t.Fatalf("headers lost: %+v", row.Headers)
	}
	// The disabled entry is skipped; the active one keeps its id + key.
	if len(row.APIKeyEntries) != 1 {
		t.Fatalf("want 1 active entry, got %d", len(row.APIKeyEntries))
	}
	e := row.APIKeyEntries[0]
	if e.APIKey != "sk-1" || e.Name != "main" || e.UpstreamProviderEntryID != 3 {
		t.Fatalf("entry not rendered correctly: %+v", e)
	}
	if len(row.Models) != 2 {
		t.Fatalf("want 2 models, got %d", len(row.Models))
	}
	if row.Models[0].Name != "glm-5.2" || row.Models[0].WireFormat != "" {
		t.Fatalf("openai model wrong: %+v", row.Models[0])
	}
	if row.Models[1].Name != "minimax-m3" || row.Models[1].WireFormat != "anthropic" {
		t.Fatalf("anthropic wire format lost: %+v", row.Models[1])
	}
}

// TestRenderOpenCodeGoStrategyAndCooling covers the row-level strategy
// normalization and disable_cooling passthrough.
func TestRenderOpenCodeGoStrategyAndCooling(t *testing.T) {
	p := store.UpstreamProvider{
		ID:              9,
		ProviderType:    TypeOpenCodeGo,
		Name:            "ocgo2",
		BaseURL:         "https://opencode.ai/zen/go/v1",
		RoutingStrategy: "failover",
		ExtraConfig:     map[string]any{"disable_cooling": true},
	}
	cfg := RenderConfig([]store.UpstreamProvider{p})
	if len(cfg.OpenCodeGo) != 1 {
		t.Fatalf("want 1 row, got %d", len(cfg.OpenCodeGo))
	}
	row := cfg.OpenCodeGo[0]
	// The raw alias "failover" canonicalizes to "round-robin".
	if row.Strategy != "round-robin" {
		t.Fatalf("strategy = %q, want round-robin", row.Strategy)
	}
	if !row.DisableCooling {
		t.Fatal("disable_cooling not carried from extra_config")
	}
}
