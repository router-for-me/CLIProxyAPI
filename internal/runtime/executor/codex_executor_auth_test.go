package executor

import (
	"context"
	"net/http"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestCodexCredsOAuthIgnoresAPIKeyAttribute(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"api_key":   "fake-api-key",
			"base_url":  "https://chatgpt.com/backend-api/codex",
		},
		Metadata: map[string]any{
			"access_token": "real-oauth-token",
		},
	}

	token, baseURL := codexCreds(auth)
	if token != "real-oauth-token" {
		t.Fatalf("expected oauth access token, got %q", token)
	}
	if baseURL != "https://chatgpt.com/backend-api/codex" {
		t.Fatalf("unexpected baseURL: %q", baseURL)
	}
}

func TestCodexCredsOAuthMissingTokenDoesNotFallbackToAPIKey(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"api_key":   "fake-api-key",
		},
	}

	token, _ := codexCreds(auth)
	if token != "" {
		t.Fatalf("expected empty token for oauth without access_token, got %q", token)
	}
	if err := ensureCodexOAuthToken(auth, token); err == nil {
		t.Fatal("expected missing oauth token error")
	}
}

func TestCodexCredsAPIKeyAuthPrefersAPIKey(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"auth_kind": "apikey",
			"api_key":   "config-api-key",
		},
		Metadata: map[string]any{
			"access_token": "oauth-token",
		},
	}

	token, _ := codexCreds(auth)
	if token != "config-api-key" {
		t.Fatalf("expected config api_key, got %q", token)
	}
}

func TestApplyCodexHeadersOAuthAddsAccountHeaders(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"api_key":   "fake-api-key",
		},
		Metadata: map[string]any{
			"account_id": "acct_123",
		},
	}

	req, err := http.NewRequest(http.MethodPost, "https://example.com", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	applyCodexHeaders(req, auth, "real-oauth-token", true)

	if got := req.Header.Get("Originator"); got != "codex_cli_rs" {
		t.Fatalf("expected oauth Originator header, got %q", got)
	}
	if got := req.Header.Get("Chatgpt-Account-Id"); got != "acct_123" {
		t.Fatalf("expected oauth account header, got %q", got)
	}
}

func TestApplyCodexHeadersAPIKeySkipsAccountHeaders(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"auth_kind": "apikey",
			"api_key":   "config-api-key",
		},
		Metadata: map[string]any{
			"account_id": "acct_123",
		},
	}

	req, err := http.NewRequest(http.MethodPost, "https://example.com", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	applyCodexHeaders(req, auth, "config-api-key", true)

	if got := req.Header.Get("Originator"); got != "" {
		t.Fatalf("expected no Originator header for api key auth, got %q", got)
	}
	if got := req.Header.Get("Chatgpt-Account-Id"); got != "" {
		t.Fatalf("expected no account header for api key auth, got %q", got)
	}
}

func TestApplyCodexWebsocketHeadersOAuthAddsAccountHeaders(t *testing.T) {
	auth := &cliproxyauth.Auth{
		Attributes: map[string]string{
			"auth_kind": "oauth",
			"api_key":   "fake-api-key",
		},
		Metadata: map[string]any{
			"account_id": "acct_456",
		},
	}

	headers := applyCodexWebsocketHeaders(context.Background(), http.Header{}, auth, "real-oauth-token")
	if got := headers.Get("Originator"); got != "codex_cli_rs" {
		t.Fatalf("expected websocket Originator header, got %q", got)
	}
	if got := headers.Get("Chatgpt-Account-Id"); got != "acct_456" {
		t.Fatalf("expected websocket account header, got %q", got)
	}
}
