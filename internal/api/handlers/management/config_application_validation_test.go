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
	for _, tc := range []struct{ name, method, path, body string }{
		{"v8 json", http.MethodPatch, "/v8/management/config", `{"server":{"host":"0.0.0.0"}}`},
		{"v8 yaml", http.MethodPut, "/v8/management/config.yaml", "config-version: 8\nserver: {host: 0.0.0.0, port: 8317}\n"},
		{"legacy yaml", http.MethodPut, "/legacy/config.yaml", "host: 0.0.0.0\nport: 8317\n"},
		{"scalar", http.MethodPut, "/legacy/request-retry", `{"value":8}`},
		{"api keys", http.MethodPut, "/legacy/api-keys", `[]`},
		{"plugin", http.MethodPatch, "/plugins/probe/enabled", `{"enabled":true}`},
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
			router.PATCH("/v8/management/config", h.ConfigV8)
			router.PUT("/v8/management/config.yaml", h.ConfigV8)
			router.PUT("/legacy/config.yaml", h.PutConfigYAML)
			router.PUT("/legacy/request-retry", h.PutRequestRetry)
			router.PUT("/legacy/api-keys", h.PutAPIKeys)
			router.PATCH("/plugins/:id/enabled", h.PatchPluginEnabled)
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
