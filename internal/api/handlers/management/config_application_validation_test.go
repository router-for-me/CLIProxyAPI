package management

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

func TestApplicationValidationRejectsManagementWritesBeforePublishing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name, method, path, body string
		legacy                   bool
	}{
		{"v8 json", http.MethodPatch, "/v8/management/config", `{"server":{"host":"0.0.0.0"}}`, false},
		{"v8 yaml", http.MethodPut, "/v8/management/config.yaml", "config-version: 8\nserver: {host: 0.0.0.0, port: 8317}\n", false},
		// Deprecated v0 endpoints keep their contract and skip application validation.
		{"legacy yaml", http.MethodPut, "/legacy/config.yaml", "host: 0.0.0.0\nport: 8317\n", true},
		{"scalar", http.MethodPut, "/legacy/request-retry", `{"value":8}`, true},
		{"api keys", http.MethodPut, "/legacy/api-keys", `[]`, true},
		{"plugin", http.MethodPatch, "/legacy/plugins/probe/enabled", `{"enabled":true}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			raw := []byte("host: 127.0.0.1\nport: 8317\nrequest-retry: 3\napi-keys: [client]\n")
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.LoadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			h := NewHandler(cfg, path, nil)
			h.SetConfigValidator(func(next *config.Config) error {
				if next.Host != "127.0.0.1" || next.RequestRetry > 3 || len(next.APIKeys) == 0 || len(next.Plugins.Configs) > 0 {
					return errors.New("application configuration rejected")
				}
				return nil
			})
			router := gin.New()
			v8 := router.Group("/v8/management", func(c *gin.Context) { c.Set(ConfigV8ContextKey, true) })
			v8.PATCH("/config", h.ConfigV8)
			v8.PUT("/config.yaml", h.ConfigV8)
			router.PUT("/legacy/config.yaml", h.PutConfigYAML)
			router.PUT("/legacy/request-retry", h.PutRequestRetry)
			router.PUT("/legacy/api-keys", h.PutAPIKeys)
			router.PATCH("/legacy/plugins/:id/enabled", h.PatchPluginEnabled)
			// A successful mutation becomes the baseline; a rejection must not revert it.
			good := httptest.NewRecorder()
			router.ServeHTTP(good, httptest.NewRequest(http.MethodPut, "/legacy/request-retry", strings.NewReader(`{"value":2}`)))
			if good.Code != 200 {
				t.Fatalf("valid save failed: %d %s", good.Code, good.Body.String())
			}
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			rejected := httptest.NewRecorder()
			router.ServeHTTP(rejected, httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body)))
			if tc.legacy {
				if rejected.Code != http.StatusOK {
					t.Fatalf("deprecated endpoint changed behavior: %d %s", rejected.Code, rejected.Body.String())
				}
				return
			}
			if rejected.Code != http.StatusUnprocessableEntity {
				t.Fatalf("invalid application config acknowledged: %d %s", rejected.Code, rejected.Body.String())
			}
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if string(after) != string(before) {
				t.Fatal("rejected mutation changed persisted configuration")
			}
			if h.cfg.Host != "127.0.0.1" || h.cfg.RequestRetry != 2 || len(h.cfg.APIKeys) != 1 || len(h.cfg.Plugins.Configs) > 0 {
				t.Fatal("rejected mutation changed handler configuration")
			}
		})
	}
}

func TestApplicationValidationSeesHomeRuntimeConfig(t *testing.T) {
	gin.SetMode(gin.TestMode)
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("host: 127.0.0.1\nport: 8317\napi-keys: [client]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Home.Enabled = true
	h := NewHandler(cfg, path, nil)
	// Home mode clears local API keys at runtime, so the application must not
	// judge a save by the stale keys still on disk.
	h.SetConfigValidator(func(next *config.Config) error {
		if len(next.APIKeys) > 0 {
			return errors.New("home runtime config must not carry local api keys")
		}
		return nil
	})
	router := gin.New()
	router.PATCH("/v8/management/config", func(c *gin.Context) { c.Set(ConfigV8ContextKey, true) }, h.ConfigV8)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodPatch, "/v8/management/config", strings.NewReader(`{"server":{"port":8318}}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("Home-mode save validated against stale local config: %d %s", rec.Code, rec.Body.String())
	}
	if len(h.cfg.APIKeys) != 1 {
		t.Fatal("validation rewrote the handler config instead of a clone")
	}
}

