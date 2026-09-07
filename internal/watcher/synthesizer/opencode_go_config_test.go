package synthesizer

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// TestConfigSynthesizer_OpenCodeGoKeys verifies one auth per active
// api_key_entries row, with routing keys, base_url, models hash, headers,
// and cooling metadata stamped the same way as the other key providers.
func TestConfigSynthesizer_OpenCodeGoKeys(t *testing.T) {
	cfg := &config.Config{
		OpenCodeGo: []config.OpenCodeGo{
			{
				Name:               "ocgo",
				BaseURL:            "https://opencode.ai/zen/go/v1",
				Prefix:             "zen",
				UpstreamProviderID: 7,
				Headers:            map[string]string{"User-Agent": "opencode"},
				APIKeyEntries: []config.OpenCodeGoKey{
					{APIKey: "sk-1", Name: "main", UpstreamProviderEntryID: 3},
					{APIKey: "sk-2", Name: "off", UpstreamProviderEntryID: 4, Disabled: true},
					{APIKey: "sk-3", Name: "alt", UpstreamProviderEntryID: 5, Priority: intPtr(2)},
				},
				Models: []config.OpenCodeGoModel{
					{Name: "glm-5.2", Alias: "glm-5.2"},
					{Name: "minimax-m3", Alias: "minimax-m3", WireFormat: "anthropic"},
				},
				DisableCooling: true,
			},
		},
	}

	synth := NewConfigSynthesizer()
	ctx := &SynthesisContext{
		Config:      cfg,
		Now:         time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		IDGenerator: NewStableIDGenerator(),
	}
	auths, err := synth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	// Only the synthesizer's opencode-go output matters here; filter.
	var mine []*coreauth.Auth
	for _, a := range auths {
		if a.Provider == "opencode-go" {
			mine = append(mine, a)
		}
	}
	if len(mine) != 2 {
		t.Fatalf("want 2 opencode-go auths (disabled skipped), got %d", len(mine))
	}
	first := mine[0]
	if first.Label != "opencode-go-apikey" {
		t.Errorf("label = %s, want opencode-go-apikey", first.Label)
	}
	if first.Prefix != "zen" {
		t.Errorf("prefix = %s, want zen", first.Prefix)
	}
	if first.Attributes["api_key"] != "sk-1" {
		t.Errorf("api_key = %s, want sk-1", first.Attributes["api_key"])
	}
	if first.Attributes["base_url"] != "https://opencode.ai/zen/go/v1" {
		t.Errorf("base_url = %s", first.Attributes["base_url"])
	}
	if got := first.Attributes["provider_key"]; got != "opencode-go:7" {
		t.Errorf("provider_key = %q, want opencode-go:7", got)
	}
	if got := first.Attributes[coreauth.AttributeEntryProviderKey]; got != "opencode-go:7:key-3" {
		t.Errorf("entry_provider_key = %q, want opencode-go:7:key-3", got)
	}
	if first.Attributes["models_hash"] == "" {
		t.Error("models_hash not stamped")
	}
	if first.Attributes["header:User-Agent"] != "opencode" {
		t.Errorf("header User-Agent = %q, want opencode", first.Attributes["header:User-Agent"])
	}
	if v, ok := first.Metadata["disable_cooling"].(bool); !ok || !v {
		t.Errorf("disable_cooling metadata = %v", first.Metadata["disable_cooling"])
	}
	// Entry priority overrides inherit.
	if got := mine[1].Attributes["priority"]; got != "2" {
		t.Errorf("entry priority = %q, want 2", got)
	}
}

// TestConfigSynthesizer_OpenCodeGoKeys_DisabledRow verifies a disabled row
// synthesizes no auths.
func TestConfigSynthesizer_OpenCodeGoKeys_DisabledRow(t *testing.T) {
	cfg := &config.Config{
		OpenCodeGo: []config.OpenCodeGo{
			{Name: "off", Disabled: true, APIKeyEntries: []config.OpenCodeGoKey{{APIKey: "sk-1"}}},
		},
	}
	synth := NewConfigSynthesizer()
	ctx := &SynthesisContext{
		Config:      cfg,
		Now:         time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
		IDGenerator: NewStableIDGenerator(),
	}
	auths, err := synth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	for _, a := range auths {
		if a.Provider == "opencode-go" {
			t.Fatalf("disabled row synthesized an auth: %+v", a)
		}
	}
}

func intPtr(v int) *int { return &v }
