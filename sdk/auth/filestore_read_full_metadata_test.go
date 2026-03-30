package auth

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestFileTokenStoreList_ReturnsFullMetadata(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	authPath := filepath.Join(tempDir, "claude-full.json")
	metadata := map[string]any{
		"type":            "claude",
		"email":           "file@example.com",
		"access_token":    "token-abc",
		"refresh_token":   "refresh-abc",
		"custom_required": "must-stay",
	}
	raw, err := json.Marshal(metadata)
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	if err = os.WriteFile(authPath, raw, 0o600); err != nil {
		t.Fatalf("write metadata file: %v", err)
	}

	store := NewFileTokenStore()
	store.SetBaseDir(tempDir)

	items, err := store.List(context.Background())
	if err != nil {
		t.Fatalf("list auth files: %v", err)
	}
	if len(items) != 1 || items[0] == nil {
		t.Fatalf("expected one auth item, got %d", len(items))
	}
	auth := items[0]
	if token, _ := auth.Metadata["access_token"].(string); token != "token-abc" {
		t.Fatalf("expected access token in listed metadata, got %q", token)
	}
	if marker, _ := auth.Metadata["custom_required"].(string); marker != "must-stay" {
		t.Fatalf("expected full metadata marker, got %q", marker)
	}
	if auth.DeferredFileHydration() {
		t.Fatalf("expected filestore list auth to be fully hydrated")
	}
}
