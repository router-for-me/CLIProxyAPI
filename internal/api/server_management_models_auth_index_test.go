package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// Only the v8 route accepts auth_index; /v0/management is frozen.
func TestManagementModelsAuthIndexOnlyOnV8Route(t *testing.T) {
	gin.SetMode(gin.TestMode)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("port: 8317\nremote-management: {secret-key: test-password}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	manager := coreauth.NewManager(nil, nil, nil)
	const authID = "route-models-config-key"
	if _, err := manager.Register(context.Background(), &coreauth.Auth{ID: authID, Index: "idx-route-key", Provider: "gemini", Status: coreauth.StatusActive}); err != nil {
		t.Fatal(err)
	}
	registry.GetGlobalRegistry().RegisterClient(authID, "gemini", []*registry.ModelInfo{{ID: "route-model"}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
	h := management.NewHandler(cfg, path, manager)
	h.SetLocalPassword("test-password")
	s := &Server{cfg: cfg, engine: gin.New(), mgmt: h}
	s.managementRoutesEnabled.Store(true)
	s.registerManagementRoutes()

	for _, tc := range []struct {
		url    string
		status int
		body   string
	}{
		{"/v8/management/credentials/models?auth_index=idx-route-key", http.StatusOK, `{"models":[{"id":"route-model"}]}`},
		{"/v0/management/auth-files/models?auth_index=idx-route-key", http.StatusBadRequest, `{"error":"name is required"}`},
	} {
		req := httptest.NewRequest(http.MethodGet, tc.url, nil)
		req.RemoteAddr = "127.0.0.1:1234"
		req.Header.Set("Authorization", "Bearer test-password")
		rec := httptest.NewRecorder()
		s.engine.ServeHTTP(rec, req)
		if rec.Code != tc.status || rec.Body.String() != tc.body {
			t.Errorf("%s: status=%d body=%s, want %d %s", tc.url, rec.Code, rec.Body.String(), tc.status, tc.body)
		}
	}
}