func TestApplicationValidationRejectsPluginDeleteBeforeRemovingArtifact(t *testing.T) {
	gin.SetMode(gin.TestMode)
	pluginsDir := writeManagementPluginFile(t, "sample")
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	raw := []byte("plugins:\n  configs:\n    sample:\n      enabled: true\n")
	if err := os.WriteFile(configPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	h := &Handler{
		cfg: &config.Config{Plugins: config.PluginsConfig{
			Dir:     pluginsDir,
			Configs: map[string]config.PluginInstanceConfig{"sample": pluginConfigFromYAML(t, "enabled: true\n")},
		}},
		configFilePath: configPath,
	}
	h.SetConfigValidator(func(next *config.Config) error {
		if _, ok := next.Plugins.Configs["sample"]; !ok {
			return errors.New("sample plugin is required")
		}
		return nil
	})
	artifact, err := pluginFilePath(pluginsDir, "sample")
	if err != nil || artifact == "" {
		t.Fatalf("plugin path %q: %v", artifact, err)
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Set(ConfigV8ContextKey, true)
	c.Params = gin.Params{{Key: "id", Value: "sample"}}
	c.Request = httptest.NewRequest(http.MethodDelete, "/v8/management/plugins/sample", nil)
	h.DeletePlugin(c)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(artifact); err != nil {
		t.Fatalf("rejected delete removed the plugin artifact: %v", err)
	}
	if _, ok := h.cfg.Plugins.Configs["sample"]; !ok {
		t.Fatal("rejected delete changed handler configuration")
	}
	after, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(raw) {
		t.Fatal("rejected delete changed persisted configuration")
	}
}

func TestApplicationValidationRechecksPluginDeleteAgainstFinalConfig(t *testing.T) {
	gin.SetMode(gin.TestMode)
	pluginsDir := writeManagementPluginFile(t, "sample")
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	raw := []byte("plugins:\n  configs:\n    sample:\n      enabled: true\n")
	if err := os.WriteFile(configPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	h := &Handler{
		cfg: &config.Config{Plugins: config.PluginsConfig{
			Dir:     pluginsDir,
			Configs: map[string]config.PluginInstanceConfig{"sample": pluginConfigFromYAML(t, "enabled: true\n")},
		}},
		configFilePath: configPath,
	}
	// A concurrent v8 save between the first check and removal can make the
	// deletion invalid; model that by rejecting every check after the first.
	validations := 0
	h.SetConfigValidator(func(*config.Config) error {
		validations++
		if validations > 1 {
			return errors.New("configuration changed to require the sample plugin")
		}
		return nil
	})
	artifact, err := pluginFilePath(pluginsDir, "sample")
	if err != nil || artifact == "" {
		t.Fatalf("plugin path %q: %v", artifact, err)
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Set(ConfigV8ContextKey, true)
	c.Params = gin.Params{{Key: "id", Value: "sample"}}
	c.Request = httptest.NewRequest(http.MethodDelete, "/v8/management/plugins/sample", nil)
	h.DeletePlugin(c)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(artifact); err != nil {
		t.Fatalf("delete removed the artifact without validating the final config: %v", err)
	}
	if _, ok := h.cfg.Plugins.Configs["sample"]; !ok {
		t.Fatal("rejected delete changed handler configuration")
	}
}

func TestApplicationValidationChecksPluginConfiguredDuringDelete(t *testing.T) {
	gin.SetMode(gin.TestMode)
	pluginsDir := writeManagementPluginFile(t, "sample")
	h := &Handler{
		cfg:            &config.Config{Plugins: config.PluginsConfig{Dir: pluginsDir, Configs: map[string]config.PluginInstanceConfig{}}},
		configFilePath: filepath.Join(t.TempDir(), "config.yaml"),
	}
	h.SetConfigValidator(func(*config.Config) error { return errors.New("sample plugin is required") })
	item := pluginConfigFromYAML(t, "enabled: true\n")
	// A concurrent save configures the discovered plugin while the delete is unlocked.
	original := beforePluginDeleteCommit
	beforePluginDeleteCommit = func() {
		h.mu.Lock()
		h.cfg.Plugins.Configs["sample"] = item
		h.mu.Unlock()
	}
	t.Cleanup(func() { beforePluginDeleteCommit = original })
	artifact, err := pluginFilePath(pluginsDir, "sample")
	if err != nil || artifact == "" {
		t.Fatalf("plugin path %q: %v", artifact, err)
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Set(ConfigV8ContextKey, true)
	c.Params = gin.Params{{Key: "id", Value: "sample"}}
	c.Request = httptest.NewRequest(http.MethodDelete, "/v8/management/plugins/sample", nil)
	h.DeletePlugin(c)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(artifact); err != nil {
		t.Fatalf("delete removed the artifact without validating the new configuration: %v", err)
	}
	if _, ok := h.cfg.Plugins.Configs["sample"]; !ok {
		t.Fatal("delete dropped a configuration it never validated")
	}
}

func TestPluginDeleteResolvesArtifactFromConfigSavedDuringDelete(t *testing.T) {
	gin.SetMode(gin.TestMode)
	oldDir := writeManagementPluginFile(t, "sample")
	newDir := writeManagementPluginFile(t, "sample")
	h := &Handler{
		cfg:            &config.Config{Plugins: config.PluginsConfig{Dir: oldDir, Configs: map[string]config.PluginInstanceConfig{}}},
		configFilePath: filepath.Join(t.TempDir(), "config.yaml"),
	}
	// A concurrent save moves the plugins directory while the delete is unlocked.
	original := beforePluginDeleteCommit
	beforePluginDeleteCommit = func() {
		h.mu.Lock()
		h.cfg.Plugins.Dir = newDir
		h.mu.Unlock()
	}
	t.Cleanup(func() { beforePluginDeleteCommit = original })
	oldArtifact, errOld := pluginFilePath(oldDir, "sample")
	newArtifact, errNew := pluginFilePath(newDir, "sample")
	if errOld != nil || errNew != nil || oldArtifact == "" || newArtifact == "" {
		t.Fatalf("plugin paths %q, %q: %v, %v", oldArtifact, newArtifact, errOld, errNew)
	}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Set(ConfigV8ContextKey, true)
	c.Params = gin.Params{{Key: "id", Value: "sample"}}
	c.Request = httptest.NewRequest(http.MethodDelete, "/v8/management/plugins/sample", nil)
	h.DeletePlugin(c)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(oldArtifact); err != nil {
		t.Fatalf("delete removed the artifact from the replaced plugins dir: %v", err)
	}
	if _, err := os.Stat(newArtifact); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("delete kept the artifact the current config resolves to: %v", err)
	}
}

func TestPluginDeleteRejectedAfterUnloadReappliesCurrentConfig(t *testing.T) {
	gin.SetMode(gin.TestMode)
	pluginsDir := writeManagementPluginFile(t, "sample")
	host := pluginhost.New()
	host.RegisterPluginForTest("sample", pluginapi.Plugin{Metadata: pluginapi.Metadata{Name: "sample"}})
	h := &Handler{
		cfg: &config.Config{Plugins: config.PluginsConfig{
			Dir:     pluginsDir,
			Configs: map[string]config.PluginInstanceConfig{"sample": pluginConfigFromYAML(t, "enabled: true\n")},
		}},
		configFilePath: filepath.Join(t.TempDir(), "config.yaml"),
		pluginHost:     host,
	}
	reloaded := make(chan *config.Config, 1)
	forced := make(chan bool, 1)
	h.SetConfigReloadHook(func(ctx context.Context, cfg *config.Config) {
		forced <- ForcedConfigReload(ctx)
		reloaded <- cfg
	})
	// A save applied while the plugin unloads makes the deletion invalid.
	validations := 0
	h.SetConfigValidator(func(*config.Config) error {
		validations++
		if validations > 1 {
			return errors.New("configuration changed to require the sample plugin")
		}
		return nil
	})
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Set(ConfigV8ContextKey, true)
	c.Params = gin.Params{{Key: "id", Value: "sample"}}
	c.Request = httptest.NewRequest(http.MethodDelete, "/v8/management/plugins/sample", nil)
	h.DeletePlugin(c)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422; body=%s", rec.Code, rec.Body.String())
	}
	if host.PluginBusy("sample") {
		t.Fatal("test setup: plugin was not unloaded before the final check")
	}
	select {
	case cfg := <-reloaded:
		if _, ok := cfg.Plugins.Configs["sample"]; !ok {
			t.Fatal("reapplied config dropped the sample plugin")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("rejected delete left the plugin unloaded without reapplying the current config")
	}
	// The concurrent save may already be applied, so an unchanged file must still reapply.
	if !<-forced {
		t.Fatal("reapply after unload was not forced, so an unchanged config file skips it")
	}
}

func TestPluginDeleteAfterUnloadForcesConfigReload(t *testing.T) {
	gin.SetMode(gin.TestMode)
	pluginsDir := writeManagementPluginFile(t, "sample")
	host := pluginhost.New()
	host.RegisterPluginForTest("sample", pluginapi.Plugin{Metadata: pluginapi.Metadata{Name: "sample"}})
	// Unconfigured plugin: deleting only its file leaves the config file unchanged.
	h := &Handler{
		cfg:            &config.Config{Plugins: config.PluginsConfig{Dir: pluginsDir}},
		configFilePath: filepath.Join(t.TempDir(), "config.yaml"),
		pluginHost:     host,
	}
	forced := make(chan bool, 1)
	h.SetConfigReloadHook(func(ctx context.Context, _ *config.Config) { forced <- ForcedConfigReload(ctx) })
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Params = gin.Params{{Key: "id", Value: "sample"}}
	c.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/plugins/sample", nil)
	h.DeletePlugin(c)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	select {
	case ok := <-forced:
		if !ok {
			t.Fatal("reload after unload was not forced, so the SDK keeps the unloaded plugin's runtime state")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("delete did not reload the config after unloading")
	}
}
