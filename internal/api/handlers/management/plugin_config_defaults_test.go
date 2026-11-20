package management

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// Reading a plugin's config shows the effective enabled flag and the plugin's
// published defaults for omitted keys, keeps configured values, and leaves the
// file untouched.
func TestConfigV8PluginConfigShowsPublishedDefaults(t *testing.T) {
	gin.SetMode(gin.TestMode)
	path := filepath.Join(t.TempDir(), "config.yaml")
	raw := "config-version: 8\nplugins:\n  enabled: true\n  configs:\n    guard:\n      mode: rewrite\n"
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	host := pluginhost.New()
	host.RegisterPluginForTest("guard", pluginapi.Plugin{Metadata: pluginapi.Metadata{Name: "guard", ConfigFields: []pluginapi.ConfigField{
		{Name: "mode", Type: pluginapi.ConfigFieldTypeEnum, Default: "block"},
		{Name: "google_guard", Type: pluginapi.ConfigFieldTypeBoolean, Default: true},
		{Name: "scope", Type: pluginapi.ConfigFieldTypeArray, Default: []any{"system"}},
		{Name: "note", Type: pluginapi.ConfigFieldTypeString},
	}}})
	h := &Handler{cfg: cfg, configFilePath: path}
	h.SetPluginHost(host)
	router := gin.New()
	router.GET("/v8/management/config/*path", h.ConfigV8)

	get := func(url string) map[string]any {
		t.Helper()
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, url, nil))
		if recorder.Code != http.StatusOK {
			t.Fatalf("GET %s: status=%d body=%s", url, recorder.Code, recorder.Body.String())
		}
		var body map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
			t.Fatalf("GET %s: %v", url, err)
		}
		return body
	}

	got := get("/v8/management/config/plugins/configs/guard")
	if got["mode"] != "rewrite" || got["google_guard"] != true || got["enabled"] != false {
		t.Fatalf("plugin config = %#v, want configured mode, default google_guard, effective enabled", got)
	}
	if scope, ok := got["scope"].([]any); !ok || len(scope) != 1 || scope[0] != "system" {
		t.Fatalf("scope = %#v, want default [system]", got["scope"])
	}
	if _, ok := got["note"]; ok {
		t.Fatal("field without a published default was added")
	}
	if plugins := get("/v8/management/config/plugins"); plugins["configs"].(map[string]any)["guard"].(map[string]any)["google_guard"] != nil {
		t.Fatal("defaults leaked into a read of the parent path")
	}
	if saved, _ := os.ReadFile(path); string(saved) != raw {
		t.Fatalf("config file changed:\n%s", saved)
	}
}

// The bundled guard's entry is synthesized in memory; its read shows it as
// enabled, so a whole-object save from the UI writes it back enabled.
func TestConfigV8BundledGuardConfigShowsEffectiveEnabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("config-version: 8\nport: 8317\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	h := &Handler{cfg: cfg, configFilePath: path}
	router := gin.New()
	router.GET("/v8/management/config/*path", h.ConfigV8)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v8/management/config/plugins/configs/"+config.AntigravityGuardPluginID, nil))
	var got map[string]any
	if recorder.Code != http.StatusOK || json.Unmarshal(recorder.Body.Bytes(), &got) != nil {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if got["enabled"] != true || got["priority"] != float64(1) {
		t.Fatalf("bundled guard config = %#v, want enabled with priority 1", got)
	}
}
