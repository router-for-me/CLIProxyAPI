package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestResetHintsV8AuthenticatedRouteSelectsSoonestAccount(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "test-management-key")
	selector := &coreauth.SoonestResetFirstSelector{}
	manager := coreauth.NewManager(nil, selector, nil)
	auths := []*coreauth.Auth{{ID: "a", Provider: "codex", Attributes: map[string]string{"weight": "20"}}, {ID: "z", Provider: "codex", Attributes: map[string]string{"weight": "1"}}}
	for _, auth := range auths {
		if _, err := manager.Register(coreauth.WithSkipPersist(context.Background()), auth); err != nil {
			t.Fatal(err)
		}
	}
	cfg := &config.Config{}
	handler := management.NewHandlerWithoutConfigFilePath(cfg, manager)
	handler.SetLocalPassword("test-management-key")
	server := &Server{cfg: cfg, engine: gin.New(), mgmt: handler}
	server.managementRoutesEnabled.Store(true)
	server.registerManagementRoutes()
	now := time.Now().UTC().Add(-time.Second)
	body := fmt.Sprintf(`{"accounts":[{"auth_index":%q,"observed_at":%q,"reset_at":%q}]}`, auths[1].EnsureIndex(), now.Format(time.RFC3339Nano), now.Add(time.Hour).Format(time.RFC3339Nano))
	for _, tc := range []struct {
		name                      string
		authorized, enabled, home bool
		want                      int
	}{
		{"missing key", false, true, false, 401},
		{"disabled", true, false, false, 404},
		{"home", true, true, true, 404},
		{"authorized", true, true, false, 200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server.managementRoutesEnabled.Store(tc.enabled)
			cfg.Home.Enabled = tc.home
			req := httptest.NewRequest(http.MethodPut, "/v8/management/routing/reset-hints", strings.NewReader(body))
			req.RemoteAddr = "127.0.0.1:4321"
			if tc.authorized {
				req.Header.Set("Authorization", "Bearer test-management-key")
			}
			rec := httptest.NewRecorder()
			server.engine.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
	got, err := selector.Pick(context.Background(), "codex", "model", cliproxyexecutor.Options{}, auths)
	if err != nil || got == nil || got.ID != "z" {
		t.Fatalf("selected = %v, %v", got, err)
	}
	// The new operational route must not appear in the deprecated v0 API.
	req := httptest.NewRequest(http.MethodPut, "/v0/management/routing/reset-hints", strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:4321"
	req.Header.Set("Authorization", "Bearer test-management-key")
	rec := httptest.NewRecorder()
	server.engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("legacy status = %d", rec.Code)
	}
}
