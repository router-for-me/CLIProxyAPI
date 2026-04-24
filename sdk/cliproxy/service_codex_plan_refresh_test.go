package cliproxy

import (
	"context"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func TestServiceAuthHookRefreshesCodexModelsAfterPlanChange(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	service := &Service{
		cfg:         &config.Config{},
		coreManager: manager,
	}
	manager.SetHook(serviceAuthHook{service: service, next: manager.Hook()})

	auth := &coreauth.Auth{
		ID:       "codex-plan-refresh-test",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"plan_type": "free",
		},
		Metadata: map[string]any{"type": "codex"},
	}

	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	if hasRegisteredModel(auth.ID, "gpt-5.5") {
		t.Fatal("expected free codex auth to exclude gpt-5.5")
	}

	auth.Attributes["plan_type"] = "plus"
	if _, err := manager.Update(context.Background(), auth); err != nil {
		t.Fatalf("update auth: %v", err)
	}
	if !hasRegisteredModel(auth.ID, "gpt-5.5") {
		t.Fatal("expected plus codex auth to include gpt-5.5 after auth update")
	}
}

func TestServiceAuthHookIgnoresTokenOnlyUpdates(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	service := &Service{
		cfg:         &config.Config{},
		coreManager: manager,
	}
	manager.SetHook(serviceAuthHook{service: service, next: manager.Hook()})

	auth := &coreauth.Auth{
		ID:       "codex-token-refresh-test",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"plan_type": "plus",
		},
		Metadata: map[string]any{"type": "codex", "access_token": "old-token"},
	}
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	state := &coreauth.ModelState{
		NextRetryAfter: time.Now().Add(time.Hour),
	}
	auth.ModelStates = map[string]*coreauth.ModelState{"gpt-5.5": state}
	auth.Metadata["access_token"] = "new-token"
	if _, err := manager.Update(context.Background(), auth); err != nil {
		t.Fatalf("update auth: %v", err)
	}

	updated, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatal("expected auth to remain registered")
	}
	updatedState := updated.ModelStates["gpt-5.5"]
	if updatedState == nil || updatedState.NextRetryAfter.IsZero() {
		t.Fatal("expected token-only update to preserve model cooldown state")
	}
	if !hasRegisteredModel(auth.ID, "gpt-5.5") {
		t.Fatal("expected plus codex model registration to remain available")
	}
}

func TestServiceAuthHookIgnoresInvalidIDTokenWhenPlanUnchanged(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	service := &Service{
		cfg:         &config.Config{},
		coreManager: manager,
	}
	manager.SetHook(serviceAuthHook{service: service, next: manager.Hook()})

	auth := &coreauth.Auth{
		ID:       "codex-invalid-id-token-test",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"plan_type": "plus",
		},
		Metadata: map[string]any{"type": "codex", "id_token": "valid-old-token"},
	}
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	state := &coreauth.ModelState{
		NextRetryAfter: time.Now().Add(time.Hour),
	}
	auth.ModelStates = map[string]*coreauth.ModelState{"gpt-5.5": state}
	auth.Metadata["id_token"] = "invalid-refreshed-token"
	if _, err := manager.Update(context.Background(), auth); err != nil {
		t.Fatalf("update auth: %v", err)
	}

	updated, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatal("expected auth to remain registered")
	}
	if got := updated.Attributes["plan_type"]; got != "plus" {
		t.Fatalf("expected plan_type to remain plus, got %q", got)
	}
	updatedState := updated.ModelStates["gpt-5.5"]
	if updatedState == nil || updatedState.NextRetryAfter.IsZero() {
		t.Fatal("expected invalid id_token update to preserve model cooldown state")
	}
}

func TestRegisterModelsForAuthSkipsStatusDisabledAuth(t *testing.T) {
	service := &Service{cfg: &config.Config{}}
	auth := &coreauth.Auth{
		ID:       "codex-status-disabled-test",
		Provider: "codex",
		Status:   coreauth.StatusDisabled,
		Attributes: map[string]string{
			"plan_type": "plus",
		},
	}
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	service.registerModelsForAuth(auth)
	if models := registry.GetGlobalRegistry().GetModelsForClient(auth.ID); len(models) != 0 {
		t.Fatalf("expected status-disabled auth to have no registered models, got %d", len(models))
	}
}

func hasRegisteredModel(authID, modelID string) bool {
	for _, model := range registry.GetGlobalRegistry().GetModelsForClient(authID) {
		if model != nil && model.ID == modelID {
			return true
		}
	}
	return false
}
