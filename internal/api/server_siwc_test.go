package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"golang.org/x/crypto/bcrypt"
)

func TestSIWCManagementRouteAccessControl(t *testing.T) {
	hash, errHash := bcrypt.GenerateFromPassword([]byte("test-password"), bcrypt.MinCost)
	if errHash != nil {
		t.Fatal(errHash)
	}
	for _, tc := range []struct {
		name                      string
		enabled, home, authorized bool
		want                      int
	}{
		{"authorized invalid target", true, false, true, http.StatusBadRequest},
		{"missing management key", true, false, false, http.StatusUnauthorized},
		{"management disabled", false, false, true, http.StatusNotFound},
		{"home disabled local API", true, true, true, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{AuthDir: t.TempDir()}
			cfg.Home.Enabled = tc.home
			cfg.RemoteManagement.SecretKey = string(hash)
			h := management.NewHandlerWithoutConfigFilePath(cfg, nil)
			h.SetLocalPassword("test-password")
			s := &Server{cfg: cfg, engine: gin.New(), mgmt: h}
			s.managementRoutesEnabled.Store(tc.enabled)
			s.registerManagementRoutes()
			req := httptest.NewRequest(http.MethodPost, "/v8/management/oauth/siwc/bind", strings.NewReader(`{}`))
			req.Header.Set("Content-Type", "application/json")
			req.RemoteAddr = "127.0.0.1:1234"
			if tc.authorized {
				req.Header.Set("Authorization", "Bearer test-password")
			}
			w := httptest.NewRecorder()
			s.engine.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("status=%d want=%d: %s", w.Code, tc.want, w.Body.String())
			}
			for _, route := range s.engine.Routes() {
				if strings.HasPrefix(route.Path, "/v0/") && strings.Contains(route.Path, "siwc") {
					t.Fatal("SIWC route registered under v0")
				}
			}
		})
	}
}
