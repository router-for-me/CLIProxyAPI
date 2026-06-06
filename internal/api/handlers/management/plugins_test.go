package management

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"gopkg.in/yaml.v3"
)

func TestListPluginsIncludesScannedAndConfiguredPlugins(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	pluginsDir := writeManagementPluginFile(t, "scanned")
	disabled := false
	h := &Handler{
		cfg: &config.Config{
			Plugins: config.PluginsConfig{
				Enabled: false,
				Dir:     pluginsDir,
				Configs: map[string]config.PluginInstanceConfig{
					"configured-only": {Enabled: &disabled},
				},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/plugins", nil)

	h.ListPlugins(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var body struct {
		PluginsEnabled bool `json:"plugins_enabled"`
		Plugins        []struct {
			ID               string `json:"id"`
			Path             string `json:"path"`
			Configured       bool   `json:"configured"`
			Registered       bool   `json:"registered"`
			Enabled          bool   `json:"enabled"`
			EffectiveEnabled bool   `json:"effective_enabled"`
			SupportsOAuth    bool   `json:"supports_oauth"`
			Logo             string `json:"logo"`
			ConfigFields     []any  `json:"config_fields"`
			Menus            []any  `json:"menus"`
		} `json:"plugins"`
	}
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &body); errDecode != nil {
		t.Fatalf("decode response: %v; body=%s", errDecode, rec.Body.String())
	}
	if body.PluginsEnabled {
		t.Fatal("plugins_enabled = true, want false")
	}
	entries := map[string]struct {
		Configured       bool
		Registered       bool
		Enabled          bool
		EffectiveEnabled bool
		Path             string
	}{}
	for _, item := range body.Plugins {
		entries[item.ID] = struct {
			Configured       bool
			Registered       bool
			Enabled          bool
			EffectiveEnabled bool
			Path             string
		}{
			Configured:       item.Configured,
			Registered:       item.Registered,
			Enabled:          item.Enabled,
			EffectiveEnabled: item.EffectiveEnabled,
			Path:             item.Path,
		}
		if item.Registered || item.SupportsOAuth || item.Logo != "" || len(item.ConfigFields) != 0 || len(item.Menus) != 0 {
			t.Fatalf("unregistered plugin entry has runtime fields: %#v", item)
		}
	}
	if got, ok := entries["scanned"]; !ok || got.Configured || !got.Enabled || got.EffectiveEnabled || got.Path == "" {
		t.Fatalf("scanned entry = %#v, exists=%v", got, ok)
	}
	if got, ok := entries["configured-only"]; !ok || !got.Configured || got.Enabled || got.EffectiveEnabled || got.Path != "" {
		t.Fatalf("configured-only entry = %#v, exists=%v", got, ok)
	}
}

func TestPatchPluginEnabledUpdatesOnlyPluginConfig(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg: &config.Config{
			Plugins: config.PluginsConfig{
				Enabled: false,
				Configs: map[string]config.PluginInstanceConfig{
					"sample": pluginConfigFromYAML(t, "enabled: false\npriority: 2\nmode: safe\n"),
				},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Params = gin.Params{{Key: "id", Value: "sample"}}
	c.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/plugins/sample/enabled", strings.NewReader(`{"enabled":true}`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PatchPluginEnabled(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if h.cfg.Plugins.Enabled {
		t.Fatal("global Plugins.Enabled changed to true")
	}
	item := h.cfg.Plugins.Configs["sample"]
	if item.Enabled == nil || !*item.Enabled {
		t.Fatalf("sample enabled = %#v, want true", item.Enabled)
	}
	raw := marshalPluginRaw(t, item)
	if !strings.Contains(raw, "mode: safe") {
		t.Fatalf("raw config lost custom field:\n%s", raw)
	}
}

func TestPutPluginConfigReplacesPluginConfig(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg: &config.Config{
			Plugins: config.PluginsConfig{
				Configs: map[string]config.PluginInstanceConfig{
					"sample": pluginConfigFromYAML(t, "enabled: false\nmode: safe\nold: true\n"),
				},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Params = gin.Params{{Key: "id", Value: "sample"}}
	c.Request = httptest.NewRequest(http.MethodPut, "/v0/management/plugins/sample/config", bytes.NewBufferString(`{"enabled":true,"priority":7,"mode":"fast"}`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PutPluginConfig(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	item := h.cfg.Plugins.Configs["sample"]
	if item.Enabled == nil || !*item.Enabled || item.Priority != 7 {
		t.Fatalf("plugin host fields = enabled %#v priority %d, want true priority 7", item.Enabled, item.Priority)
	}
	raw := marshalPluginRaw(t, item)
	if !strings.Contains(raw, "mode: fast") || strings.Contains(raw, "old:") {
		t.Fatalf("raw config =\n%s", raw)
	}
}

func TestPatchPluginConfigMergesAndDeletesFields(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg: &config.Config{
			Plugins: config.PluginsConfig{
				Configs: map[string]config.PluginInstanceConfig{
					"sample": pluginConfigFromYAML(t, "enabled: false\npriority: 3\nmode: safe\nremove: yes\n"),
				},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Params = gin.Params{{Key: "id", Value: "sample"}}
	c.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/plugins/sample/config", strings.NewReader(`{"mode":"fast","remove":null,"count":3}`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PatchPluginConfig(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	item := h.cfg.Plugins.Configs["sample"]
	if item.Enabled == nil || *item.Enabled || item.Priority != 3 {
		t.Fatalf("plugin host fields = enabled %#v priority %d, want false priority 3", item.Enabled, item.Priority)
	}
	raw := marshalPluginRaw(t, item)
	if !strings.Contains(raw, "mode: fast") || !strings.Contains(raw, "count: 3") || strings.Contains(raw, "remove:") {
		t.Fatalf("raw config =\n%s", raw)
	}
}

func writeManagementPluginFile(t *testing.T, id string) string {
	t.Helper()
	root := t.TempDir()
	archDir := filepath.Join(root, runtime.GOOS, runtime.GOARCH)
	if errMkdirAll := os.MkdirAll(archDir, 0o755); errMkdirAll != nil {
		t.Fatalf("MkdirAll() error = %v", errMkdirAll)
	}
	path := filepath.Join(archDir, id+managementPluginExtension(runtime.GOOS))
	if errWriteFile := os.WriteFile(path, []byte("x"), 0o644); errWriteFile != nil {
		t.Fatalf("WriteFile(%s) error = %v", path, errWriteFile)
	}
	return root
}

func managementPluginExtension(goos string) string {
	switch goos {
	case "darwin":
		return ".dylib"
	case "windows":
		return ".dll"
	default:
		return ".so"
	}
}

func pluginConfigFromYAML(t *testing.T, text string) config.PluginInstanceConfig {
	t.Helper()
	var item config.PluginInstanceConfig
	if errUnmarshal := yaml.Unmarshal([]byte(text), &item); errUnmarshal != nil {
		t.Fatalf("unmarshal plugin config: %v", errUnmarshal)
	}
	return item
}

func marshalPluginRaw(t *testing.T, item config.PluginInstanceConfig) string {
	t.Helper()
	data, errMarshal := yaml.Marshal(&item.Raw)
	if errMarshal != nil {
		t.Fatalf("marshal plugin raw: %v", errMarshal)
	}
	return string(data)
}
