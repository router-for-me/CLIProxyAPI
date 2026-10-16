package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher/diff"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestPatchAuthFileFieldsExcludedModelsRuntime(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tt := range []struct {
		name   string
		fields string
		kind   string
		global []string
		want   string
	}{
		{"override", `"excluded_models":[" Local-* ","LOCAL-*",42]`, "oauth", []string{"global-*"}, "local-*"},
		{"alias override", `"excluded-models":["local-*"]`, "oauth", []string{"global-*"}, "local-*"},
		{"canonical wins", `"excluded_models":[],"excluded-models":["alias-*"]`, "oauth", []string{"global-*"}, "global-*"},
		{"empty falls back", `"excluded_models":[]`, "oauth", []string{"global-*"}, "global-*"},
		{"whitespace falls back", `"excluded_models":[" ","\t"]`, "oauth", []string{"global-*"}, "global-*"},
		{"null falls back", `"excluded_models":null`, "oauth", []string{"global-*"}, "global-*"},
		{"invalid entries fall back", `"excluded_models":[42,false]`, "oauth", []string{"global-*"}, "global-*"},
		{"no global clears stale", `"excluded_models":[]`, "oauth", nil, ""},
		{"apikey override", `"excluded_models":["local-*"]`, "apikey", []string{"global-*"}, "local-*"},
		{"apikey clear ignores global", `"excluded_models":[]`, "apikey", []string{"global-*"}, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			manager := coreauth.NewManager(nil, nil, nil)
			record := &coreauth.Auth{
				ID: "credential.json", FileName: "credential.json", Provider: "codex",
				Attributes: map[string]string{"auth_kind": tt.kind, "excluded_models": "stale-*", "excluded_models_hash": "stale-hash"},
				Metadata:   map[string]any{"excluded-models": []string{"old-*"}},
			}
			if _, err := manager.Register(context.Background(), record); err != nil {
				t.Fatal(err)
			}
			h := NewHandlerWithoutConfigFilePath(&config.Config{OAuthExcludedModels: map[string][]string{"codex": tt.global}}, manager)
			hookCalled := false
			check := func(auth *coreauth.Auth) {
				t.Helper()
				if got := auth.Attributes["excluded_models"]; got != tt.want {
					t.Errorf("excluded_models = %q, want %q", got, tt.want)
				}
				var rules []string
				if tt.want != "" {
					rules = strings.Split(tt.want, ",")
				}
				if got, want := auth.Attributes["excluded_models_hash"], diff.ComputeExcludedModelsHash(rules); got != want {
					t.Errorf("excluded_models_hash = %q, want %q", got, want)
				}
				if tt.want == "" {
					for _, key := range []string{"excluded_models", "excluded_models_hash"} {
						if _, exists := auth.Attributes[key]; exists {
							t.Errorf("stale attribute %s retained", key)
						}
					}
				}
				if got := auth.Attributes["auth_kind"]; got != tt.kind {
					t.Errorf("auth_kind = %q, want %q", got, tt.kind)
				}
			}
			h.postAuthPersistHook = func(_ context.Context, auth *coreauth.Auth) error { hookCalled = true; check(auth); return nil }
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPatch, "/v8/management/credentials/fields", strings.NewReader(`{"name":"credential.json",`+tt.fields+`}`))
			c.Request.Header.Set("Content-Type", "application/json")
			h.PatchAuthFileFields(c)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
			}
			updated, ok := manager.GetByID(record.ID)
			if !ok {
				t.Fatal("updated auth missing")
			}
			check(updated)
			if !hookCalled {
				t.Error("post-persist hook not called")
			}
			if _, exists := updated.Metadata["excluded-models"]; exists {
				t.Error("legacy alias retained")
			}
		})
	}
}
