package auth

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

type testTokenStorage struct {
	meta map[string]any
}

func (s *testTokenStorage) SetMetadata(meta map[string]any) { s.meta = meta }

func (s *testTokenStorage) SaveTokenToFile(authFilePath string) error {
	raw, err := json.Marshal(s.meta)
	if err != nil {
		return err
	}
	return os.WriteFile(authFilePath, raw, 0o600)
}

func TestFileTokenStore_Save_DisabledPersistsFlagForTokenStorage(t *testing.T) {
	ctx := context.Background()
	baseDir := t.TempDir()
	path := filepath.Join(baseDir, "disabled.json")

	if err := os.WriteFile(path, []byte(`{"type":"test","disabled":true}`), 0o600); err != nil {
		t.Fatalf("seed auth file: %v", err)
	}

	store := NewFileTokenStore()
	store.SetBaseDir(baseDir)
	storage := &testTokenStorage{}

	auth := &cliproxyauth.Auth{
		ID:       "disabled.json",
		Provider: "test",
		FileName: "disabled.json",
		Disabled: true,
		Storage:  storage,
		Metadata: map[string]any{"type": "test"},
	}

	if _, err := store.Save(ctx, auth); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read auth file: %v", err)
	}
	var meta map[string]any
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatalf("unmarshal auth file: %v", err)
	}
	if disabled, _ := meta["disabled"].(bool); !disabled {
		t.Fatalf("disabled=%v, want true (raw=%s)", meta["disabled"], string(raw))
	}
}

func TestFileTokenStore_Save_WritesMetadataVerbatim(t *testing.T) {
	ctx := context.Background()
	baseDir := t.TempDir()
	path := filepath.Join(baseDir, "codex.json")

	// The store must persist exactly the metadata it is handed. Field
	// preservation across a partial update is the caller's job (the handlers
	// merge on-disk metadata first), so the store does not re-read the file.
	seed := `{"type":"codex","access_token":"access","refresh_token":"refresh","email":"u@example.com","disabled":false}`
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatalf("seed auth file: %v", err)
	}

	store := NewFileTokenStore()
	store.SetBaseDir(baseDir)
	auth := &cliproxyauth.Auth{
		ID:       "codex.json",
		Provider: "codex",
		FileName: "codex.json",
		Disabled: true,
		Metadata: map[string]any{"name": "codex.json", "access_token": "access"},
	}

	if _, err := store.Save(ctx, auth); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read auth file: %v", err)
	}
	var meta map[string]any
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatalf("unmarshal auth file: %v", err)
	}
	// Provided keys are written; type is backfilled; disabled reflects the auth.
	if got := meta["type"]; got != "codex" {
		t.Fatalf("type=%#v, want codex (raw=%s)", got, string(raw))
	}
	if got := meta["access_token"]; got != "access" {
		t.Fatalf("access_token=%#v, want access (raw=%s)", got, string(raw))
	}
	if got := meta["disabled"]; got != true {
		t.Fatalf("disabled=%#v, want true (raw=%s)", got, string(raw))
	}
	// Disk-only keys not in the update are NOT resurrected from disk.
	if _, ok := meta["refresh_token"]; ok {
		t.Fatalf("refresh_token should not be resurrected from disk (raw=%s)", string(raw))
	}
	if _, ok := meta["email"]; ok {
		t.Fatalf("email should not be resurrected from disk (raw=%s)", string(raw))
	}
}

func TestFileTokenStore_Save_DropsKeyDeletedFromUpdate(t *testing.T) {
	ctx := context.Background()
	baseDir := t.TempDir()
	path := filepath.Join(baseDir, "codex.json")

	// Regression test: a key present on disk but intentionally omitted from the
	// update (e.g. clearing all custom headers) must not reappear after save.
	seed := `{"type":"codex","access_token":"access","headers":{"X-Test":"1"}}`
	if err := os.WriteFile(path, []byte(seed), 0o600); err != nil {
		t.Fatalf("seed auth file: %v", err)
	}

	store := NewFileTokenStore()
	store.SetBaseDir(baseDir)
	auth := &cliproxyauth.Auth{
		ID:       "codex.json",
		Provider: "codex",
		FileName: "codex.json",
		Metadata: map[string]any{"type": "codex", "access_token": "access"},
	}

	if _, err := store.Save(ctx, auth); err != nil {
		t.Fatalf("Save() error: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read auth file: %v", err)
	}
	var meta map[string]any
	if err := json.Unmarshal(raw, &meta); err != nil {
		t.Fatalf("unmarshal auth file: %v", err)
	}
	if _, ok := meta["headers"]; ok {
		t.Fatalf("deleted 'headers' was resurrected from disk (raw=%s)", string(raw))
	}
	if got := meta["access_token"]; got != "access" {
		t.Fatalf("access_token=%#v, want access (raw=%s)", got, string(raw))
	}
}
