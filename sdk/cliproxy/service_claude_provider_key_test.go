package cliproxy

import (
	"context"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	internalregistry "github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestRegisterModelsForAuth_ClaudeAPIKeyKeepsRowProviderKey(t *testing.T) {
	authID := "claude-row-provider-key"
	modelRegistry := internalregistry.GetGlobalRegistry()
	modelRegistry.UnregisterClient(authID)
	t.Cleanup(func() { modelRegistry.UnregisterClient(authID) })

	service := &Service{cfg: &internalconfig.Config{ClaudeKey: []internalconfig.ClaudeKey{{
		APIKey: "test-key",
		Models: []internalconfig.ClaudeModel{{Name: "claude-upstream", Alias: "claude-row-model"}},
	}}}}
	auth := &coreauth.Auth{
		ID:       authID,
		Provider: "claude",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			coreauth.AttributeAPIKey:      "test-key",
			coreauth.AttributeAuthKind:    coreauth.AuthKindAPIKey,
			coreauth.AttributeConfigIndex: "0",
			coreauth.AttributeSource:      "config:claude[test]",
			"provider_key":                "claude:94",
		},
	}

	service.registerModelsForAuth(context.Background(), auth)
	providers := modelRegistry.GetModelProviders("claude-row-model")
	if len(providers) != 1 || providers[0] != "claude:94" {
		t.Fatalf("GetModelProviders() = %v, want [claude:94]", providers)
	}
}
