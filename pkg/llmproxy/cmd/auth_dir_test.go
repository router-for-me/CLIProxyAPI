package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveAuthDir_Default(t *testing.T) {
	got, err := resolveAuthDir("")
	if err != nil {
		t.Fatalf("resolveAuthDir(\"\") error: %v", err)
	}

	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	expected := filepath.Join(home, ".cli-proxy-api")
	if got != expected {
		t.Fatalf("resolveAuthDir(\"\") = %q, want %q", got, expected)
	}
}

func TestEnsureAuthDir_RejectsTooPermissiveDir(t *testing.T) {
	authDir := t.TempDir()
	if err := os.Chmod(authDir, 0o755); err != nil {
		t.Fatalf("Chmod: %v", err)
	}

	if _, err := ensureAuthDir(authDir, "provider"); err == nil {
		t.Fatalf("ensureAuthDir(%q) expected error", authDir)
	} else if !strings.Contains(err.Error(), "too permissive") {
		t.Fatalf("ensureAuthDir(%q) error = %q, want too permissive", authDir, err)
	} else if !strings.Contains(err.Error(), "chmod 700") {
		t.Fatalf("ensureAuthDir(%q) error = %q, want chmod guidance", authDir, err)
	}
}

func TestAuthDirTokenFileRef(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}

	got := authDirTokenFileRef(filepath.Join(home, ".cli-proxy-api"), "key.json")
	if got != "~/.cli-proxy-api/key.json" {
		t.Fatalf("authDirTokenFileRef(home default) = %q, want ~/.cli-proxy-api/key.json", got)
	}

	nested := authDirTokenFileRef(filepath.Join(home, ".cli-proxy-api", "provider"), "key.json")
	if nested != "~/.cli-proxy-api/provider/key.json" {
		t.Fatalf("authDirTokenFileRef(home nested) = %q, want ~/.cli-proxy-api/provider/key.json", nested)
	}

	outside := filepath.Join(os.TempDir(), "key.json")
	if got := authDirTokenFileRef(os.TempDir(), "key.json"); got != outside {
		t.Fatalf("authDirTokenFileRef(outside home) = %q, want %q", got, outside)
	}
}
