package upstreamsync

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

func TestOpenAICompatRenderCopiesEntryIdentity(t *testing.T) {
	provider := store.UpstreamProvider{
		ProviderType: "openai-compatibility",
		ID:           17,
		Name:         "gateway",
		APIKeyEntries: []store.UpstreamProviderAPIKey{{
			ID:       42,
			Name:     "team-a",
			APIKey:   "placeholder-entry-key",
			ProxyURL: "http://proxy.example",
		}},
	}

	got := openAICompatFromProvider(provider)
	if len(got.APIKeyEntries) != 1 {
		t.Fatalf("rendered %d API key entries, want 1", len(got.APIKeyEntries))
	}
	entry := got.APIKeyEntries[0]
	if entry.UpstreamProviderEntryID != 42 || entry.Name != "team-a" || entry.APIKey != "placeholder-entry-key" || entry.ProxyURL != "http://proxy.example" {
		t.Fatalf("rendered entry = %+v, want persisted identity and values copied", entry)
	}
}

func TestOpenAICompatSeedCopiesEntryNameWithoutID(t *testing.T) {
	provider := providerFromOpenAICompat(config.OpenAICompatibility{
		Name: "gateway",
		APIKeyEntries: []config.OpenAICompatibilityAPIKey{{
			UpstreamProviderEntryID: 42,
			Name:                    "team-a",
			APIKey:                  "placeholder-entry-key",
			ProxyURL:                "http://proxy.example",
		}},
	})

	if len(provider.APIKeyEntries) != 1 {
		t.Fatalf("seeded %d API key entries, want 1", len(provider.APIKeyEntries))
	}
	entry := provider.APIKeyEntries[0]
	if entry.ID != 0 {
		t.Fatalf("seeded entry ID = %d, want zero before persistence", entry.ID)
	}
	if entry.Name != "team-a" || entry.APIKey != "placeholder-entry-key" || entry.ProxyURL != "http://proxy.example" {
		t.Fatalf("seeded entry = %+v, want name and values copied", entry)
	}
}

func TestOpenAICompatEntryIdentityNameRoundTrip(t *testing.T) {
	seeded := providerFromOpenAICompat(config.OpenAICompatibility{
		Name: "gateway",
		APIKeyEntries: []config.OpenAICompatibilityAPIKey{{
			Name:   "team-a",
			APIKey: "placeholder-entry-key",
		}},
	})
	rendered := openAICompatFromProvider(seeded)
	if len(rendered.APIKeyEntries) != 1 || rendered.APIKeyEntries[0].Name != "team-a" {
		t.Fatalf("name did not survive seed/render round trip: %+v", rendered.APIKeyEntries)
	}
}

// TestMetaAPIKeySeedRenderRoundTrip pins that a meta-api-key row seeds from
// config.MetaKey (CodexKey alias) via providerFromCodexKey with the meta
// provider type and renders back into cfg.MetaKey via RenderConfig — the
// same one-item-to-one-row round trip the xai-api-key path guarantees.
func TestMetaAPIKeySeedRenderRoundTrip(t *testing.T) {
	seeded := providerFromCodexKey(config.CodexKey{
		APIKey:         "meta-key",
		BaseURL:        "https://api.meta.ai/v1",
		Prefix:         "meta/",
		Priority:       3,
		Websockets:     false,
		ProxyURL:       "http://proxy.example",
		ExcludedModels: []string{"muse-code-old"},
		Models: []config.CodexModel{
			{Name: "muse-code", Alias: "meta-flash"},
		},
	}, TypeMetaAPIKey)
	if seeded.ProviderType != TypeMetaAPIKey {
		t.Fatalf("seeded provider type = %q, want %q", seeded.ProviderType, TypeMetaAPIKey)
	}

	rendered := RenderConfig([]store.UpstreamProvider{seeded})
	if len(rendered.MetaKey) != 1 {
		t.Fatalf("rendered %d meta keys, want 1", len(rendered.MetaKey))
	}
	got := rendered.MetaKey[0]
	if got.APIKey != "meta-key" || got.BaseURL != "https://api.meta.ai/v1" {
		t.Fatalf("rendered meta key = %+v", got)
	}
	if got.Prefix != "meta/" || got.Priority != 3 {
		t.Fatalf("rendered prefix/priority = %q/%d, want meta//3", got.Prefix, got.Priority)
	}
	if got.ProxyURL != "http://proxy.example" {
		t.Fatalf("rendered proxy URL = %q", got.ProxyURL)
	}
	if len(got.Models) != 1 || got.Models[0].Name != "muse-code" || got.Models[0].Alias != "meta-flash" {
		t.Fatalf("rendered models = %+v", got.Models)
	}
	if len(got.ExcludedModels) != 1 || got.ExcludedModels[0] != "muse-code-old" {
		t.Fatalf("rendered excluded models = %+v", got.ExcludedModels)
	}
}
