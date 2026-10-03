package misc

import (
	"os"
	"path/filepath"
	"testing"
)

// Run on Windows: both creation and replacement must succeed without attempting
// FlushFileBuffers on a read-only directory handle.
func TestAtomicWritePrivateWindowsCreateAndReplace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credential.json")
	for _, data := range []string{"initial", "replacement"} {
		ops := privateWriteOpsDefault()
		ops.openDir = func(string) (*os.File, error) {
			t.Fatal("Windows must not open a directory for fsync")
			return nil, nil
		}
		if err := atomicWritePrivate(path, []byte(data), ops); err != nil {
			t.Fatalf("write %q: %v", data, err)
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != data {
			t.Fatalf("contents=%q error=%v, want %q", got, err, data)
		}
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 1 {
			t.Fatalf("temporary file leaked: %v, %v", entries, err)
		}
	}
}
