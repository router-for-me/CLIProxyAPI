package auth

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFileTokenStoreDeleteResolvesNestedAndEnforcesBaseDir(t *testing.T) {
	t.Parallel()

	t.Run("nested relative id deletes file under baseDir", func(t *testing.T) {
		t.Parallel()

		baseDir := t.TempDir()
		nestedDir := filepath.Join(baseDir, "team-a")
		if errMkdir := os.MkdirAll(nestedDir, 0o700); errMkdir != nil {
			t.Fatalf("mkdir nested dir: %v", errMkdir)
		}
		targetPath := filepath.Join(nestedDir, "account.json")
		if errWrite := os.WriteFile(targetPath, []byte(`{"type":"codex"}`), 0o600); errWrite != nil {
			t.Fatalf("write nested auth file: %v", errWrite)
		}

		store := NewFileTokenStore()
		store.SetBaseDir(baseDir)

		// Verify List produces the nested relative ID and Delete removes the file on disk.
		listed, errList := store.List(context.Background())
		if errList != nil {
			t.Fatalf("List() error = %v", errList)
		}
		if len(listed) != 1 {
			t.Fatalf("List() len = %d, want 1", len(listed))
		}

		if errDelete := store.Delete(context.Background(), listed[0].ID); errDelete != nil {
			t.Fatalf("Delete(%q) error = %v", listed[0].ID, errDelete)
		}
		if _, errStat := os.Stat(targetPath); !os.IsNotExist(errStat) {
			t.Fatalf("expected %q to be deleted, stat err = %v", targetPath, errStat)
		}
	})

	t.Run("absolute path inside baseDir deletes file", func(t *testing.T) {
		t.Parallel()

		baseDir := t.TempDir()
		nestedDir := filepath.Join(baseDir, "team-b")
		if errMkdir := os.MkdirAll(nestedDir, 0o700); errMkdir != nil {
			t.Fatalf("mkdir nested dir: %v", errMkdir)
		}
		targetPath := filepath.Join(nestedDir, "account.json")
		if errWrite := os.WriteFile(targetPath, []byte(`{"type":"codex"}`), 0o600); errWrite != nil {
			t.Fatalf("write auth file: %v", errWrite)
		}

		store := NewFileTokenStore()
		store.SetBaseDir(baseDir)

		if errDelete := store.Delete(context.Background(), targetPath); errDelete != nil {
			t.Fatalf("Delete(%q) error = %v", targetPath, errDelete)
		}
		if _, errStat := os.Stat(targetPath); !os.IsNotExist(errStat) {
			t.Fatalf("expected %q to be deleted, stat err = %v", targetPath, errStat)
		}
	})

	t.Run("relative traversal outside baseDir is rejected", func(t *testing.T) {
		t.Parallel()

		parentDir := t.TempDir()
		baseDir := filepath.Join(parentDir, "auths")
		if errMkdir := os.MkdirAll(baseDir, 0o700); errMkdir != nil {
			t.Fatalf("mkdir base dir: %v", errMkdir)
		}
		outsidePath := filepath.Join(parentDir, "outside.json")
		if errWrite := os.WriteFile(outsidePath, []byte(`{"keep":true}`), 0o600); errWrite != nil {
			t.Fatalf("write outside file: %v", errWrite)
		}

		store := NewFileTokenStore()
		store.SetBaseDir(baseDir)

		errDelete := store.Delete(context.Background(), filepath.Join("..", "outside.json"))
		if errDelete == nil {
			t.Fatalf("Delete(../outside.json) = nil, want containment error")
		}
		if _, errStat := os.Stat(outsidePath); errStat != nil {
			t.Fatalf("outside file should not be deleted, stat err = %v", errStat)
		}
	})

	t.Run("absolute path outside baseDir is rejected", func(t *testing.T) {
		t.Parallel()

		parentDir := t.TempDir()
		baseDir := filepath.Join(parentDir, "auths")
		if errMkdir := os.MkdirAll(baseDir, 0o700); errMkdir != nil {
			t.Fatalf("mkdir base dir: %v", errMkdir)
		}
		outsidePath := filepath.Join(parentDir, "outside.json")
		if errWrite := os.WriteFile(outsidePath, []byte(`{"keep":true}`), 0o600); errWrite != nil {
			t.Fatalf("write outside file: %v", errWrite)
		}

		store := NewFileTokenStore()
		store.SetBaseDir(baseDir)

		errDelete := store.Delete(context.Background(), outsidePath)
		if errDelete == nil {
			t.Fatalf("Delete(%q) = nil, want containment error", outsidePath)
		}
		if _, errStat := os.Stat(outsidePath); errStat != nil {
			t.Fatalf("outside file should not be deleted, stat err = %v", errStat)
		}
	})
}
