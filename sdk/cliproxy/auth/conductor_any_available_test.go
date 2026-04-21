package auth

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
)

func TestAnyAvailableAuthForModel(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:         "auth-ws",
		Provider:   "test-provider",
		Status:     StatusActive,
		Attributes: map[string]string{"websockets": "true"},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}

	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	found := manager.AnyAvailableAuthForModel([]string{"test-provider"}, "test-model", func(candidate *Auth) bool {
		return authWebsocketsEnabled(candidate)
	})
	if !found {
		t.Fatalf("expected to find available websocket-capable auth")
	}
}
