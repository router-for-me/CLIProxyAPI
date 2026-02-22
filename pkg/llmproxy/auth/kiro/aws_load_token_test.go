package kiro

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadKiroIDEToken_FallbackLegacyPathAndSnakeCase(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	legacyPath := filepath.Join(home, ".kiro", "kiro-auth-token.json")
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0700); err != nil {
		t.Fatalf("mkdir legacy path: %v", err)
	}

	content := `{
		"access_token": "legacy-access",
		"refresh_token": "legacy-refresh",
		"expires_at": "2099-01-01T00:00:00Z",
		"auth_method": "IdC",
		"provider": "legacy",
		"client_id_hash": "hash-legacy"
	}`
	if err := os.WriteFile(legacyPath, []byte(content), 0600); err != nil {
		t.Fatalf("write legacy token: %v", err)
	}

	token, err := LoadKiroIDEToken()
	if err != nil {
		t.Fatalf("LoadKiroIDEToken failed: %v", err)
	}

	if token.AccessToken != "legacy-access" {
		t.Fatalf("access token mismatch: got %q", token.AccessToken)
	}
	if token.RefreshToken != "legacy-refresh" {
		t.Fatalf("refresh token mismatch: got %q", token.RefreshToken)
	}
	if token.AuthMethod != "idc" {
		t.Fatalf("auth method should be normalized: got %q", token.AuthMethod)
	}
}

func TestLoadKiroIDEToken_PrefersDefaultPathOverLegacy(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	defaultPath := filepath.Join(home, KiroIDETokenFile)
	legacyPath := filepath.Join(home, KiroIDETokenLegacyFile)
	for _, path := range []string{defaultPath, legacyPath} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatalf("mkdir %s: %v", path, err)
		}
	}

	if err := os.WriteFile(legacyPath, []byte(`{"accessToken":"legacy-access","refreshToken":"legacy-refresh","expiresAt":"2099-01-01T00:00:00Z"}`), 0600); err != nil {
		t.Fatalf("write legacy token: %v", err)
	}
	if err := os.WriteFile(defaultPath, []byte(`{"accessToken":"default-access","refreshToken":"default-refresh","expiresAt":"2099-01-01T00:00:00Z"}`), 0600); err != nil {
		t.Fatalf("write default token: %v", err)
	}

	token, err := LoadKiroIDEToken()
	if err != nil {
		t.Fatalf("LoadKiroIDEToken failed: %v", err)
	}
	if token.AccessToken != "default-access" {
		t.Fatalf("expected default path token, got %q", token.AccessToken)
	}
	if token.RefreshToken != "default-refresh" {
		t.Fatalf("expected default path refresh token, got %q", token.RefreshToken)
	}
}
