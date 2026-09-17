package atomicfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteCreatesFileWithContents(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "config.yaml")

	if err := Write(target, []byte("port: 8317\n"), 0o600); err != nil {
		t.Fatalf("Write: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "port: 8317\n" {
		t.Fatalf("contents = %q", got)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v; want 0600", info.Mode().Perm())
	}
}

func TestWriteReplacesExistingAtomically(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "data.bin")

	if err := Write(target, []byte("v1"), 0o600); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := Write(target, []byte("v2-longer"), 0o640); err != nil {
		t.Fatalf("second write: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != "v2-longer" {
		t.Fatalf("contents = %q", got)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %v; want 0640", info.Mode().Perm())
	}
}

func TestWriteLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "out.txt")

	for i := 0; i < 3; i++ {
		if err := Write(target, []byte("payload"), 0o600); err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("dir has %d entries; want only the target. names=%v", len(entries), names)
	}
	if entries[0].Name() != "out.txt" {
		t.Fatalf("unexpected entry %q", entries[0].Name())
	}
}

func TestWriteEmptyPathRejected(t *testing.T) {
	if err := Write("", []byte("x"), 0o600); err == nil {
		t.Fatal("expected error for empty path")
	}
}

func TestWriteToMissingDirectoryFails(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does", "not", "exist", "out.txt")
	if err := Write(missing, []byte("x"), 0o600); err == nil {
		t.Fatal("expected error when target directory is missing")
	}
}
