package management

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

// TestUpstreamModelToCatalog asserts the mapping that mirrors an upstream
// provider model entry into a models_catalog row. The invariants matter
// because the resulting row must match what the async registry hook would
// later upsert (same id/provider/owned_by) so the synchronous mirror is a
// no-op conflict rather than a duplicate key — see syncCatalogFromUpstreamProviders.
func TestUpstreamModelToCatalog(t *testing.T) {
	t.Run("alias preferred over name", func(t *testing.T) {
		got := upstreamModelToCatalog(store.UpstreamProviderModel{
			Name:  "gpt-4o-2024-08-06",
			Alias: "gpt-4o",
		}, "acme", 100)
		if got.ID != "gpt-4o" {
			t.Errorf("ID = %q; want alias gpt-4o", got.ID)
		}
		if got.Name != "gpt-4o-2024-08-06" {
			t.Errorf("Name = %q; want original name", got.Name)
		}
		if got.Provider != "acme" || got.OwnedBy != "acme" || got.OfficialProvider != "acme" {
			t.Errorf("provider fields = %q/%q/%q; want acme for all",
				got.Provider, got.OwnedBy, got.OfficialProvider)
		}
		if got.Object != "model" {
			t.Errorf("Object = %q; want model", got.Object)
		}
		if got.Type != "openai-compatibility" {
			t.Errorf("Type = %q; want openai-compatibility", got.Type)
		}
	})

	t.Run("name used when alias empty", func(t *testing.T) {
		got := upstreamModelToCatalog(store.UpstreamProviderModel{
			Name: "mimo-v2.5",
		}, "mimo", 100)
		if got.ID != "mimo-v2.5" {
			t.Errorf("ID = %q; want name fallback", got.ID)
		}
	})

	t.Run("image model maps to image type", func(t *testing.T) {
		got := upstreamModelToCatalog(store.UpstreamProviderModel{
			Name:  "dall-e-3",
			Alias: "dall-e-3",
			Image: true,
		}, "openai", 100)
		if got.Type != registry.OpenAIImageModelType {
			t.Errorf("Type = %q; want %q", got.Type, registry.OpenAIImageModelType)
		}
	})

	t.Run("display name falls back to alias/name", func(t *testing.T) {
		got := upstreamModelToCatalog(store.UpstreamProviderModel{
			Name:  "gpt-4o",
			Alias: "gpt-4o",
		}, "openai", 100)
		if got.DisplayName != "gpt-4o" {
			t.Errorf("DisplayName = %q; want gpt-4o fallback", got.DisplayName)
		}
	})

	t.Run("explicit display name preserved", func(t *testing.T) {
		got := upstreamModelToCatalog(store.UpstreamProviderModel{
			Name:        "gpt-4o",
			Alias:       "gpt-4o",
			DisplayName: "GPT-4o",
		}, "openai", 100)
		if got.DisplayName != "GPT-4o" {
			t.Errorf("DisplayName = %q; want GPT-4o", got.DisplayName)
		}
	})

	t.Run("empty alias and name yields empty id (skipped by caller)", func(t *testing.T) {
		got := upstreamModelToCatalog(store.UpstreamProviderModel{}, "openai", 100)
		if got.ID != "" {
			t.Errorf("ID = %q; want empty (caller drops empty-id rows)", got.ID)
		}
	})

	t.Run("modalities + thinking carried through", func(t *testing.T) {
		got := upstreamModelToCatalog(store.UpstreamProviderModel{
			Name:             "m",
			Alias:            "m",
			InputModalities:  []string{"TEXT", "IMAGE"},
			OutputModalities: []string{"TEXT"},
			Thinking:         map[string]any{"min": 1024},
		}, "p", 100)
		if len(got.InputModalities) != 2 || got.InputModalities[0] != "TEXT" {
			t.Errorf("InputModalities = %v", got.InputModalities)
		}
		if got.Thinking["min"] != 1024 {
			t.Errorf("thinking not carried: %v", got.Thinking)
		}
	})
}

// TestMirrorsCatalogByUpstreamName locks in which provider types are mirrored
// synchronously into models_catalog (the ones whose catalog rows are keyed by
// upstream name verbatim). Adding/removing a type here is a deliberate
// behavior change.
func TestMirrorsCatalogByUpstreamName(t *testing.T) {
	cases := []struct {
		pt   string
		want bool
	}{
		{"openai-compatibility", true},
		{"gemini-api-key", true},
		{"codex-api-key", true},
		{"xai-api-key", true},
		{"claude-api-key", true},
		{"vertex-api-key", true},
		{"interactions-api-key", true},
		{"neuralwatt-api-key", true},
		{"oauth:claude", false},
		{"oauth:codex", false},
		{"", false},
	}
	for _, c := range cases {
		if got := mirrorsCatalogByUpstreamName(c.pt); got != c.want {
			t.Errorf("mirrorsCatalogByUpstreamName(%q) = %v; want %v", c.pt, got, c.want)
		}
	}
}
