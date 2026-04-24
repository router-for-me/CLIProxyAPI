package cliproxy

import (
	"context"
	"testing"

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

func hasRegisteredModel(authID, modelID string) bool {
	for _, model := range registry.GetGlobalRegistry().GetModelsForClient(authID) {
		if model != nil && model.ID == modelID {
			return true
		}
	}
	return false
}
