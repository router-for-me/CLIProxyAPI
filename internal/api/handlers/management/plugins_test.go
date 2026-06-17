package management

import (
	"bytes"
	"context"
	"encoding/json"
	"html"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/pluginapi"
	"gopkg.in/yaml.v3"
)

func waitForAsyncReload(t *testing.T, reloads <-chan *config.Config) *config.Config {
	t.Helper()
	select {
	case cfg := <-reloads:
		return cfg
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for async config reload")
		return nil
	}
}

func waitForReloadDone(t *testing.T, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for config reload hook to finish")
	}
}

func captureConfigReload(h *Handler) (<-chan *config.Config, <-chan struct{}) {
	reloads := make(chan *config.Config, 1)
	done := make(chan struct{})
	h.SetConfigReloadHook(func(_ context.Context, cfg *config.Config) {
		defer close(done)
		reloads <- cfg
	})
	return reloads, done
}

func TestConfigReloadGenerationSkipsOlderSnapshot(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			Plugins: config.PluginsConfig{
				Configs: map[string]config.PluginInstanceConfig{
					"sample": pluginConfigFromYAML(t, "enabled: true\nmode: old\n"),
				},
			},
		},
	}
	reloadedModes := make([]string, 0, 1)
	h.SetConfigReloadHook(func(_ context.Context, cfg *config.Config) {
		reloadedModes = append(reloadedModes, pluginRawScalarValue(t, cfg.Plugins.Configs["sample"], "mode"))
	})

	h.mu.Lock()
	older := h.reloadSnapshotConfigLocked()
	item := h.cfg.Plugins.Configs["sample"]
	setPluginRawScalarValue(t, &item.Raw, "mode", "new")
	h.cfg.Plugins.Configs["sample"] = item
	newer := h.reloadSnapshotConfigLocked()
	h.mu.Unlock()

	h.reloadConfigAfterManagementSave(context.Background(), newer)
	h.reloadConfigAfterManagementSave(context.Background(), older)

	if len(reloadedModes) != 1 || reloadedModes[0] != "new" {
		t.Fatalf("reloaded modes = %#v, want only new snapshot", reloadedModes)
	}
}

func TestListPluginsIncludesScannedAndConfiguredPlugins(t *testing.T) {
	t.Parallel()

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
	if got, ok := entries["scanned"]; !ok || got.Configured || got.Enabled || got.EffectiveEnabled || got.Path == "" {
		t.Fatalf("scanned entry = %#v, exists=%v", got, ok)
	}
	if got, ok := entries["configured-only"]; !ok || !got.Configured || got.Enabled || got.EffectiveEnabled || got.Path != "" {
		t.Fatalf("configured-only entry = %#v, exists=%v", got, ok)
	}
}

