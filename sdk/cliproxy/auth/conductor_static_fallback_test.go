package auth

import (
	"context"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestAuthSupportsRouteModelFallback(t *testing.T) {
	mgr := NewManager(nil, nil, nil)
	globalReg := registry.GetGlobalRegistry()

	authAntigravity := &Auth{
		ID:       "auth-ag-1",
		Provider: "antigravity",
	}
	authClaude := &Auth{
		ID:       "auth-claude-1",
		Provider: "claude",
	}
	authCodex := &Auth{
		ID:       "auth-codex-1",
		Provider: "codex",
	}

	// 1. Static model present in Antigravity catalog (e.g. gemini-3.7-flash-high)
	if !mgr.authSupportsRouteModel(globalReg, authAntigravity, "gemini-3.7-flash-high") {
		t.Fatal("authSupportsRouteModel(authAntigravity, gemini-3.7-flash-high) = false, want true via static catalog fallback")
	}
	if mgr.authSupportsRouteModel(globalReg, authClaude, "gemini-3.7-flash-high") {
		t.Fatal("authSupportsRouteModel(authClaude, gemini-3.7-flash-high) = true, want false")
	}

	// 2. Static model present in both Claude and Antigravity catalogs (e.g. claude-sonnet-4-6)
	if !mgr.authSupportsRouteModel(globalReg, authAntigravity, "claude-sonnet-4-6") {
		t.Fatal("authSupportsRouteModel(authAntigravity, claude-sonnet-4-6) = false, want true")
	}
	if !mgr.authSupportsRouteModel(globalReg, authClaude, "claude-sonnet-4-6") {
		t.Fatal("authSupportsRouteModel(authClaude, claude-sonnet-4-6) = false, want true")
	}

	// 3. Heuristic-only model routes (e.g. deepseek-chat -> codex/claude)
	if !mgr.authSupportsRouteModel(globalReg, authCodex, "deepseek-chat") {
		t.Fatal("authSupportsRouteModel(authCodex, deepseek-chat) = false, want true via heuristic fallback")
	}
	if !mgr.authSupportsRouteModel(globalReg, authClaude, "deepseek-chat") {
		t.Fatal("authSupportsRouteModel(authClaude, deepseek-chat) = false, want true via heuristic fallback")
	}
	if mgr.authSupportsRouteModel(globalReg, authAntigravity, "deepseek-chat") {
		t.Fatal("authSupportsRouteModel(authAntigravity, deepseek-chat) = true, want false")
	}

	// 4. Preserves per-auth model exclusions when auth has a registered model set
	globalReg.RegisterClient(authCodex.ID, "codex", []*registry.ModelInfo{
		{ID: "gpt-4o-mini"},
	})
	t.Cleanup(func() {
		globalReg.UnregisterClient(authCodex.ID)
	})

	// When authCodex has registered models, only gpt-4o-mini is allowed, gpt-4o is excluded
	if !mgr.authSupportsRouteModel(globalReg, authCodex, "gpt-4o-mini") {
		t.Fatal("authSupportsRouteModel(authCodex, gpt-4o-mini) = false, want true")
	}
	if mgr.authSupportsRouteModel(globalReg, authCodex, "gpt-4o") {
		t.Fatal("authSupportsRouteModel(authCodex, gpt-4o) = true, want false because gpt-4o is excluded from snapshot")
	}

	// 5. Scheduler supportsModel test with empty vs populated supportedModelSet
	metaUnindexed := &scheduledAuthMeta{
		auth:              authAntigravity,
		supportedModelSet: nil,
	}
	if !metaUnindexed.supportsModel("gemini-3.7-flash-high") {
		t.Fatal("metaUnindexed.supportsModel(gemini-3.7-flash-high) = false, want true")
	}
	if !metaUnindexed.supportsModel("claude-sonnet-4-6") {
		t.Fatal("metaUnindexed.supportsModel(claude-sonnet-4-6) = false, want true")
	}

	metaNarrowed := &scheduledAuthMeta{
		auth: authCodex,
		supportedModelSet: map[string]struct{}{
			"gpt-4o-mini": {},
		},
	}
	if !metaNarrowed.supportsModel("gpt-4o-mini") {
		t.Fatal("metaNarrowed.supportsModel(gpt-4o-mini) = false, want true")
	}
	if metaNarrowed.supportsModel("gpt-4o") {
		t.Fatal("metaNarrowed.supportsModel(gpt-4o) = true, want false because gpt-4o was narrowed out")
	}
}

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
