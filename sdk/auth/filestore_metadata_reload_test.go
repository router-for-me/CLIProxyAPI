package auth

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// tokenStorageWithTokens mimics provider token storages (e.g. codex) that
// serialize OAuth tokens alongside the injected metadata.
type tokenStorageWithTokens struct {
	meta map[string]any
}

func (s *tokenStorageWithTokens) SetMetadata(meta map[string]any) { s.meta = meta }

func (s *tokenStorageWithTokens) SaveTokenToFile(authFilePath string) error {
	payload := map[string]any{
		"type":          "codex",
		"access_token":  "token-from-storage",
		"refresh_token": "refresh-from-storage",
	}
	for k, v := range s.meta {
		payload[k] = v
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return os.WriteFile(authFilePath, raw, 0o600)
}

func TestFileTokenStore_Save_ReloadsMetadataFromPersistedFile(t *testing.T) {
	ctx := context.Background()
	baseDir := t.TempDir()

	store := NewFileTokenStore()
	store.SetBaseDir(baseDir)

	auth := &cliproxyauth.Auth{
		ID:       "codex-user@example.com.json",
		Provider: "codex",
		FileName: "codex-user@example.com.json",
		Storage:  &tokenStorageWithTokens{},
		Metadata: map[string]any{"email": "user@example.com"},
	}

	if _, err := store.Save(ctx, auth); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	if got, _ := auth.Metadata["access_token"].(string); got != "token-from-storage" {
		t.Fatalf("auth.Metadata[access_token]=%q, want token persisted by storage", got)
	}
	if got, _ := auth.Metadata["email"].(string); got != "user@example.com" {
		t.Fatalf("auth.Metadata[email]=%q, want original metadata preserved", got)
	}

	path := filepath.Join(baseDir, "codex-user@example.com.json")
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected auth file at %s: %v", path, err)
	}
}
