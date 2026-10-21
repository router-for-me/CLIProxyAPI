package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestConfigV8OfficialClientOnlyRoundTrip(t *testing.T) {
	gin.SetMode(gin.TestMode)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("config-version: 8\nserver:\n  port: 8317\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{cfg: cfg, configFilePath: path}
	reloads := make(chan *config.Config, 8)
	h.SetConfigReloadHook(func(_ context.Context, cfg *config.Config) { reloads <- cfg })
	router := gin.New()
	router.GET("/v8/management/config/*path", h.ConfigV8)
	router.PATCH("/v8/management/config", h.ConfigV8)
	router.PUT("/v8/management/config/*path", h.ConfigV8)

	request := func(method, url, body string, status int) string {
		t.Helper()
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(method, url, strings.NewReader(body)))
		if recorder.Code != status {
			t.Fatalf("%s %s: status=%d body=%s", method, url, recorder.Code, recorder.Body.String())
		}
		return recorder.Body.String()
	}

	request(http.MethodPatch, "/v8/management/config", `{"oauth":{"official-client-only":{"claude":true,"Codex":false}}}`, http.StatusOK)
	saved, _ := os.ReadFile(path)
	if !strings.Contains(string(saved), "official-client-only") {
		t.Fatalf("setting was not written to config.yaml:\n%s", saved)
	}
	select {
	case reloaded := <-reloads:
		if !reloaded.OAuthOfficialClientOnly["claude"] || reloaded.OAuthOfficialClientOnly["codex"] {
			t.Fatalf("reloaded config = %v", reloaded.OAuthOfficialClientOnly)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("config reload hook was not called")
	}

	request(http.MethodPut, "/v8/management/config/oauth/official-client-only/codex", `true`, http.StatusOK)
	if got := request(http.MethodGet, "/v8/management/config/oauth/official-client-only", "", http.StatusOK); !strings.Contains(got, `"codex":true`) || !strings.Contains(got, `"claude":true`) {
		t.Fatalf("GET = %s", got)
	}

	// Unrelated edits must not drop the setting.
	request(http.MethodPatch, "/v8/management/config", `{"routing":{"retry":{"request-retry":1}}}`, http.StatusOK)
	reloaded, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.OAuthOfficialClientOnly["claude"] || !reloaded.OAuthOfficialClientOnly["codex"] {
		t.Fatalf("setting lost after unrelated edit: %v", reloaded.OAuthOfficialClientOnly)
	}
}
