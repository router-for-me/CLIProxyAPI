package management

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

func TestValidateDefaultUserConfig(t *testing.T) {
	cases := []struct {
		name        string
		scope       string
		defaultUser string
		endpoints   []string
		wantErr     bool
	}{
		{name: "empty_ok_read", scope: store.MgmtTokenScopeRead},
		{name: "empty_ok_write", scope: store.MgmtTokenScopeWrite},
		{
			name: "default_on_write_ok", scope: store.MgmtTokenScopeWrite,
			defaultUser: "user-1", endpoints: []string{"/v0/management/api-keys-pg"},
		},
		{
			name: "default_on_read_rejected", scope: store.MgmtTokenScopeRead,
			defaultUser: "user-1", endpoints: []string{"/v0/management/api-keys-pg"},
			wantErr: true,
		},
		{
			name: "endpoints_without_user_rejected", scope: store.MgmtTokenScopeWrite,
			endpoints: []string{"/v0/management/api-keys-pg"},
			wantErr:   true,
		},
		{
			name: "relative_endpoint_rejected", scope: store.MgmtTokenScopeWrite,
			defaultUser: "user-1", endpoints: []string{"v0/management/api-keys-pg"},
			wantErr: true,
		},
		{
			name: "user_without_endpoints_ok", scope: store.MgmtTokenScopeWrite,
			defaultUser: "user-1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := validateDefaultUserConfig(tc.scope, tc.defaultUser, tc.endpoints)
			if tc.wantErr && got == "" {
				t.Fatalf("expected an error message, got none")
			}
			if !tc.wantErr && got != "" {
				t.Fatalf("expected valid config, got %q", got)
			}
		})
	}
}

func TestPathMatchesAny(t *testing.T) {
	patterns := []string{"/v0/management/api-keys-pg", "/v0/management/litellm/*"}
	cases := []struct {
		path string
		want bool
	}{
		{"/v0/management/api-keys-pg", true},
		{"/v0/management/litellm/keys", true},
		{"/v0/management/litellm/key/generate", true},
		{"/v0/management/internal-users", false},
		{"/v0/management/api-keys-pg/extra", false},
	}
	for _, tc := range cases {
		if got := pathMatchesAny(tc.path, patterns); got != tc.want {
			t.Errorf("pathMatchesAny(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
	if pathMatchesAny("/v0/management/api-keys-pg", nil) {
		t.Errorf("empty pattern list must match nothing")
	}
}

// TestEnforceTokenPolicyDefaultUser ensures the middleware only exposes the
// default-user fallback when a write-scope token names one and the request path
// is on its allow-list.
func TestEnforceTokenPolicyDefaultUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cases := []struct {
		name        string
		scope       string
		defaultUser string
		endpoints   []string
		path        string
		want        string
	}{
		{
			name: "write_allowlisted_applies", scope: store.MgmtTokenScopeWrite,
			defaultUser: "user-1", endpoints: []string{"/v0/management/api-keys-pg"},
			path: "/v0/management/api-keys-pg", want: "user-1",
		},
		{
			name: "write_non_allowlisted_skips", scope: store.MgmtTokenScopeWrite,
			defaultUser: "user-1", endpoints: []string{"/v0/management/api-keys-pg"},
			path: "/v0/management/internal-users", want: "",
		},
		{
			name: "read_scope_skips", scope: store.MgmtTokenScopeRead,
			defaultUser: "user-1", endpoints: []string{"/v0/management/api-keys-pg"},
			path: "/v0/management/api-keys-pg", want: "",
		},
		{
			name: "no_default_skips", scope: store.MgmtTokenScopeWrite,
			endpoints: []string{"/v0/management/api-keys-pg"},
			path:      "/v0/management/api-keys-pg", want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &Handler{}
			var got string
			engine := gin.New()
			engine.Use(func(c *gin.Context) {
				c.Set(ctxMgmtToken, &store.ManagementToken{
					ID: "tok-1", Scope: tc.scope,
					DefaultUserID:          tc.defaultUser,
					DefaultUserIDEndpoints: tc.endpoints,
				})
				c.Set(ctxMgmtPolicy, (*store.ManagementTokenPolicy)(nil))
			})
			engine.Use(h.EnforceTokenPolicy())
			engine.POST(tc.path, func(c *gin.Context) {
				got = defaultUserIDFromContext(c)
				c.Status(http.StatusOK)
			})
			req := httptest.NewRequest(http.MethodPost, tc.path, nil)
			engine.ServeHTTP(httptest.NewRecorder(), req)
			if got != tc.want {
				t.Fatalf("defaultUserIDFromContext = %q, want %q", got, tc.want)
			}
		})
	}
}
