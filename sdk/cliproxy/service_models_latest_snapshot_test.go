package cliproxy

import (
	"context"
	"testing"

	internalregistry "github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

// TestRegisterModelsForAuthUsesLatestRuntimeSnapshot reproduces the startup race:
// a registration task holding an older clone (taken before the file watcher applied
// per-account excluded_models) runs after the manager already holds the newer auth.
// The registry must reflect the manager's current state, not the stale clone.
func TestRegisterModelsForAuthUsesLatestRuntimeSnapshot(t *testing.T) {
	reg := internalregistry.GetGlobalRegistry()
	authID := "codex-latest-snapshot-auth"
	reg.UnregisterClient(authID)
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})

	teamModels := internalregistry.GetCodexTeamModels()
	if len(teamModels) < 2 {
		t.Fatal("expected at least two Codex Team default models")
	}
	excludedModelID := teamModels[0].ID

	manager := coreauth.NewManager(nil, nil, nil)
	stale := &coreauth.Auth{
		ID:       authID,
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"plan_type": "team",
			"path":      "/path/to/codex-latest-snapshot.json",
		},
	}
	if _, errRegister := manager.Register(context.Background(), stale); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	staleClone := stale.Clone()

	latest := stale.Clone()
	latest.Attributes["excluded_models"] = excludedModelID
	if _, errUpdate := manager.Update(context.Background(), latest); errUpdate != nil {
		t.Fatalf("update auth: %v", errUpdate)
	}

	service := &Service{
		cfg:         &config.Config{},
		coreManager: manager,
	}
	service.registerModelsForAuth(context.Background(), staleClone)

	registered := codexModelIDSet(reg.GetModelsForClient(authID))
	if len(registered) == 0 {
		t.Fatal("expected models to be registered")
	}
	if _, found := registered[excludedModelID]; found {
		t.Fatalf("stale snapshot overrode newer excluded_models: %s is still registered", excludedModelID)
	}
}

// TestShouldSkipModelRegistrationIgnoresNewerGeneration ensures that a generation bump
// caused by an unrelated update (token refresh, request result) does not cancel a pending
// registration, because nothing else would re-register the credential's models.
func TestShouldSkipModelRegistrationIgnoresNewerGeneration(t *testing.T) {
	authID := "codex-newer-generation-auth"
	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{
		ID:       authID,
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"plan_type": "team",
			"path":      "/path/to/codex-newer-generation.json",
		},
	}
	registered, errRegister := manager.Register(context.Background(), auth)
	if errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	expectedGeneration := registered.Generation

	bumped := registered.Clone()
	bumped.Metadata = map[string]any{"access_token": "rotated"}
	if _, errUpdate := manager.Update(context.Background(), bumped); errUpdate != nil {
		t.Fatalf("update auth: %v", errUpdate)
	}
	current, ok := manager.GetByID(authID)
	if !ok || current.Generation <= expectedGeneration {
		t.Fatalf("expected generation to advance beyond %d, got %d", expectedGeneration, current.Generation)
	}

	service := &Service{
		cfg:         &config.Config{},
		coreManager: manager,
	}
	if service.shouldSkipModelRegistration(authID, expectedGeneration, false) {
		t.Fatal("registration must not be skipped when only the generation advanced")
	}
	if !service.shouldSkipModelRegistration(authID, expectedGeneration, true) {
		t.Fatal("registration must be skipped when the disabled state no longer matches")
	}
}
