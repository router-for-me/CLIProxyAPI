package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/config"
)

func TestGitLabAuthenticatorLoginPAT(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v4/user":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":       42,
				"username": "duo-user",
				"email":    "duo@example.com",
				"name":     "Duo User",
			})
		case "/api/v4/personal_access_tokens/self":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"id":     5,
				"name":   "CLIProxyAPI",
				"scopes": []string{"api"},
			})
		case "/api/v4/code_suggestions/direct_access":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"base_url":   "https://cloud.gitlab.example.com",
				"token":      "gateway-token",
				"expires_at": 1710003600,
				"headers":    map[string]string{"X-Gitlab-Realm": "saas"},
				"model_details": map[string]any{
					"model_provider": "anthropic",
					"model_name":     "claude-sonnet-4-5",
				},
			})
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()

	authenticator := NewGitLabAuthenticator()
	record, err := authenticator.Login(context.Background(), &config.Config{}, &LoginOptions{
		Metadata: map[string]string{
			"login_mode":            "pat",
			"base_url":              srv.URL,
			"personal_access_token": "glpat-test-token",
		},
	})
	if err != nil {
		t.Fatalf("Login() error = %v", err)
	}
	if record.Provider != "gitlab" {
		t.Fatalf("expected gitlab provider, got %q", record.Provider)
	}
	if got := record.Metadata["model_name"]; got != "claude-sonnet-4-5" {
		t.Fatalf("expected discovered model, got %#v", got)
	}
	if got := record.Metadata["auth_kind"]; got != "personal_access_token" {
		t.Fatalf("expected personal_access_token auth kind, got %#v", got)
	}
}
