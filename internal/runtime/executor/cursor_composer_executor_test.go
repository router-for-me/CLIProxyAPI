package executor

import (
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestCursorComposerCredentialsDefaults(t *testing.T) {
	t.Setenv("CURSOR_BACKEND_BASE_URL", "")
	t.Setenv("CURSOR_CHAT_ENDPOINT", "")
	t.Setenv("CURSOR_CLIENT_VERSION", "")

	_, _, backend, endpoint, version := cursorComposerCredentials(&cliproxyauth.Auth{
		Attributes: map[string]string{"api_key": "crsr_test"},
	})
	if backend == "" {
		t.Fatal("expected default backend base URL")
	}
	if endpoint == "" {
		t.Fatal("expected default chat endpoint")
	}
	if version == "" {
		t.Fatal("expected default client version")
	}
}

func TestCursorComposerCredentialsAttrOverride(t *testing.T) {
	t.Setenv("CURSOR_BACKEND_BASE_URL", "https://env.example")
	_, _, backend, endpoint, version := cursorComposerCredentials(&cliproxyauth.Auth{
		Attributes: map[string]string{
			"api_key":          "crsr_test",
			"backend_base_url": "https://cfg.example",
			"chat_endpoint":    "custom.Endpoint/Run",
			"client_version":   "9.9.9",
		},
	})
	if backend != "https://cfg.example" {
		t.Fatalf("backend = %q, want https://cfg.example", backend)
	}
	if endpoint != "custom.Endpoint/Run" {
		t.Fatalf("endpoint = %q, want custom.Endpoint/Run", endpoint)
	}
	if version != "9.9.9" {
		t.Fatalf("version = %q, want 9.9.9", version)
	}
}

func TestCursorSessionIDUsesAccessTokenHash(t *testing.T) {
	got := cursorSessionID("access-token-example")
	wantPrefix := sha256Hex("access-token-example")[:8]
	if len(got) < 8 || got[:8] != wantPrefix {
		t.Fatalf("cursorSessionID() = %q, want prefix %q", got, wantPrefix)
	}
	if got == stableUUID("", "access-token-example") {
		t.Fatal("cursorSessionID must not use stableUUID with empty namespace")
	}
}
