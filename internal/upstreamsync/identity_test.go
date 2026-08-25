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
