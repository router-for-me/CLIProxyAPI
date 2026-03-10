package executor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v6/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestGitLabExecutorExecuteUsesChatEndpoint(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != gitLabChatEndpoint {
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`"chat response"`))
	}))
	defer srv.Close()

	exec := NewGitLabExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "gitlab",
		Metadata: map[string]any{
			"base_url":     srv.URL,
			"access_token": "oauth-access",
			"model_name":   "claude-sonnet-4-5",
		},
	}
	req := cliproxyexecutor.Request{
		Model:   "gitlab-duo",
		Payload: []byte(`{"model":"gitlab-duo","messages":[{"role":"user","content":"hello"}]}`),
	}

	resp, err := exec.Execute(context.Background(), auth, req, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got := gjson.GetBytes(resp.Payload, "choices.0.message.content").String(); got != "chat response" {
		t.Fatalf("expected chat response, got %q", got)
	}
	if got := gjson.GetBytes(resp.Payload, "model").String(); got != "claude-sonnet-4-5" {
		t.Fatalf("expected resolved model, got %q", got)
	}
}

func TestGitLabExecutorExecuteFallsBackToCodeSuggestions(t *testing.T) {
	chatCalls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case gitLabChatEndpoint:
			chatCalls++
			http.Error(w, "feature unavailable", http.StatusForbidden)
		case gitLabCodeSuggestionsEndpoint:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{{
					"text": "fallback response",
				}},
			})
		default:
			t.Fatalf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()

	exec := NewGitLabExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		Provider: "gitlab",
		Metadata: map[string]any{
			"base_url":              srv.URL,
			"personal_access_token": "glpat-token",
			"auth_method":           "pat",
		},
	}
	req := cliproxyexecutor.Request{
		Model:   "gitlab-duo",
		Payload: []byte(`{"model":"gitlab-duo","messages":[{"role":"user","content":"write code"}]}`),
	}

	resp, err := exec.Execute(context.Background(), auth, req, cliproxyexecutor.Options{
		SourceFormat: sdktranslator.FromString("openai"),
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if chatCalls != 1 {
		t.Fatalf("expected chat endpoint to be tried once, got %d", chatCalls)
	}
	if got := gjson.GetBytes(resp.Payload, "choices.0.message.content").String(); got != "fallback response" {
		t.Fatalf("expected fallback response, got %q", got)
	}
}

func TestGitLabExecutorRefreshUpdatesMetadata(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/token":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"access_token":  "oauth-refreshed",
				"refresh_token": "oauth-refresh",
				"token_type":    "Bearer",
				"scope":         "api read_user",
				"created_at":    1710000000,
				"expires_in":    3600,
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

	exec := NewGitLabExecutor(&config.Config{})
	auth := &cliproxyauth.Auth{
		ID:       "gitlab-auth.json",
		Provider: "gitlab",
		Metadata: map[string]any{
			"base_url":            srv.URL,
			"access_token":        "oauth-access",
			"refresh_token":       "oauth-refresh",
			"oauth_client_id":     "client-id",
			"oauth_client_secret": "client-secret",
			"auth_method":         "oauth",
			"oauth_expires_at":    "2000-01-01T00:00:00Z",
		},
	}

	updated, err := exec.Refresh(context.Background(), auth)
	if err != nil {
		t.Fatalf("Refresh() error = %v", err)
	}
	if got := updated.Metadata["access_token"]; got != "oauth-refreshed" {
		t.Fatalf("expected refreshed access token, got %#v", got)
	}
	if got := updated.Metadata["model_name"]; got != "claude-sonnet-4-5" {
		t.Fatalf("expected refreshed model metadata, got %#v", got)
	}
}
