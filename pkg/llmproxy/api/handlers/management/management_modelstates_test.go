package management

import (
	"context"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestRegisterAuthFromFilePreservesModelStates(t *testing.T) {
	authID := "iflow-user.json"
	manager := coreauth.NewManager(nil, nil, nil)
	existing := &coreauth.Auth{
		ID:       authID,
		Provider: "iflow",
		FileName: authID,
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"path": authID,
		},
		Metadata: map[string]any{
			"type":  "iflow",
			"email": "user@example.com",
		},
		CreatedAt: time.Now().Add(-time.Hour),
		ModelStates: map[string]*coreauth.ModelState{
			"iflow/deepseek-v3.1": {
				Unavailable: true,
			},
		},
	}
	if _, err := manager.Register(context.Background(), existing); err != nil {
		t.Fatalf("register existing auth: %v", err)
	}

	h := &Handler{
		cfg:         &config.Config{AuthDir: "."},
		authManager: manager,
	}

	payload := []byte(`{"type":"iflow","email":"user@example.com","access_token":"next"}`)
	if err := h.registerAuthFromFile(context.Background(), authID, payload); err != nil {
		t.Fatalf("registerAuthFromFile failed: %v", err)
	}

	updated, ok := manager.GetByID(authID)
	if !ok {
		t.Fatalf("updated auth not found")
	}
	if len(updated.ModelStates) != 1 {
		t.Fatalf("expected model states preserved, got %d", len(updated.ModelStates))
	}
	if _, ok = updated.ModelStates["iflow/deepseek-v3.1"]; !ok {
		t.Fatalf("expected specific model state to be preserved")
	}
}