func TestGetPluginConfigReturnsPreservedRawConfig(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			Plugins: config.PluginsConfig{
				Configs: map[string]config.PluginInstanceConfig{
					"sample": pluginConfigFromYAML(t, `
enabled: false
priority: 7
mode: safe
allowed_models:
  - gemini-2.5-pro
  - claude-sonnet-4
options:
  retries: 2
  strict: true
`),
				},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Params = gin.Params{{Key: "id", Value: "sample"}}
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/plugins/sample/config", nil)

	h.GetPluginConfig(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var body struct {
		Enabled       bool           `json:"enabled"`
		Priority      int            `json:"priority"`
		Mode          string         `json:"mode"`
		AllowedModels []string       `json:"allowed_models"`
		Options       map[string]any `json:"options"`
	}
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &body); errDecode != nil {
		t.Fatalf("decode response: %v; body=%s", errDecode, rec.Body.String())
	}
	if body.Enabled || body.Priority != 7 || body.Mode != "safe" {
		t.Fatalf("base fields = enabled %v priority %d mode %q, want false 7 safe", body.Enabled, body.Priority, body.Mode)
	}
	if len(body.AllowedModels) != 2 || body.AllowedModels[0] != "gemini-2.5-pro" || body.AllowedModels[1] != "claude-sonnet-4" {
		t.Fatalf("allowed_models = %#v", body.AllowedModels)
	}
	if body.Options["retries"] != float64(2) || body.Options["strict"] != true {
		t.Fatalf("options = %#v", body.Options)
	}
}

func TestGetPluginConfigReturnsEmptyObjectForKnownUnconfiguredPlugin(t *testing.T) {
	t.Parallel()

	pluginsDir := writeManagementPluginFile(t, "scanned")
	h := &Handler{
		cfg: &config.Config{
			Plugins: config.PluginsConfig{
				Dir: pluginsDir,
			},
		},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Params = gin.Params{{Key: "id", Value: "scanned"}}
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/plugins/scanned/config", nil)

	h.GetPluginConfig(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var body map[string]any
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &body); errDecode != nil {
		t.Fatalf("decode response: %v; body=%s", errDecode, rec.Body.String())
	}
	if len(body) != 0 {
		t.Fatalf("body = %#v, want empty object", body)
	}
}

func TestGetPluginConfigReturnsNotFoundForUnknownPlugin(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg:            &config.Config{},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Params = gin.Params{{Key: "id", Value: "missing"}}
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/plugins/missing/config", nil)

	h.GetPluginConfig(c)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

func TestPatchPluginEnabledUpdatesOnlyPluginConfig(t *testing.T) {
	t.Parallel()

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
	reloads, reloadDone := captureConfigReload(h)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Params = gin.Params{{Key: "id", Value: "sample"}}
	c.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/plugins/sample/enabled", strings.NewReader(`{"enabled":true}`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PatchPluginEnabled(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	cfgSnapshot := waitForAsyncReload(t, reloads)
	waitForReloadDone(t, reloadDone)
	if cfgSnapshot == h.cfg {
		t.Fatalf("reload config = handler config %p, want independent snapshot", h.cfg)
	}
	if cfgSnapshot.Plugins.Enabled {
		t.Fatal("snapshot global Plugins.Enabled changed to true")
	}
	snapshotItem := cfgSnapshot.Plugins.Configs["sample"]
	if snapshotItem.Enabled == nil || !*snapshotItem.Enabled {
		t.Fatalf("snapshot sample enabled = %#v, want true", snapshotItem.Enabled)
	}
	if raw := marshalPluginRaw(t, snapshotItem); !strings.Contains(raw, "mode: safe") {
		t.Fatalf("snapshot raw config lost custom field:\n%s", raw)
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

func TestPatchPluginEnabledReloadSnapshotRawImmutability(t *testing.T) {
	t.Parallel()
	h := &Handler{
		cfg: &config.Config{
			Plugins: config.PluginsConfig{
				Configs: map[string]config.PluginInstanceConfig{
					"sample": pluginConfigFromYAML(t, "enabled: false\nmode: first\n"),
				},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}
	reloads := make(chan *config.Config, 1)
	releaseReload := make(chan struct{})
	reloadDone := make(chan struct{})
	h.SetConfigReloadHook(func(_ context.Context, cfg *config.Config) {
		defer close(reloadDone)
		reloads <- cfg
		<-releaseReload
	})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Params = gin.Params{{Key: "id", Value: "sample"}}
	c.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/plugins/sample/enabled", strings.NewReader(`{"enabled":true}`))
	c.Request.Header.Set("Content-Type", "application/json")

	h.PatchPluginEnabled(c)

	if rec.Code != http.StatusOK {
		close(releaseReload)
		waitForReloadDone(t, reloadDone)
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	cfgSnapshot := waitForAsyncReload(t, reloads)

	h.mu.Lock()
	item := h.cfg.Plugins.Configs["sample"]
	setPluginRawScalarValue(t, &item.Raw, "mode", "second")
	h.cfg.Plugins.Configs["sample"] = item
	h.mu.Unlock()

	if cfgSnapshot == h.cfg {
		t.Fatalf("reload config = handler config %p, want independent snapshot", h.cfg)
	}
	snapshotItem := cfgSnapshot.Plugins.Configs["sample"]
	if snapshotItem.Enabled == nil || !*snapshotItem.Enabled {
		t.Fatalf("snapshot sample enabled = %#v, want true", snapshotItem.Enabled)
	}
	if got := pluginRawScalarValue(t, snapshotItem, "mode"); got != "first" {
		t.Fatalf("snapshot raw mode = %q, want first", got)
	}
	h.mu.Lock()
	handlerItem := h.cfg.Plugins.Configs["sample"]
	h.mu.Unlock()
	if got := pluginRawScalarValue(t, handlerItem, "mode"); got != "second" {
		t.Fatalf("handler raw mode = %q, want second", got)
	}

	close(releaseReload)
	waitForReloadDone(t, reloadDone)
}

func TestPutPluginConfigReplacesPluginConfig(t *testing.T) {
	t.Parallel()

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

func TestDeletePluginRemovesDiscoveredFileAndConfig(t *testing.T) {
	t.Parallel()

	pluginsDir := writeManagementPluginFile(t, "sample")
	h := &Handler{
		cfg: &config.Config{
			Plugins: config.PluginsConfig{
				Dir: pluginsDir,
				Configs: map[string]config.PluginInstanceConfig{
					"sample": pluginConfigFromYAML(t, "enabled: true\nmode: safe\n"),
				},
			},
		},
		configFilePath: writeTestConfigFile(t),
	}
	reloads := make(chan *config.Config, 1)
	releaseReload := make(chan struct{})
	reloadDone := make(chan struct{})
	h.SetConfigReloadHook(func(_ context.Context, cfg *config.Config) {
		defer close(reloadDone)
		reloads <- cfg
		<-releaseReload
	})

	path, errPath := pluginFilePath(pluginsDir, "sample")
	if errPath != nil {
		t.Fatalf("pluginFilePath() error = %v", errPath)
	}
	if path == "" {
		t.Fatal("plugin path is empty")
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Params = gin.Params{{Key: "id", Value: "sample"}}
	c.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/plugins/sample", nil)

	done := make(chan struct{})
	go func() {
		h.DeletePlugin(c)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("DeletePlugin blocked waiting for config reload")
	}

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if _, ok := h.cfg.Plugins.Configs["sample"]; ok {
		t.Fatal("plugin config still exists after delete")
	}
	if _, errStat := os.Stat(path); !os.IsNotExist(errStat) {
		t.Fatalf("plugin file stat error = %v, want not exist", errStat)
	}
	cfgSnapshot := waitForAsyncReload(t, reloads)
	if cfgSnapshot == h.cfg {
		close(releaseReload)
		waitForReloadDone(t, reloadDone)
		t.Fatalf("reload config = handler config %p, want independent snapshot", h.cfg)
	}
	if _, ok := cfgSnapshot.Plugins.Configs["sample"]; ok {
		close(releaseReload)
		waitForReloadDone(t, reloadDone)
		t.Fatal("snapshot plugin config still exists after delete")
	}
	close(releaseReload)
	waitForReloadDone(t, reloadDone)
}

func TestDeletePluginReturnsNotFoundForUnknownPlugin(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg:            &config.Config{},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Params = gin.Params{{Key: "id", Value: "missing"}}
	c.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/plugins/missing", nil)

	h.DeletePlugin(c)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

func TestPluginDisplayFieldsEscapeHTML(t *testing.T) {
	t.Parallel()

	fields := pluginConfigFields([]pluginapi.ConfigField{{
		Name:        `<img src=x onerror=alert(1)>`,
		Type:        pluginapi.ConfigFieldTypeEnum,
		EnumValues:  []string{`<fast>`, `safe & sound`},
		Description: `"quoted" 'single' <b>mode</b>`,
	}})
	if len(fields) != 1 {
		t.Fatalf("fields len = %d, want 1", len(fields))
	}
	if fields[0].Name != html.EscapeString(`<img src=x onerror=alert(1)>`) {
		t.Fatalf("field name = %q, want escaped", fields[0].Name)
	}
	if fields[0].EnumValues[0] != html.EscapeString(`<fast>`) || fields[0].EnumValues[1] != html.EscapeString(`safe & sound`) {
		t.Fatalf("enum values = %#v, want escaped values", fields[0].EnumValues)
	}
	if fields[0].Description != html.EscapeString(`"quoted" 'single' <b>mode</b>`) {
		t.Fatalf("description = %q, want escaped", fields[0].Description)
	}

	menus := pluginMenus([]pluginhost.RegisteredPluginMenu{{
		Path:        `/v0/resource/plugins/sample/<status>`,
		Menu:        `<b>Status</b>`,
		Description: `Shows <script>alert(1)</script>.`,
	}})
	if len(menus) != 1 {
		t.Fatalf("menus len = %d, want 1", len(menus))
	}
	if menus[0].Path != html.EscapeString(`/v0/resource/plugins/sample/<status>`) ||
		menus[0].Menu != html.EscapeString(`<b>Status</b>`) ||
		menus[0].Description != html.EscapeString(`Shows <script>alert(1)</script>.`) {
		t.Fatalf("menu = %#v, want escaped strings", menus[0])
	}

	meta := pluginMetadata(pluginapi.Metadata{
		Name:             `<script>alert(1)</script>`,
		Version:          `1.0.0&evil=true`,
		Author:           `"attacker"`,
		GitHubRepository: `https://example.com/repo?x=<script>`,
		Logo:             `<svg onload=alert(1)>`,
	})
	if meta.Name != html.EscapeString(`<script>alert(1)</script>`) ||
		meta.Version != html.EscapeString(`1.0.0&evil=true`) ||
		meta.Author != html.EscapeString(`"attacker"`) ||
		meta.GitHubRepository != html.EscapeString(`https://example.com/repo?x=<script>`) ||
		meta.Logo != html.EscapeString(`<svg onload=alert(1)>`) {
		t.Fatalf("metadata = %#v, want escaped strings", meta)
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

func pluginRawScalarValue(t *testing.T, item config.PluginInstanceConfig, key string) string {
	t.Helper()
	for i := 0; i+1 < len(item.Raw.Content); i += 2 {
		if item.Raw.Content[i] != nil && item.Raw.Content[i].Value == key && item.Raw.Content[i+1] != nil {
			return item.Raw.Content[i+1].Value
		}
	}
	t.Fatalf("plugin raw missing scalar key %q", key)
	return ""
}

func setPluginRawScalarValue(t *testing.T, node *yaml.Node, key, value string) {
	t.Helper()
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i] != nil && node.Content[i].Value == key && node.Content[i+1] != nil {
			node.Content[i+1].Value = value
			return
		}
	}
	t.Fatalf("plugin raw missing scalar key %q", key)
}
