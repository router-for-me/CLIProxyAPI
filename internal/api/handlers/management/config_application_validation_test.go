package management

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
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
