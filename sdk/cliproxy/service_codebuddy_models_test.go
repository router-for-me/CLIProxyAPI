package cliproxy

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/constant"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestRegisterModelsForAuthCodeBuddyCNOAuthIgnoresAPIKeyModels(t *testing.T) {
	const authID = "codebuddy-cn-oauth-models"
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.UnregisterClient(authID)
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(authID)
	})

	service := &Service{cfg: &config.Config{
		CodeBuddyCNKey: []config.CodeBuddyCNKey{
			{
				APIKey:  "api-key",
				BaseURL: "https://copilot.tencent.com/v2",
				Models: []config.CodeBuddyCNModel{
					{Name: "hy3"},
				},
			},
		},
	}}
	auth := &coreauth.Auth{
		ID:       authID,
		Provider: constant.CodeBuddyCN,
		Attributes: map[string]string{
			coreauth.AttributeAuthKind: coreauth.AuthKindOAuth,
			"base_url":                 "https://copilot.tencent.com/v2",
		},
	}

	service.registerModelsForAuth(context.Background(), auth)

	got := modelRegistry.GetModelsForClient(authID)
	want := registry.GetCodeBuddyCNModels()
	if len(got) != len(want) {
		t.Fatalf("registered models = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].ID != want[i].ID {
			t.Fatalf("registered model[%d] = %q, want %q", i, got[i].ID, want[i].ID)
		}
	}
}
