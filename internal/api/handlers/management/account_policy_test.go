package management

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/managementauth"
)

func TestAccountUserCredentialDeleteRequiresAdmin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &Handler{}

	for _, tc := range []struct {
		name    string
		role    string
		method  string
		path    string
		allowed bool
	}{
		{name: "user cannot delete one", role: managementauth.RoleUser, method: http.MethodDelete, path: "/v0/management/auth-files?name=credential.json"},
		{name: "user cannot batch delete", role: managementauth.RoleUser, method: http.MethodDelete, path: "/v0/management/auth-files?name=one.json&name=two.json"},
		{name: "admin can delete", role: managementauth.RoleAdmin, method: http.MethodDelete, path: "/v0/management/auth-files?name=credential.json", allowed: true},
		{name: "user can list", role: managementauth.RoleUser, method: http.MethodGet, path: "/v0/management/auth-files", allowed: true},
		{name: "user can upload", role: managementauth.RoleUser, method: http.MethodPost, path: "/v0/management/auth-files", allowed: true},
		{name: "user can change status", role: managementauth.RoleUser, method: http.MethodPatch, path: "/v0/management/auth-files/status", allowed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(tc.method, tc.path, nil)
			ctx.Set("management_account", managementauth.User{Username: "operator", Role: tc.role})

			allowed := h.authorizeAccountRequest(ctx)
			if allowed != tc.allowed {
				t.Fatalf("authorizeAccountRequest()=%v want %v; status=%d body=%s", allowed, tc.allowed, recorder.Code, recorder.Body.String())
			}
			if !tc.allowed {
				if recorder.Code != http.StatusForbidden {
					t.Fatalf("status=%d want %d", recorder.Code, http.StatusForbidden)
				}
				if !strings.Contains(recorder.Body.String(), "credential deletion") {
					t.Fatalf("response does not explain restriction: %s", recorder.Body.String())
				}
			}
		})
	}
}
