package store

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGitTokenStoreReadAuthFileIgnoresNonAuthJSON(t *testing.T) {
	tempDir := t.TempDir()
	path := filepath.Join(tempDir, "usage-statistics.json")
	if err := os.WriteFile(path, []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	store := &GitTokenStore{}
	auth, err := store.readAuthFile(path, tempDir)
	if err != nil {
		t.Fatalf("readAuthFile() error = %v", err)
	}
	if auth != nil {
		t.Fatalf("readAuthFile() = %#v, want nil", auth)
	}
}

func TestObjectTokenStoreReadAuthFileIgnoresNonAuthJSON(t *testing.T) {
	tempDir := t.TempDir()
	path := filepath.Join(tempDir, "usage-statistics.json")
	if err := os.WriteFile(path, []byte(`{"version":1}`), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	store := &ObjectTokenStore{}
	auth, err := store.readAuthFile(path, tempDir)
	if err != nil {
		t.Fatalf("readAuthFile() error = %v", err)
	}
	if auth != nil {
		t.Fatalf("readAuthFile() = %#v, want nil", auth)
	}
}
