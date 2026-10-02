package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestManagementV8RoutesShareAccessControl(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name                      string
		enabled, home, authorized bool
		want                      int
	}{
		{"authorized", true, false, true, 200},
		{"missing key", true, false, false, 401},
		{"disabled", false, false, true, 404},
		{"home", true, true, true, 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte("port: 8317\nremote-management: {secret-key: test-password}\n"), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.LoadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			cfg.Home.Enabled = tc.home
			cfg.Plugins.Dir = filepath.Dir(path)
			h := management.NewHandler(cfg, path, nil)
			h.SetLocalPassword("test-password")
			s := &Server{cfg: cfg, engine: gin.New(), mgmt: h}
			s.managementRoutesEnabled.Store(tc.enabled)
			s.registerManagementRoutes()
			for _, route := range []string{"/v0/management/config", "/v8/management/config", "/v0/management/plugins", "/v8/management/plugins"} {
				req := httptest.NewRequest(http.MethodGet, route, nil)
				req.RemoteAddr = "127.0.0.1:1234"
				if tc.authorized {
					req.Header.Set("Authorization", "Bearer test-password")
				}
				recorder := httptest.NewRecorder()
				s.engine.ServeHTTP(recorder, req)
				if recorder.Code != tc.want {
					t.Fatalf("%s: status=%d want=%d body=%s", route, recorder.Code, tc.want, recorder.Body.String())
				}
			}
		})
	}
}

