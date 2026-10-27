package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
)

func TestPluginManagementV8AliasUsesAuthenticationAndPreservesQuery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name, path                string
		authorized, enabled, home bool
		want, calls               int
	}{
		{"authorized", "/v8/management/plugins/synthetic/accounting?cursor=a%2Bb&cursor=c%26d&empty=", true, true, false, 200, 1},
		{"unauthorized", "/v8/management/plugins/synthetic/accounting", false, true, false, 401, 0},
		{"encoded_unauthorized", "/v8/management/plugins/synthetic/%61ccounting", false, true, false, 401, 0},
		{"unknown", "/v8/management/plugins/synthetic/missing", true, true, false, 404, 1},
		{"disabled", "/v8/management/plugins/synthetic/accounting", true, false, false, 404, 0},
		{"home", "/v8/management/plugins/synthetic/accounting", true, true, true, 404, 0},
		{"near_prefix", "/v8/management/pluginsx/synthetic/accounting", true, true, false, 404, 0},
		{"near_management_prefix", "/v8/managementx/plugins/synthetic/accounting", true, true, false, 404, 0},
		{"legacy_v8_config", "/v8/management/debug", true, true, false, 404, 0},
		{"legacy_v0", "/v0/management/plugins/synthetic/accounting?cursor=a%2Bb&cursor=c%26d&empty=", true, true, false, 200, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.RemoteManagement.SecretKey = "synthetic-key-configured"
			cfg.Home.Enabled = test.home
			handler := management.NewHandler(cfg, filepath.Join(t.TempDir(), "config.yaml"), nil)
			handler.SetLocalPassword("synthetic-management-password")
			server := &Server{cfg: cfg, mgmt: handler, pluginHost: pluginhost.New(), engine: gin.New()}
			server.managementRoutesEnabled.Store(test.enabled)
			calls := 0
			server.engine.NoRoute(func(c *gin.Context) {
				server.pluginManagementNoRouteWithDispatcher(c, func(w http.ResponseWriter, r *http.Request) bool {
					calls++
					if r.URL.Path != "/v0/management/plugins/synthetic/accounting" {
						return false
					}
					values := r.URL.Query()["cursor"]
					if len(values) != 2 || values[0] != "a+b" || values[1] != "c&d" || !r.URL.Query().Has("empty") {
						t.Fatalf("query changed during alias dispatch: %q", r.URL.RawQuery)
					}
					w.WriteHeader(http.StatusOK)
					return true
				})
			})
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			request.RemoteAddr = "127.0.0.1:1234"
			originalPath, originalQuery, originalURI := request.URL.Path, request.URL.RawQuery, request.RequestURI
			if test.authorized {
				request.Header.Set("Authorization", "Bearer synthetic-management-password")
			}
			response := httptest.NewRecorder()
			server.engine.ServeHTTP(response, request)
			if response.Code != test.want || calls != test.calls {
				t.Fatalf("status=%d calls=%d, want %d/%d body=%s", response.Code, calls, test.want, test.calls, response.Body.String())
			}
			if request.URL.Path != originalPath || request.URL.RawQuery != originalQuery || request.RequestURI != originalURI {
				t.Fatal("alias dispatch modified the caller request")
			}
		})
	}
}
