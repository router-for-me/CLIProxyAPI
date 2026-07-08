package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestObjectTokenStoreReadAuthFileCopiesBaseURL(t *testing.T) {
	root := t.TempDir()
	authDir := filepath.Join(root, "auths")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatalf("mkdir auth dir: %v", err)
	}
	path := filepath.Join(authDir, "claude.json")
	if err := os.WriteFile(path, []byte(`{"type":"claude","email":"claude@example.com","base-url":"http://127.0.0.1:18082"}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	store := &ObjectTokenStore{}
	auth, err := store.readAuthFile(path, authDir)
	if err != nil {
		t.Fatalf("readAuthFile() error = %v", err)
	}
	if got := auth.Attributes["base_url"]; got != "http://127.0.0.1:18082" {
		t.Fatalf("base_url attr = %q, want copied metadata base URL", got)
	}
}
