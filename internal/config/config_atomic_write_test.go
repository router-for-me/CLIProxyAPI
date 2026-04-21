package config

import (
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"testing"
)

func TestWriteConfigFileAtomicallyFallsBackToInPlaceWriteOnEBUSY(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configFile := filepath.Join(dir, "config.yaml")
	originalMode := os.FileMode(0o640)
	if err := os.WriteFile(configFile, []byte("old: true\n"), originalMode); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	previousRename := renameFile
	renameFile = func(_, _ string) error {
		return syscall.EBUSY
	}
	t.Cleanup(func() {
		renameFile = previousRename
	})

	if err := writeConfigFileAtomically(configFile, []byte("new: true\n")); err != nil {
		t.Fatalf("writeConfigFileAtomically returned error: %v", err)
	}

	got, err := os.ReadFile(configFile)
	if err != nil {
		t.Fatalf("read updated config: %v", err)
	}
	if string(got) != "new: true\n" {
		t.Fatalf("unexpected config contents: %q", string(got))
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(configFile)
		if err != nil {
			t.Fatalf("stat updated config: %v", err)
		}
		if info.Mode().Perm() != originalMode {
			t.Fatalf("expected mode %v, got %v", originalMode, info.Mode().Perm())
		}
	}

	leftovers, err := filepath.Glob(filepath.Join(dir, ".config-*.tmp"))
	if err != nil {
		t.Fatalf("glob temp files: %v", err)
	}
	if len(leftovers) != 0 {
		t.Fatalf("expected temp files to be cleaned up, found %v", leftovers)
	}
}
