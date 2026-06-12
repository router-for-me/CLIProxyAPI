package management

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"html"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestListPluginStoreMergesInstalledStatus(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	pluginsDir := writeManagementPluginFile(t, "sample-provider")
	h := &Handler{
		cfg: &config.Config{
			Plugins: config.PluginsConfig{
				Enabled: true,
				Dir:     pluginsDir,
				Configs: map[string]config.PluginInstanceConfig{
					"sample-provider": pluginConfigFromYAML(t, "enabled: true\nmode: fast\n"),
				},
			},
		},
		configFilePath:         writeTestConfigFile(t),
		pluginStoreRegistryURL: "https://registry.example/registry.json",
		pluginStoreHTTPClient: fakePluginStoreHTTPClient{
			"https://registry.example/registry.json": registryJSON(t),
		},
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/plugin-store", nil)

	h.ListPluginStore(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var body pluginStoreListResponse
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &body); errDecode != nil {
		t.Fatalf("Unmarshal() error = %v; body=%s", errDecode, rec.Body.String())
	}
	if !body.PluginsEnabled {
		t.Fatal("plugins_enabled = false, want true")
	}
	if len(body.Plugins) != 1 {
		t.Fatalf("plugins len = %d, want 1", len(body.Plugins))
	}
	entry := body.Plugins[0]
	if !entry.Installed || !entry.Configured || !entry.Enabled {
		t.Fatalf("store entry status = %#v, want installed configured enabled", entry)
	}
	if entry.Registered || entry.EffectiveEnabled {
		t.Fatalf("runtime status = registered %v effective %v, want false false", entry.Registered, entry.EffectiveEnabled)
	}
	if entry.InstalledVersion != "" {
		t.Fatalf("installed_version = %q, want empty for unregistered plugin", entry.InstalledVersion)
	}
	if entry.UpdateAvailable {
		t.Fatal("update_available = true, want false when installed version is unknown")
	}
	if entry.Path == "" {
		t.Fatal("path is empty")
	}
}

func TestListPluginStoreEscapesRegistryStrings(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	h := &Handler{
		cfg: &config.Config{
			Plugins: config.PluginsConfig{
				Enabled: true,
				Dir:     t.TempDir(),
			},
		},
		configFilePath:         writeTestConfigFile(t),
		pluginStoreRegistryURL: "https://registry.example/registry.json",
		pluginStoreHTTPClient: fakePluginStoreHTTPClient{
			"https://registry.example/registry.json": []byte(`{
				"schema_version": 1,
				"plugins": [{
					"id": "sample-provider",
					"name": "<script>alert(1)</script>",
					"description": "<img src=x onerror=alert(1)>",
					"author": "\"attacker\"",
					"version": "0.1.0",
					"repository": "https://github.com/author-name/cliproxy-sample-provider-plugin",
					"logo": "<svg onload=alert(1)>",
					"homepage": "https://example.com/?q=<x>",
					"license": "<b>MIT</b>",
					"tags": ["<provider>", "safe & sound"]
				}]
			}`),
		},
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/plugin-store", nil)

	h.ListPluginStore(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var body pluginStoreListResponse
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &body); errDecode != nil {
		t.Fatalf("Unmarshal() error = %v; body=%s", errDecode, rec.Body.String())
	}
	if len(body.Plugins) != 1 {
		t.Fatalf("plugins len = %d, want 1", len(body.Plugins))
	}
	entry := body.Plugins[0]
	if entry.Name != html.EscapeString("<script>alert(1)</script>") ||
		entry.Description != html.EscapeString("<img src=x onerror=alert(1)>") ||
		entry.Author != html.EscapeString(`"attacker"`) ||
		entry.Version != "0.1.0" ||
		entry.Repository != "https://github.com/author-name/cliproxy-sample-provider-plugin" ||
		entry.Logo != html.EscapeString("<svg onload=alert(1)>") ||
		entry.Homepage != html.EscapeString("https://example.com/?q=<x>") ||
		entry.License != html.EscapeString("<b>MIT</b>") {
		t.Fatalf("store entry = %#v, want escaped strings", entry)
	}
	if len(entry.Tags) != 2 ||
		entry.Tags[0] != html.EscapeString("<provider>") ||
		entry.Tags[1] != html.EscapeString("safe & sound") {
		t.Fatalf("tags = %#v, want escaped strings", entry.Tags)
	}
}

func TestInstallPluginFromStoreWritesFileAndEnablesConfig(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	pluginsDir := t.TempDir()
	archiveData := makeManagementPluginStoreZip(t, "sample-provider"+managementPluginExtension(runtime.GOOS), "library-data")
	archiveName := "sample-provider_0.1.0_" + runtime.GOOS + "_" + runtime.GOARCH + ".zip"
	checksum := sha256.Sum256(archiveData)
	h := &Handler{
		cfg: &config.Config{
			Plugins: config.PluginsConfig{
				Enabled: false,
				Dir:     pluginsDir,
				Configs: map[string]config.PluginInstanceConfig{
					"sample-provider": pluginConfigFromYAML(t, "enabled: false\nmode: fast\n"),
				},
			},
		},
		configFilePath:         writeTestConfigFile(t),
		pluginStoreRegistryURL: "https://registry.example/registry.json",
		pluginStoreHTTPClient: fakePluginStoreHTTPClient{
			"https://registry.example/registry.json": registryJSON(t),
			"https://api.github.com/repos/author-name/cliproxy-sample-provider-plugin/releases/tags/v0.1.0": []byte(`{
				"tag_name": "v0.1.0",
				"assets": [
					{"name": "` + archiveName + `", "browser_download_url": "https://downloads.example/` + archiveName + `"},
					{"name": "checksums.txt", "browser_download_url": "https://downloads.example/checksums.txt"}
				]
			}`),
			"https://downloads.example/" + archiveName: archiveData,
			"https://downloads.example/checksums.txt":  []byte(hex.EncodeToString(checksum[:]) + "  " + archiveName + "\n"),
		},
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Params = gin.Params{{Key: "id", Value: "sample-provider"}}
	c.Request = httptest.NewRequest(http.MethodPost, "/v0/management/plugin-store/sample-provider/install", nil)

	h.InstallPluginFromStore(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var body pluginInstallResponse
	if errDecode := json.Unmarshal(rec.Body.Bytes(), &body); errDecode != nil {
		t.Fatalf("Unmarshal() error = %v; body=%s", errDecode, rec.Body.String())
	}
	if body.Status != "installed" || body.ID != "sample-provider" || body.Version != "0.1.0" {
		t.Fatalf("install response = %#v", body)
	}
	if body.PluginsEnabled {
		t.Fatal("plugins_enabled = true, want false")
	}
	if body.RestartRequired {
		t.Fatal("restart_required = true, want false")
	}
	targetPath := filepath.Join(pluginsDir, runtime.GOOS, runtime.GOARCH, "sample-provider"+managementPluginExtension(runtime.GOOS))
	data, errRead := os.ReadFile(targetPath)
	if errRead != nil {
		t.Fatalf("ReadFile(%s) error = %v", targetPath, errRead)
	}
	if string(data) != "library-data" {
		t.Fatalf("installed file = %q, want library-data", data)
	}
	item := h.cfg.Plugins.Configs["sample-provider"]
	if item.Enabled == nil || !*item.Enabled {
		t.Fatalf("plugin enabled = %#v, want true", item.Enabled)
	}
	if h.cfg.Plugins.Enabled {
		t.Fatal("global plugins.enabled changed to true")
	}
	raw := marshalPluginRaw(t, item)
	if !strings.Contains(raw, "mode: fast") {
		t.Fatalf("plugin raw config lost custom field:\n%s", raw)
	}
}

func TestInstallPluginFromStoreOverwritesFilePreservesConfigAndReloads(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	pluginsDir := t.TempDir()
	existingPath := filepath.Join(pluginsDir, "sample-provider"+managementPluginExtension(runtime.GOOS))
	if errWrite := os.WriteFile(existingPath, []byte("old-library-data"), 0o644); errWrite != nil {
		t.Fatalf("WriteFile(%s) error = %v", existingPath, errWrite)
	}
	archiveData := makeManagementPluginStoreZip(t, "sample-provider"+managementPluginExtension(runtime.GOOS), "new-library-data")
	archiveName := "sample-provider_0.1.0_" + runtime.GOOS + "_" + runtime.GOARCH + ".zip"
	checksum := sha256.Sum256(archiveData)
	h := &Handler{
		cfg: &config.Config{
			Plugins: config.PluginsConfig{
				Enabled: true,
				Dir:     pluginsDir,
				Configs: map[string]config.PluginInstanceConfig{
					"sample-provider": pluginConfigFromYAML(t, "enabled: false\npriority: 5\nmode: fast\nextra: keep\n"),
				},
			},
		},
		configFilePath:         writeTestConfigFile(t),
		pluginStoreRegistryURL: "https://registry.example/registry.json",
		pluginStoreHTTPClient: fakePluginStoreHTTPClient{
			"https://registry.example/registry.json": registryJSON(t),
			"https://api.github.com/repos/author-name/cliproxy-sample-provider-plugin/releases/tags/v0.1.0": []byte(`{
				"tag_name": "v0.1.0",
				"assets": [
					{"name": "` + archiveName + `", "browser_download_url": "https://downloads.example/` + archiveName + `"},
					{"name": "checksums.txt", "browser_download_url": "https://downloads.example/checksums.txt"}
				]
			}`),
			"https://downloads.example/" + archiveName: archiveData,
			"https://downloads.example/checksums.txt":  []byte(hex.EncodeToString(checksum[:]) + "  " + archiveName + "\n"),
		},
	}
	reloads := 0
	h.SetConfigReloadHook(func(_ context.Context, cfg *config.Config) {
		reloads++
		if cfg != h.cfg {
			t.Fatalf("reload config = %p, want handler config %p", cfg, h.cfg)
		}
	})

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Params = gin.Params{{Key: "id", Value: "sample-provider"}}
	c.Request = httptest.NewRequest(http.MethodPost, "/v0/management/plugin-store/sample-provider/install", nil)

	h.InstallPluginFromStore(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if reloads != 1 {
		t.Fatalf("reloads = %d, want 1", reloads)
	}
	data, errRead := os.ReadFile(existingPath)
	if errRead != nil {
		t.Fatalf("ReadFile(%s) error = %v", existingPath, errRead)
	}
	if string(data) != "new-library-data" {
		t.Fatalf("installed file = %q, want new-library-data", data)
	}
	item := h.cfg.Plugins.Configs["sample-provider"]
	if item.Enabled == nil || !*item.Enabled {
		t.Fatalf("plugin enabled = %#v, want true", item.Enabled)
	}
	if item.Priority != 5 {
		t.Fatalf("plugin priority = %d, want 5", item.Priority)
	}
	raw := marshalPluginRaw(t, item)
	if !strings.Contains(raw, "mode: fast") || !strings.Contains(raw, "extra: keep") {
		t.Fatalf("plugin raw config lost custom fields:\n%s", raw)
	}
}

func TestEnablePluginConfigLockedPreservesExistingFields(t *testing.T) {
	t.Parallel()

	h := &Handler{
		cfg: &config.Config{
			Plugins: config.PluginsConfig{
				Enabled: false,
				Configs: map[string]config.PluginInstanceConfig{
					"sample-provider": pluginConfigFromYAML(t, "enabled: false\npriority: 5\nmode: fast\n"),
				},
			},
		},
	}

	if errEnable := h.enablePluginConfigLocked("sample-provider"); errEnable != nil {
		t.Fatalf("enablePluginConfigLocked() error = %v", errEnable)
	}
	if h.cfg.Plugins.Enabled {
		t.Fatal("global Plugins.Enabled changed to true")
	}
	item := h.cfg.Plugins.Configs["sample-provider"]
	if item.Enabled == nil || !*item.Enabled {
		t.Fatalf("plugin enabled = %#v, want true", item.Enabled)
	}
	if item.Priority != 5 {
		t.Fatalf("plugin priority = %d, want 5", item.Priority)
	}
	raw := marshalPluginRaw(t, item)
	if !strings.Contains(raw, "mode: fast") {
		t.Fatalf("plugin raw config lost custom field:\n%s", raw)
	}
}

func TestEnablePluginConfigLockedCreatesMissingConfig(t *testing.T) {
	t.Parallel()

	h := &Handler{cfg: &config.Config{}}
	if errEnable := h.enablePluginConfigLocked("sample-provider"); errEnable != nil {
		t.Fatalf("enablePluginConfigLocked() error = %v", errEnable)
	}
	item := h.cfg.Plugins.Configs["sample-provider"]
	if item.Enabled == nil || !*item.Enabled {
		t.Fatalf("plugin enabled = %#v, want true", item.Enabled)
	}
}

type fakePluginStoreHTTPClient map[string][]byte

func (c fakePluginStoreHTTPClient) Do(req *http.Request) (*http.Response, error) {
	body, ok := c[req.URL.String()]
	if !ok {
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Body:       io.NopCloser(strings.NewReader("not found")),
			Header:     make(http.Header),
			Request:    req,
		}, nil
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(body)),
		Header:     make(http.Header),
		Request:    req,
	}, nil
}

func registryJSON(t *testing.T) []byte {
	t.Helper()

	return []byte(`{
		"schema_version": 1,
		"plugins": [{
			"id": "sample-provider",
			"name": "Sample Provider",
			"description": "Adds sample provider support.",
			"author": "author-name",
			"version": "0.1.0",
			"repository": "https://github.com/author-name/cliproxy-sample-provider-plugin",
			"tags": ["provider"]
		}]
	}`)
}

func makeManagementPluginStoreZip(t *testing.T, name string, content string) []byte {
	t.Helper()

	var buffer bytes.Buffer
	writer := zip.NewWriter(&buffer)
	file, errCreate := writer.Create(name)
	if errCreate != nil {
		t.Fatalf("Create(%s) error = %v", name, errCreate)
	}
	if _, errWrite := file.Write([]byte(content)); errWrite != nil {
		t.Fatalf("Write(%s) error = %v", name, errWrite)
	}
	if errClose := writer.Close(); errClose != nil {
		t.Fatalf("Close() error = %v", errClose)
	}
	return buffer.Bytes()
}
