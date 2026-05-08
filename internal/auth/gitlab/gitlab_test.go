package gitlab

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestAuthClientGenerateAuthURLIncludesPKCE(t *testing.T) {
	client := NewAuthClient(nil)
	pkce, err := GeneratePKCECodes()
	if err != nil {
		t.Fatalf("GeneratePKCECodes() error = %v", err)
	}

	rawURL, err := client.GenerateAuthURL("https://gitlab.example.com", "client-id", RedirectURL(17171), "state-123", pkce)
	if err != nil {
		t.Fatalf("GenerateAuthURL() error = %v", err)
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("Parse(authURL) error = %v", err)
	}
	if got := parsed.Path; got != "/oauth/authorize" {
		t.Fatalf("expected /oauth/authorize path, got %q", got)
	}
	query := parsed.Query()
	if got := query.Get("client_id"); got != "client-id" {
		t.Fatalf("expected client_id, got %q", got)
	}
	if got := query.Get("scope"); got != defaultOAuthScope {
		t.Fatalf("expected scope %q, got %q", defaultOAuthScope, got)
	}
	if got := query.Get("code_challenge_method"); got != "S256" {
		t.Fatalf("expected PKCE method S256, got %q", got)
	}
	if got := query.Get("code_challenge"); got == "" {
		t.Fatal("expected non-empty code_challenge")
	}
}

func TestAuthClientExchangeCodeForTokens(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/oauth/token" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm() error = %v", err)
		}
		if got := r.Form.Get("grant_type"); got != "authorization_code" {
			t.Fatalf("expected authorization_code grant, got %q", got)
		}
		if got := r.Form.Get("code_verifier"); got != "verifier-123" {
			t.Fatalf("expected code_verifier, got %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token":  "oauth-access",
			"refresh_token": "oauth-refresh",
			"token_type":    "Bearer",
			"scope":         "api read_user",
			"created_at":    1710000000,
			"expires_in":    3600,
		})
	}))
	defer srv.Close()

	client := NewAuthClient(nil)
	token, err := client.ExchangeCodeForTokens(context.Background(), srv.URL, "client-id", "client-secret", RedirectURL(17171), "auth-code", "verifier-123")
	if err != nil {
		t.Fatalf("ExchangeCodeForTokens() error = %v", err)
	}
	if token.AccessToken != "oauth-access" {
		t.Fatalf("expected access token, got %q", token.AccessToken)
	}
	if token.RefreshToken != "oauth-refresh" {
		t.Fatalf("expected refresh token, got %q", token.RefreshToken)
	}
}

func TestExtractDiscoveredModels(t *testing.T) {
	models := ExtractDiscoveredModels(map[string]any{
		"model_details": map[string]any{
			"model_provider": "anthropic",
			"model_name":     "claude-sonnet-4-5",
		},
		"supported_models": []any{
			map[string]any{"model_provider": "openai", "model_name": "gpt-4.1"},
			"claude-sonnet-4-5",
		},
	})
	if len(models) != 2 {
		t.Fatalf("expected 2 unique models, got %d", len(models))
	}
	if models[0].ModelName != "claude-sonnet-4-5" {
		t.Fatalf("unexpected first model %q", models[0].ModelName)
	}
	if models[1].ModelName != "gpt-4.1" {
		t.Fatalf("unexpected second model %q", models[1].ModelName)
	}
}

func TestFetchDirectAccessDecodesModelDetails(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v4/code_suggestions/direct_access" {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); !strings.Contains(got, "token-123") {
			t.Fatalf("expected bearer token, got %q", got)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"base_url":   "https://cloud.gitlab.example.com",
			"token":      "gateway-token",
			"expires_at": 1710003600,
			"headers": map[string]string{
				"X-Gitlab-Realm": "saas",
			},
			"model_details": map[string]any{
				"model_provider": "anthropic",
				"model_name":     "claude-sonnet-4-5",
			},
		})
	}))
	defer srv.Close()

	client := NewAuthClient(nil)
	direct, err := client.FetchDirectAccess(context.Background(), srv.URL, "token-123")
	if err != nil {
		t.Fatalf("FetchDirectAccess() error = %v", err)
	}
	if direct.ModelDetails == nil || direct.ModelDetails.ModelName != "claude-sonnet-4-5" {
		t.Fatalf("expected model details, got %+v", direct.ModelDetails)
	}
}
