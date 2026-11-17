package auth

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestExistingRefreshTokenFindsStoredCredential(t *testing.T) {
	dir := t.TempDir()
	record := `{"type":"antigravity","email":"user@example.com","refresh_token":"stored-refresh"}`
	if err := os.WriteFile(filepath.Join(dir, "antigravity-user.json"), []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{AuthDir: dir}
	if got := existingRefreshToken(cfg, "user@example.com"); got != "stored-refresh" {
		t.Fatalf("existingRefreshToken() = %q, want the stored token", got)
	}
	if got := existingRefreshToken(cfg, "other@example.com"); got != "" {
		t.Fatalf("existingRefreshToken(other account) = %q, want empty", got)
	}
}

func TestExistingRefreshTokenWithoutStoredCredential(t *testing.T) {
	cfg := &config.Config{AuthDir: t.TempDir()}
	if got := existingRefreshToken(cfg, "user@example.com"); got != "" {
		t.Fatalf("existingRefreshToken() = %q, want empty for a fresh account", got)
	}
}
