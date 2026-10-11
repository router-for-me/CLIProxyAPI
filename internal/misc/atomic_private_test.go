package misc

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// This exercises real local filesystem failure, not injected fsync failures.
func TestAtomicWritePrivateRenameFailureCleansTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "existing-directory")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(target, "marker")
	if err := os.WriteFile(marker, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := AtomicWritePrivate(target, []byte("synthetic-new-credential")); err == nil {
		t.Fatal("rename over directory must fail")
	}
	got, err := os.ReadFile(marker)
	if err != nil || string(got) != "unchanged" {
		t.Fatalf("existing destination changed: %q, %v", got, err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "existing-directory" {
		t.Fatalf("temporary file leaked: %v", entries)
	}
}

func TestAtomicWritePrivateReplacementContentsAndMode(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "credential.json")
	if err := os.WriteFile(target, []byte("old"), 0644); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := AtomicWritePrivate(target, []byte("synthetic-replacement")); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "synthetic-replacement" {
		t.Fatalf("replacement contents: %q, %v", got, err)
	}
	after, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(before, after) {
		t.Fatal("destination inode was not replaced")
	}
	if runtime.GOOS != "windows" && after.Mode().Perm() != 0600 {
		t.Fatalf("mode=%o", after.Mode().Perm())
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("temporary file leaked: %v", entries)
	}
}
