package cmd

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestGetAuthFilePath_UsesDefaultAuthDirAndFallbackName(t *testing.T) {
	path := getAuthFilePath(nil, "iflow", "")
	if filepath.Dir(path) != "." {
		t.Fatalf("unexpected auth path prefix: %q", path)
	}
	base := filepath.Base(path)
	if !strings.HasPrefix(base, "iflow-account-") {
		t.Fatalf("fallback filename should use account marker, got %q", base)
	}

	path = getAuthFilePath(&config.Config{}, "iflow", "user@example.com")
	base = filepath.Base(path)
	if !strings.HasPrefix(base, "iflow-user@example.com-") {
		t.Fatalf("filename should include sanitized email, got %q", base)
	}

	path = getAuthFilePath(&config.Config{AuthDir: "/tmp/auth"}, "iflow", "user@example.com")
	dir := filepath.Dir(path)
	if dir != "/tmp/auth" {
		t.Fatalf("auth dir should respect cfg.AuthDir; got %q", dir)
	}
}
