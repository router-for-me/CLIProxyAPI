package cliproxy

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestRegisterModelsForAuth_GeminiCLIDiscoveryRegistersOnlyDiscoveredModels(t *testing.T) {
	service := &Service{
		geminiCLIModelDiscoverer: func(context.Context, *coreauth.Auth) (*executor.GeminiCLIDiscoveryResult, error) {
			return &executor.GeminiCLIDiscoveryResult{
				AvailableModels: []*registry.ModelInfo{
					{ID: "gemini-2.5-pro", Object: "model", OwnedBy: "gemini-cli", Type: "gemini-cli"},
				},
			}, nil
		},
	}
	auth := &coreauth.Auth{
		ID:       "auth-gemini-cli-discovery",
		Provider: "gemini-cli",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "oauth",
		},
	}

	reg := GlobalModelRegistry()
	reg.UnregisterClient(auth.ID)
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })

	service.registerModelsForAuth(auth)

	models := reg.GetAvailableModelsByProvider("gemini-cli")
	if len(models) != 1 {
		t.Fatalf("registered models = %d, want 1", len(models))
	}
	if models[0].ID != "gemini-2.5-pro" {
		t.Fatalf("registered model = %q, want gemini-2.5-pro", models[0].ID)
	}
}
