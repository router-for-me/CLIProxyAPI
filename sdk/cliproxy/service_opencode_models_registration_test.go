package cliproxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	internalregistry "github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// Exercise native configuration registration and the registry used by /v1/models.
func TestRegisterConfigAPIKeyAuthsOpenCodeModels(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			t.Errorf("unexpected catalog path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-5.6-luna","canonical_id":"openai/gpt-5.6-luna","api":{"responses":{}}}]}`))
	}))
	defer server.Close()

	cfg := &config.Config{
		OpenCodeKey: []config.OpenCodeKey{{
			APIKey:  "opencode-runtime-key",
			BaseURL: server.URL,
		}},
	}
	modelRegistry := internalregistry.GetGlobalRegistry()
	manager := coreauth.NewManager(nil, nil, nil)
	service := &Service{cfg: cfg, coreManager: manager}

	service.registerConfigAPIKeyAuths(context.Background(), cfg)

	auths := manager.List()
	if len(auths) != 1 {
		t.Fatalf("runtime auth count = %d, want 1 (opencode key present)", len(auths))
	}
	t.Cleanup(func() { modelRegistry.UnregisterClient(auths[0].ID) })

	models := modelRegistry.GetModelsForClient(auths[0].ID)
	if len(models) != 1 || models[0].ID != "gpt-5.6-luna" ||
		models[0].CatalogProviderID != "openai" || models[0].CatalogModelID != "gpt-5.6-luna" {
		t.Fatalf("expected the registered catalog identity, got %+v", models)
	}

	available := modelRegistry.GetAvailableModelInfos()
	foundOpenCode := false
	for _, m := range available {
		if m != nil && m.OwnedBy == "opencode" {
			foundOpenCode = true
			break
		}
	}
	if !foundOpenCode {
		t.Fatalf("OpenCode models registered for client %s (count=%d) but absent from GetAvailableModelInfos() source used by /v1/models", auths[0].ID, len(models))
	}
}
