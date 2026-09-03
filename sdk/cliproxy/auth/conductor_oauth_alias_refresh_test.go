package auth

import (
	"context"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestSetOAuthModelAliasRefreshesScheduler(t *testing.T) {
	mgr := NewManager(nil, nil, nil)
	globalReg := registry.GetGlobalRegistry()

	auth := &Auth{
		ID:       "auth-test-alias",
		Provider: "claude",
	}
	globalReg.RegisterClient(auth.ID, "claude", []*registry.ModelInfo{
		{ID: "claude-sonnet-4-5"},
	})
	t.Cleanup(func() {
		globalReg.UnregisterClient(auth.ID)
	})

	if _, err := mgr.Register(context.Background(), auth); err != nil {
		t.Fatal(err)
	}

	// Update alias table dynamically
	mgr.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		"claude": {
			{Alias: "fast-model", Name: "claude-sonnet-4-5"},
		},
	})

	// After SetOAuthModelAlias, scheduler is refreshed and supports the aliased model
	if !mgr.authSupportsRouteModel(globalReg, auth, "fast-model") {
		t.Fatal("expected auth to support fast-model after SetOAuthModelAlias")
	}
}
