package cliproxy

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func TestRegisterModelsForAuth_GitLabUsesDiscoveredModels(t *testing.T) {
	service := &Service{cfg: &config.Config{}}
	auth := &coreauth.Auth{
		ID:       "gitlab-auth.json",
		Provider: "gitlab",
		Status:   coreauth.StatusActive,
		Metadata: map[string]any{
			"model_details": map[string]any{
				"model_provider": "anthropic",
				"model_name":     "claude-sonnet-4-5",
			},
		},
	}

	reg := registry.GetGlobalRegistry()
	reg.UnregisterClient(auth.ID)
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

	service.registerModelsForAuth(auth)
	models := reg.GetModelsForClient(auth.ID)
	if len(models) < 2 {
		t.Fatalf("expected stable alias and discovered model, got %d entries", len(models))
	}

	seenAlias := false
	seenDiscovered := false
	for _, model := range models {
		switch model.ID {
		case "gitlab-duo":
			seenAlias = true
		case "claude-sonnet-4-5":
			seenDiscovered = true
		}
	}
	if !seenAlias || !seenDiscovered {
		t.Fatalf("expected gitlab-duo and discovered model, got %+v", models)
	}
}
