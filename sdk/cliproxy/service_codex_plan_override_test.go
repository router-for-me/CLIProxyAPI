package cliproxy

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestRegisterModelsForAuth_CodexOAuthPlanOverride(t *testing.T) {
	t.Setenv("CODEX_OAUTH_PLAN_OVERRIDE", "pro")

	service := &Service{}
	auth := &coreauth.Auth{
		ID:       "auth-codex-override",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"plan_type": "free",
		},
	}

	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.UnregisterClient(auth.ID)
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(auth.ID)
	})

	service.registerModelsForAuth(auth)

	models := modelRegistry.GetModelsForClient(auth.ID)
	if len(models) == 0 {
		t.Fatal("expected codex models to be registered")
	}

	has54 := false
	for _, model := range models {
		if model == nil {
			continue
		}
		if model.ID == "gpt-5.4" {
			has54 = true
			break
		}
	}

	if !has54 {
		t.Fatal("expected CODEX_OAUTH_PLAN_OVERRIDE=pro to expose gpt-5.4")
	}
}
