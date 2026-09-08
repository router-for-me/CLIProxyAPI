package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/home"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestCodexNativeModelsReturnsCatalog(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	clientID := "test-codex-native-models"
	modelRegistry.RegisterClient(clientID, "codex", []*registry.ModelInfo{{
		ID: "gpt-5.6-sol", Object: "model", OwnedBy: "openai", Type: "codex",
	}})
	t.Cleanup(func() { modelRegistry.UnregisterClient(clientID) })
	server := newTestServer(t)

	catalog := func(t *testing.T, path, userAgent, anthropicVersion string) map[string]json.RawMessage {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Authorization", "Bearer test-key")
		req.Header.Set("User-Agent", userAgent)
		req.Header.Set("Anthropic-Version", anthropicVersion)
		recorder := httptest.NewRecorder()
		server.engine.ServeHTTP(recorder, req)
		if recorder.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200; body=%s", recorder.Code, recorder.Body.String())
		}
		var response map[string]json.RawMessage
		if errUnmarshal := json.Unmarshal(recorder.Body.Bytes(), &response); errUnmarshal != nil {
			t.Fatalf("decode catalog: %v", errUnmarshal)
		}
		if response["models"] == nil || response["data"] != nil || response["object"] != nil {
			t.Fatalf("expected Codex catalog format, got %s", recorder.Body.String())
		}
		if !strings.Contains(string(response["models"]), `"slug":"gpt-5.6-sol"`) {
			t.Fatalf("expected registered model in catalog: %s", response["models"])
		}
		return response
	}

	for _, version := range []string{"", "0.137.0", "0.153.4"} {
		t.Run("version="+version, func(t *testing.T) {
			want := catalog(t, "/v1/models?client_version="+version, "", "")
			path := "/backend-api/codex/models"
			if version != "" {
				path += "?client_version=" + version
			}
			for _, tc := range []struct {
				name, userAgent, anthropicVersion string
			}{
				{name: "no-user-agent"},
				{name: "codex", userAgent: "codex_cli_rs/0.153.4"},
				{name: "browser", userAgent: "Mozilla/5.0"},
				{name: "claude", userAgent: "claude-cli/1.0"},
				{name: "grok", userAgent: "grok-shell/0.2.119"},
				{name: "anthropic", anthropicVersion: "2023-06-01"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					got := catalog(t, path, tc.userAgent, tc.anthropicVersion)
					if !reflect.DeepEqual(got, want) {
						t.Fatalf("native catalog differs from /v1/models for client version %q", version)
					}
				})
			}
		})
	}
}

func TestCodexNativeModelsRequiresAuthentication(t *testing.T) {
	server := newTestServer(t)
	for _, authorization := range []string{"", "Bearer invalid-key"} {
		t.Run(authorization, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/backend-api/codex/models", nil)
			req.Header.Set("Authorization", authorization)
			recorder := httptest.NewRecorder()
			server.engine.ServeHTTP(recorder, req)
			if recorder.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401; body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestCodexNativeModelsPrefersHome(t *testing.T) {
	previousHome := home.Current()
	home.ClearCurrent()
	t.Cleanup(func() { home.SetCurrent(previousHome) })
	server := newTestServer(t)
	server.cfg.Home.Enabled = true

	for _, route := range server.engine.Routes() {
		if route.Method != http.MethodGet || route.Path != "/backend-api/codex/models" {
			continue
		}
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodGet, route.Path, nil)
		// Exercise the registered handler without the Home heartbeat middleware.
		route.HandlerFunc(ctx)
		if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "home control center unavailable") {
			t.Fatalf("expected Home error instead of local catalog; status=%d body=%s", recorder.Code, recorder.Body.String())
		}
		return
	}
	t.Fatal("native models route is not registered")
}
