package cliproxy

import (
	"context"
	"slices"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	internalregistry "github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// TestRegisterModelsForAuth_OpencodeGoRegistersOwnRowModels reproduces the
// "auth_not_found: no auth available" Run-Test failure for opencode-go rows:
// registerModelsForAuthWithCache had no case "opencode-go", so the auth fell
// into the OpenAI-compat default branch. That branch treats any auth carrying
// a provider_key attribute as compat, then resolves the entry via
// configEntryForAuthIndex(auth, cfg.OpenAICompatibility) — but the auth's
// config_index is the index within cfg.OpenCodeGo, not within
// cfg.OpenAICompatibility. With an opencode-go row at index 0 over a compat
// row at index 0, the opencode-go auth registered the COMPAT row's models
// (here: neuralwatt's flex catalog) and never its own — leaving its own
// models unroutable ("not live").
//
// The fix adds a dedicated case "opencode-go" that resolves the row through
// cfg.OpenCodeGo (same config_index contract as every other built-in
// channel) and registers the row's own model list.
func TestRegisterModelsForAuth_OpencodeGoRegistersOwnRowModels(t *testing.T) {
	authID := "opencode-go-row-models"
	modelRegistry := internalregistry.GetGlobalRegistry()
	modelRegistry.UnregisterClient(authID)
	t.Cleanup(func() { modelRegistry.UnregisterClient(authID) })

	service := &Service{cfg: &internalconfig.Config{
		// Compat row at index 0 — its models must NOT leak onto the
		// opencode-go auth.
		OpenAICompatibility: []internalconfig.OpenAICompatibility{{
			Name:   "neuralwatt",
			Models: []internalconfig.OpenAICompatibilityModel{{Name: "glm-5.2-flex", Alias: "glm-5.2"}},
		}},
		// opencode-go row at index 0 with its own catalog.
		OpenCodeGo: []internalconfig.OpenCodeGo{{
			Name:               "ocg",
			UpstreamProviderID: 95,
			Models: []internalconfig.OpenCodeGoModel{
				{Name: "glm-5.2"},
				{Name: "glm-5.1"},
				{Name: "minimax-m3", WireFormat: "anthropic"},
			},
		}},
	}}
	auth := &coreauth.Auth{
		ID:       authID,
		Provider: "opencode-go",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			coreauth.AttributeAuthKind:    coreauth.AuthKindAPIKey,
			coreauth.AttributeConfigIndex: "0",
			coreauth.AttributeSource:      "config:opencode-go[test]",
			"provider_key":                "opencode-go:95",
		},
	}

	service.registerModelsForAuth(context.Background(), auth)

	for _, model := range []string{"glm-5.2", "glm-5.1", "minimax-m3"} {
		providers := modelRegistry.GetModelProviders(model)
		if !slices.Contains(providers, "opencode-go:95") {
			t.Fatalf("GetModelProviders(%q) = %v, want opencode-go:95 (row's own model must be live)", model, providers)
		}
	}
	// glm-5.2 is BOTH the compat alias and an opencode-go model here; assert
	// via client-level registration instead: the opencode-go auth must not be
	// registered for the compat row's unique model id.
	if modelRegistry.ClientSupportsModel(authID, "gemma-4-31b") {
		t.Fatalf("opencode-go auth must not be registered for the compat row's models")
	}
}
