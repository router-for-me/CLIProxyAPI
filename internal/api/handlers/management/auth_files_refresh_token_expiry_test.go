package management

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func registerClaudeListTestAuth(t *testing.T, metadata map[string]any) *Handler {
	t.Helper()
	t.Setenv("MANAGEMENT_PASSWORD", "")

	authDir := t.TempDir()
	fileName := "claude-user@example.com.json"
	filePath := filepath.Join(authDir, fileName)
	if errWrite := os.WriteFile(filePath, []byte(`{"type":"claude","email":"user@example.com"}`), 0o600); errWrite != nil {
		t.Fatalf("failed to write auth file: %v", errWrite)
	}
	manager := coreauth.NewManager(nil, nil, nil)
	record := &coreauth.Auth{
		ID:         fileName,
		FileName:   fileName,
		Provider:   "claude",
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"path": filePath},
		Metadata:   metadata,
	}
	if _, errRegister := manager.Register(context.Background(), record); errRegister != nil {
		t.Fatalf("failed to register auth record: %v", errRegister)
	}
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	h.tokenStore = &memoryAuthStore{}
	return h
}

func TestListAuthFiles_IncludesRefreshTokenExpiresAt(t *testing.T) {
	h := registerClaudeListTestAuth(t, map[string]any{
		"type":                     "claude",
		"email":                    "user@example.com",
		"refresh_token_expires_at": "2026-02-01T08:00:00+08:00",
	})

	entry := firstAuthFileEntry(t, h)
	if got, want := entry["refresh_token_expires_at"], "2026-02-01T00:00:00Z"; got != want {
		t.Fatalf("refresh_token_expires_at = %#v, want %q", got, want)
	}
}

func TestListAuthFiles_OmitsMissingOrInvalidRefreshTokenExpiresAt(t *testing.T) {
	for name, metadata := range map[string]map[string]any{
		"absent":  {"type": "claude", "email": "user@example.com"},
		"invalid": {"type": "claude", "email": "user@example.com", "refresh_token_expires_at": "next month"},
	} {
		t.Run(name, func(t *testing.T) {
			h := registerClaudeListTestAuth(t, metadata)
			entry := firstAuthFileEntry(t, h)
			if got, exists := entry["refresh_token_expires_at"]; exists {
				t.Fatalf("refresh_token_expires_at = %#v, want field omitted", got)
			}
		})
	}
}

func TestListAuthFilesFromDisk_IncludesRefreshTokenExpiresAt(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	authDir := t.TempDir()
	filePath := filepath.Join(authDir, "claude-user@example.com.json")
	content := `{"type":"claude","email":"user@example.com","refresh_token_expires_at":"2026-02-01T00:00:00Z"}`
	if errWrite := os.WriteFile(filePath, []byte(content), 0o600); errWrite != nil {
		t.Fatalf("failed to write auth file: %v", errWrite)
	}

	entry := firstAuthFileEntry(t, NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil))
	if got, want := entry["refresh_token_expires_at"], "2026-02-01T00:00:00Z"; got != want {
		t.Fatalf("refresh_token_expires_at = %#v, want %q", got, want)
	}
}
