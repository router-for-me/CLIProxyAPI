package cliproxy

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func TestOpenAICompat_ZaiGLM47_OnlyCerebrasRegisters(t *testing.T) {
	cfg := &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{
			{
				Name: "cerebras",
				Models: []config.OpenAICompatibilityModel{
					{Name: "zai-glm-4.7", Alias: "zai-glm-4.7"},
				},
			},
			{
				Name: "other",
				Models: []config.OpenAICompatibilityModel{
					{Name: "zai-glm-4.7", Alias: "zai-glm-4.7"},
				},
			},
		},
	}

	svc := &Service{cfg: cfg}

	authCerebras := &coreauth.Auth{
		ID:       "auth-cerebras",
		Provider: "cerebras",
		Label:    "cerebras",
		Attributes: map[string]string{
			"auth_kind":    "apikey",
			"compat_name":  "cerebras",
			"provider_key": "cerebras",
		},
	}
	authOther := &coreauth.Auth{
		ID:       "auth-other",
		Provider: "other",
		Label:    "other",
		Attributes: map[string]string{
			"auth_kind":    "apikey",
			"compat_name":  "other",
			"provider_key": "other",
		},
	}

	reg := registry.GetGlobalRegistry()
	t.Cleanup(func() {
		reg.UnregisterClient(authCerebras.ID)
		reg.UnregisterClient(authOther.ID)
	})

	svc.registerModelsForAuth(authCerebras)
	svc.registerModelsForAuth(authOther)

	providers := reg.GetModelProviders("zai-glm-4.7")
	if len(providers) != 1 || providers[0] != "cerebras" {
		t.Fatalf("expected providers [cerebras], got %v", providers)
	}
	if reg.ClientSupportsModel(authOther.ID, "zai-glm-4.7") {
		t.Fatalf("expected non-cerebras client to not support zai-glm-4.7")
	}
}

