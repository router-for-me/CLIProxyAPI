package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestAuthFilesList_ReportsOAuthProvider(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	auths := []*coreauth.Auth{
		{ID: "builtin", FileName: "builtin.json", Provider: "vendor"},
		{ID: "attributes", FileName: "attributes.json", Provider: "vendor", Attributes: map[string]string{"oauth_provider": "vendor-cn"}},
		{ID: "metadata", FileName: "metadata.json", Provider: "vendor", Metadata: map[string]any{"oauth_provider": "vendor-cn"}},
		{ID: "portal", FileName: "portal.json", Provider: "vendor", Metadata: map[string]any{"portal": " Vendor-CN "}},
		{ID: "precedence", FileName: "precedence.json", Provider: "vendor", Attributes: map[string]string{"oauth_provider": "vendor"}, Metadata: map[string]any{"oauth_provider": "vendor-cn", "portal": "ignored"}},
		{ID: "non-string", FileName: "non-string.json", Provider: "vendor", Metadata: map[string]any{"oauth_provider": 42}},
	}
	for _, auth := range auths {
		if auth.Attributes == nil {
			auth.Attributes = map[string]string{}
		}
		// Runtime-only keeps the entries off the auth dir, which the list route does not need.
		auth.Attributes["runtime_only"] = "true"
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("failed to register auth %s: %v", auth.ID, errRegister)
		}
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, manager)

	listFiles := func(v8 bool) map[string]map[string]any {
		t.Helper()
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		ctx.Request = httptest.NewRequest(http.MethodGet, "/v8/management/credentials", nil)
		if v8 {
			ctx.Set(ConfigV8ContextKey, true)
		}
		h.ListAuthFiles(ctx)
		if rec.Code != http.StatusOK {
			t.Fatalf("expected status 200, got %d: %s", rec.Code, rec.Body.String())
		}
		var resp struct {
			Files []map[string]any `json:"files"`
		}
		if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &resp); errUnmarshal != nil {
			t.Fatalf("failed to unmarshal files: %v", errUnmarshal)
		}
		out := make(map[string]map[string]any, len(resp.Files))
		for _, f := range resp.Files {
			id, _ := f["id"].(string)
			out[id] = f
		}
		return out
	}

	files := listFiles(true)
	cases := []struct {
		id   string
		want string
	}{
		{id: "builtin"},
		{id: "attributes", want: "vendor-cn"},
		{id: "metadata", want: "vendor-cn"},
		{id: "portal", want: "vendor-cn"},
		{id: "precedence", want: "vendor"},
		{id: "non-string"},
	}
	for _, tc := range cases {
		file, ok := files[tc.id]
		if !ok {
			t.Fatalf("expected auth entry %s in list", tc.id)
		}
		got, hasOAuthProvider := file["oauth_provider"]
		if tc.want == "" {
			if hasOAuthProvider {
				t.Errorf("%s: expected no oauth_provider, got %#v", tc.id, got)
			}
		} else if got != tc.want {
			t.Errorf("%s: expected oauth_provider %q, got %#v", tc.id, tc.want, got)
		}
		if file["type"] != "vendor" || file["provider"] != "vendor" {
			t.Errorf("%s: expected type/provider to stay vendor, got %#v/%#v", tc.id, file["type"], file["provider"])
		}
	}

	// The deprecated v0 route keeps its frozen response shape.
	for id, file := range listFiles(false) {
		if got, ok := file["oauth_provider"]; ok {
			t.Errorf("%s: v0 auth files must not expose oauth_provider, got %#v", id, got)
		}
	}
}
