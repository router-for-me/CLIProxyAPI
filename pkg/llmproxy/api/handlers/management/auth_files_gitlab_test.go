package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestRequestGitLabPATToken_SavesAuthRecord(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer glpat-test-token" {
			t.Fatalf("authorization header = %q, want Bearer glpat-test-token", got)
		}

		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v4/user":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":       42,
				"username": "gitlab-user",
				"name":     "GitLab User",
				"email":    "gitlab@example.com",
			})
		case "/api/v4/personal_access_tokens/self":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":      7,
				"name":    "management-center",
				"scopes":  []string{"api", "read_user"},
				"user_id": 42,
			})
		case "/api/v4/code_suggestions/direct_access":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"base_url":   "https://cloud.gitlab.example.com",
				"token":      "gateway-token",
				"expires_at": 1893456000,
				"headers": map[string]string{
					"X-Gitlab-Realm": "saas",
				},
				"model_details": map[string]any{
					"model_provider": "anthropic",
					"model_name":     "claude-sonnet-4-5",
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()

	store := &memoryAuthStore{}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, coreauth.NewManager(nil, nil, nil))
	h.tokenStore = store

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/gitlab-auth-url", strings.NewReader(`{"base_url":"`+upstream.URL+`","personal_access_token":"glpat-test-token"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.RequestGitLabPATToken(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if got := resp["status"]; got != "ok" {
		t.Fatalf("status = %#v, want ok", got)
	}
	if got := resp["model_provider"]; got != "anthropic" {
		t.Fatalf("model_provider = %#v, want anthropic", got)
	}
	if got := resp["model_name"]; got != "claude-sonnet-4-5" {
		t.Fatalf("model_name = %#v, want claude-sonnet-4-5", got)
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.items) != 1 {
		t.Fatalf("expected 1 saved auth record, got %d", len(store.items))
	}
	var saved *coreauth.Auth
	for _, item := range store.items {
		saved = item
	}
	if saved == nil {
		t.Fatal("expected saved auth record")
	}
	if saved.Provider != "gitlab" {
		t.Fatalf("provider = %q, want gitlab", saved.Provider)
	}
	if got := saved.Metadata["auth_kind"]; got != "personal_access_token" {
		t.Fatalf("auth_kind = %#v, want personal_access_token", got)
	}
	if got := saved.Metadata["model_provider"]; got != "anthropic" {
		t.Fatalf("saved model_provider = %#v, want anthropic", got)
	}
	if got := saved.Metadata["duo_gateway_token"]; got != "gateway-token" {
		t.Fatalf("saved duo_gateway_token = %#v, want gateway-token", got)
	}
}

func TestPostOAuthCallback_GitLabWritesPendingCallbackFile(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	authDir := t.TempDir()
	state := "gitlab-state-123"
	RegisterOAuthSession(state, "gitlab")
	t.Cleanup(func() { CompleteOAuthSession(state) })

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, coreauth.NewManager(nil, nil, nil))

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/oauth-callback", strings.NewReader(`{"provider":"gitlab","redirect_url":"http://localhost:17171/auth/callback?code=test-code&state=`+state+`"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.PostOAuthCallback(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	filePath := filepath.Join(authDir, ".oauth-gitlab-"+state+".oauth")
	data, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("read callback file: %v", err)
	}

	var payload map[string]string
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("decode callback payload: %v", err)
	}
	if got := payload["code"]; got != "test-code" {
		t.Fatalf("callback code = %q, want test-code", got)
	}
	if got := payload["state"]; got != state {
		t.Fatalf("callback state = %q, want %q", got, state)
	}
}

func TestNormalizeOAuthProvider_GitLab(t *testing.T) {
	provider, err := NormalizeOAuthProvider("gitlab")
	if err != nil {
		t.Fatalf("NormalizeOAuthProvider returned error: %v", err)
	}
	if provider != "gitlab" {
		t.Fatalf("provider = %q, want gitlab", provider)
	}
}
