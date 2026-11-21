package bundledplugins

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestInstallCopiesUpdatesAndKeepsUserChanges(t *testing.T) {
	source, plugins := t.TempDir(), t.TempDir()
	platform := filepath.Join(runtime.GOOS, runtime.GOARCH)
	bundled := filepath.Join(source, platform, "guard.so")
	target := filepath.Join(plugins, platform, "guard.so")
	writeFile(t, bundled, "v1")
	writeFile(t, filepath.Join(source, platform, "guard.h"), "header")

	if installed := mustInstall(t, source, plugins); len(installed) != 1 || readFile(t, target) != "v1" {
		t.Fatalf("first install = %v, target %q", installed, readFile(t, target))
	}
	if _, err := os.Stat(filepath.Join(plugins, platform, "guard.h")); !os.IsNotExist(err) {
		t.Fatal("non-library file was installed")
	}
	if installed := mustInstall(t, source, plugins); len(installed) != 0 {
		t.Fatalf("unchanged install = %v, want none", installed)
	}

	writeFile(t, bundled, "v2")
	if installed := mustInstall(t, source, plugins); len(installed) != 1 || readFile(t, target) != "v2" {
		t.Fatalf("update = %v, target %q", installed, readFile(t, target))
	}

	writeFile(t, target, "user build")
	writeFile(t, bundled, "v3")
	if installed := mustInstall(t, source, plugins); len(installed) != 0 || readFile(t, target) != "user build" {
		t.Fatalf("user change overwritten: %v, target %q", installed, readFile(t, target))
	}
}

func TestInstallWithoutBundledDirectoryIsNoop(t *testing.T) {
	if installed := mustInstall(t, filepath.Join(t.TempDir(), "missing"), t.TempDir()); len(installed) != 0 {
		t.Fatalf("installed = %v, want none", installed)
	}
}

func mustInstall(t *testing.T, source, plugins string) []string {
	t.Helper()
	installed, err := Install(source, plugins)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	return installed
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
