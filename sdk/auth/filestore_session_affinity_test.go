package auth

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFileTokenStoreIgnoresSessionAffinitySnapshot(t *testing.T) {
	baseDir := t.TempDir()
	runtimeDir := filepath.Join(baseDir, ".runtime")
	if errMkdir := os.MkdirAll(runtimeDir, 0o700); errMkdir != nil {
		t.Fatal(errMkdir)
	}
	snapshotPath := filepath.Join(runtimeDir, "session-affinity.cache")
	if errWrite := os.WriteFile(snapshotPath, []byte(`{"version":1,"groups":[]}`), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	if errWrite := os.WriteFile(filepath.Join(baseDir, "codex.json"), []byte(`{"type":"codex"}`), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}

	store := NewFileTokenStore()
	store.SetBaseDir(baseDir)
	auths, errList := store.List(context.Background())
	if errList != nil {
		t.Fatal(errList)
	}
	if len(auths) != 1 || auths[0].ID != "codex.json" {
		t.Fatalf("listed auths = %#v, want only codex.json", auths)
	}
}
