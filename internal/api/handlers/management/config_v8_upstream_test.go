package management

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestConfigV8SharedUpstreamRoundTrip(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.yaml")
	raw := "oauth: {providers: {codex: {stream-bootstrap-buffering: true, header-defaults: {user-agent: oauth-agent}}}}\n"
	if errWrite := os.WriteFile(file, []byte(raw), 0600); errWrite != nil {
		t.Fatal(errWrite)
	}
	cfg, errLoad := config.LoadConfig(file)
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	h := &Handler{cfg: cfg, configFilePath: file}
	router := gin.New()
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		router.Handle(method, "/v8/management/config/*path", h.ConfigV8)
	}
	request := func(method, path, body string, want int) string {
		t.Helper()
		response := httptest.NewRecorder()
		router.ServeHTTP(response, httptest.NewRequest(method, "/v8/management/config/"+path, strings.NewReader(body)))
		if response.Code != want {
			t.Fatalf("%s %s: status %d, want %d: %s", method, path, response.Code, want, response.Body.String())
		}
		return response.Body.String()
	}
	if got := request(http.MethodGet, "upstream/codex/stream-bootstrap-buffering", "", http.StatusOK); got != "true" {
		t.Fatalf("historical shared field was not exposed at upstream: %s", got)
	}
	if data, errRead := os.ReadFile(file); errRead != nil || string(data) != raw {
		t.Fatal("GET rewrote the historical document")
	}
	request(http.MethodPut, "upstream/codex/stream-bootstrap-buffering", "false", http.StatusOK)
	request(http.MethodPut, "oauth/auth-auto-refresh-workers", "3", http.StatusOK)
	request(http.MethodPatch, "upstream/claude", `{"header-defaults":{"timezone":"Asia/Singapore","stabilize-device-profile":false}}`, http.StatusOK)
	loaded, errLoad := config.LoadConfig(file)
	if errLoad != nil {
		t.Fatal(errLoad)
	}
	if loaded.Codex.StreamBootstrapBuffering || loaded.AuthAutoRefreshWorkers != 3 || loaded.ClaudeHeaderDefaults.Timezone != "Asia/Singapore" || loaded.ForAPIKey().ClaudeHeaderDefaults.Timezone != "Asia/Singapore" {
		t.Fatal("management write lost shared upstream values")
	}
	if loaded.CodexHeaderDefaults.UserAgent != "oauth-agent" || loaded.ForAPIKey().CodexHeaderDefaults.UserAgent != "" {
		t.Fatal("management write changed OAuth-only header scope")
	}
	before, errRead := os.ReadFile(file)
	if errRead != nil {
		t.Fatal(errRead)
	}
	request(http.MethodPut, "oauth/providers/codex/stream-bootstrap-buffering", `"invalid"`, http.StatusUnprocessableEntity)
	if after, errRead := os.ReadFile(file); errRead != nil || string(after) != string(before) {
		t.Fatal("rejected historical-path write changed the document")
	}
	request(http.MethodDelete, "upstream/claude", "", http.StatusOK)
	if got := request(http.MethodGet, "upstream/claude", "", http.StatusNotFound); got == "" {
		t.Fatal("missing subtree response was empty")
	}
	loaded, errLoad = config.LoadConfig(file)
	if errLoad != nil || loaded.ClaudeHeaderDefaults.Timezone != "" {
		t.Fatalf("deleted upstream fields did not return to defaults: %v", errLoad)
	}
}