func TestManagementV8IndependentContract(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	raw := "port: 8317\nrequest-retry: 3\ndebug: false\napi-keys: [client]\nremote-management: {secret-key: test-password}\n"
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.AuthDir = t.TempDir()
	h := management.NewHandler(cfg, path, nil)
	h.SetLocalPassword("test-password")
	s := &Server{cfg: cfg, engine: gin.New(), mgmt: h}
	s.managementRoutesEnabled.Store(true)
	s.registerManagementRoutes()
	baseline, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	routes := make(map[string]bool)
	for _, route := range s.engine.Routes() {
		routes[route.Method+" "+route.Path] = true
	}
	for _, route := range []string{
		"GET /v0/management/debug", "PUT /v0/management/request-retry", "GET /v0/management/auth-files",
		"GET /v8/management/observability/logs", "GET /v8/management/observability/usage/queue",
		"GET /v8/management/credentials", "POST /v8/management/credentials",
		"GET /v8/management/oauth/auth-url", "POST /v8/management/oauth/import", "POST /v8/management/oauth/callback",
		"POST /v8/management/routing/cooldown/reset",
		"GET /v8/management/plugins/:id/quota", "POST /v8/management/plugins/:id/quota", "DELETE /v8/management/plugins/:id/quota",
		"POST /v8/management/plugins/store/:id/install", "DELETE /v8/management/plugins/:id",
	} {
		if !routes[route] {
			t.Errorf("missing route %s", route)
		}
	}
	request := func(method, url, body string, status int) string {
		t.Helper()
		req := httptest.NewRequest(method, url, strings.NewReader(body))
		req.RemoteAddr = "127.0.0.1:1234"
		req.Header.Set("Authorization", "Bearer test-password")
		response := httptest.NewRecorder()
		s.engine.ServeHTTP(response, req)
		if response.Code != status {
			t.Fatalf("%s %s: status=%d body=%s", method, url, response.Code, response.Body.String())
		}
		return strings.TrimSpace(response.Body.String())
	}
	for _, legacy := range []string{"debug", "request-retry", "api-keys", "codex-api-key", "auth-files", "codex-auth-url", "oauth/providers/codex/auth-url", "plugins/test-plugin/config"} {
		request(http.MethodGet, "/v8/management/"+legacy, "", http.StatusNotFound)
	}
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/v8/management/credentials/quota/providers"},
		{http.MethodPost, "/v8/management/credentials/quota/fetch"},
		{http.MethodPost, "/v8/management/credentials/quota/reset"},
	} {
		if routes[route.method+" "+route.path] {
			t.Errorf("removed route still registered: %s %s", route.method, route.path)
		}
		request(route.method, route.path, "", http.StatusNotFound)
	}
	request(http.MethodPost, "/v8/management/oauth/providers/vertex/import", "", http.StatusNotFound)
	for _, tc := range []struct {
		method, url, body string
		status            int
	}{
		{http.MethodGet, "/v8/management/oauth/auth-url", `{"error":"provider is required"}`, http.StatusBadRequest},
		{http.MethodGet, "/v8/management/oauth/auth-url?provider=%20", `{"error":"provider is required"}`, http.StatusBadRequest},
		{http.MethodGet, "/v8/management/oauth/auth-url?provider=unknown", `{"error":"provider_not_found"}`, http.StatusNotFound},
		{http.MethodPost, "/v8/management/oauth/import", `{"error":"provider is required"}`, http.StatusBadRequest},
		{http.MethodPost, "/v8/management/oauth/import?provider=codex", `{"error":"provider_not_found"}`, http.StatusNotFound},
		{http.MethodPost, "/v8/management/oauth/import?provider=vertex", `{"error":"file required"}`, http.StatusBadRequest},
		{http.MethodPost, "/v0/management/vertex/import", `{"error":"file required"}`, http.StatusBadRequest},
	} {
		if got := request(tc.method, tc.url, "", tc.status); got != tc.body {
			t.Fatalf("%s: body=%s, want %s", tc.url, got, tc.body)
		}
	}
	for _, tc := range []struct{ url, provider string }{
		{"/v8/management/oauth/auth-url?provider=codex", "codex"},
		{"/v8/management/oauth/auth-url?provider=%20CLAUDE%20", "anthropic"},
		{"/v0/management/codex-auth-url", "codex"},
		{"/v0/management/anthropic-auth-url", "anthropic"},
	} {
		body := request(http.MethodGet, tc.url, "", http.StatusOK)
		var login struct{ URL, State string }
		if errDecode := json.Unmarshal([]byte(body), &login); errDecode != nil {
			t.Fatal(errDecode)
		}
		t.Cleanup(func() { management.CancelOAuthSession(login.State) })
		if login.URL == "" || !management.IsOAuthSessionPending(login.State, tc.provider) {
			t.Fatalf("%s: invalid login response %s", tc.url, body)
		}
		if got := request(http.MethodGet, "/v8/management/oauth/status?state="+login.State, "", http.StatusOK); got != `{"status":"wait"}` {
			t.Fatalf("pending login status=%s", got)
		}
		request(http.MethodDelete, "/v8/management/oauth/session?state="+login.State, "", http.StatusOK)
		if management.IsOAuthSessionPending(login.State, tc.provider) {
			t.Fatal("login session was not cancelled")
		}
	}
	if got := request(http.MethodGet, "/v8/management/config/routing/retry/request-retry", "", http.StatusOK); got != "3" {
		t.Fatalf("nested value = %s, want 3", got)
	}
	request(http.MethodPatch, "/v8/management/config", `{"request-retry":5}`, http.StatusBadRequest)
	request(http.MethodPut, "/v8/management/config/routing/retry/request-retry", `{"value":5}`, http.StatusUnprocessableEntity)
	unchanged, err := os.ReadFile(path)
	if err != nil || string(unchanged) != string(baseline) {
		t.Fatalf("reads or rejected writes migrated the file: %v", err)
	}
	request(http.MethodPut, "/v8/management/config/routing/retry/request-retry", `0`, http.StatusOK)
	request(http.MethodPut, "/v0/management/request-retry", `{"value":2}`, http.StatusOK)
	if got := request(http.MethodGet, "/v8/management/config/routing/retry/request-retry", "", http.StatusOK); got != "2" {
		t.Fatalf("legacy write did not update the migrated field: %s", got)
	}
}

func TestManagementV8PluginOperationMigratesConfiguration(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	raw := fmt.Sprintf("codex: {disable-codex-cloaking: true}\nremote-management: {secret-key: test-password}\nplugins:\n  dir: %q\n  configs: {test-plugin: {enabled: false}}\n", filepath.ToSlash(dir))
	if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	h := management.NewHandler(cfg, path, nil)
	h.SetLocalPassword("test-password")
	reloads := make(chan *config.Config, 1)
	h.SetConfigReloadHook(func(_ context.Context, next *config.Config) { reloads <- next })
	s := &Server{cfg: cfg, engine: gin.New(), mgmt: h}
	s.managementRoutesEnabled.Store(true)
	s.registerManagementRoutes()
	req := httptest.NewRequest(http.MethodDelete, "/v8/management/plugins/test-plugin", nil)
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("Authorization", "Bearer test-password")
	response := httptest.NewRecorder()
	s.engine.ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	saved, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(saved), "config-version: 8") {
		t.Fatalf("plugin operation did not migrate the config: %v", err)
	}
	select {
	case next := <-reloads:
		if next.ForAPIKey().Codex.DisableCodexCloaking || !next.Codex.DisableCodexCloaking {
			t.Fatal("plugin operation published an incorrect configuration scope")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("missing configuration reload")
	}
}
