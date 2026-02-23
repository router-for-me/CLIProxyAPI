package auth

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFileTokenStoreResolveDeletePathRejectsEscapeInputs(t *testing.T) {
	t.Parallel()

	store := NewFileTokenStore()
	store.SetBaseDir(t.TempDir())

	absolute := filepath.Join(t.TempDir(), "outside.json")
	cases := []string{
		"../outside.json",
<<<<<<< HEAD
		"..\\outside.json",
		"..//..%2foutside.json",
=======
>>>>>>> archive/pr-234-head-20260223
		absolute,
	}
	for _, id := range cases {
		if _, err := store.resolveDeletePath(id); err == nil {
			t.Fatalf("expected id %q to be rejected", id)
		}
	}
}

func TestFileTokenStoreDeleteRemovesFileWithinBaseDir(t *testing.T) {
	t.Parallel()

	baseDir := t.TempDir()
	store := NewFileTokenStore()
	store.SetBaseDir(baseDir)

	target := filepath.Join(baseDir, "nested", "auth.json")
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		t.Fatalf("create nested dir: %v", err)
	}
	if err := os.WriteFile(target, []byte(`{"ok":true}`), 0o600); err != nil {
		t.Fatalf("write target file: %v", err)
	}

	if err := store.Delete(context.Background(), "nested/auth.json"); err != nil {
		t.Fatalf("delete auth file: %v", err)
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("expected target to be deleted, stat err=%v", err)
	}
}
