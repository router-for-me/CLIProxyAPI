package claudemaster

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func nativeBinaryTestEnvironment(t *testing.T) (string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("synthetic native executables use POSIX shell")
	}
	homeDir := canonicalTestTempDir(t)
	binDir := filepath.Join(homeDir, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", homeDir)
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("PATH", binDir)
	return homeDir, binDir
}

func writeNativeTestExecutable(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	// Resolution deliberately does not execute the installed CLI or inspect its version.
	script := "#!/bin/sh\nprintf '%s\\n' 'current Claude Code'\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestResolveNativeBinaryResolvesCurrentLauncherSymlink(t *testing.T) {
	homeDir, binDir := nativeBinaryTestEnvironment(t)
	current := filepath.Join(homeDir, "versions", "current")
	writeNativeTestExecutable(t, current)
	launcher := filepath.Join(binDir, "claude")
	if err := os.Symlink(current, launcher); err != nil {
		t.Fatal(err)
	}
	got, err := resolveNativeBinary(t.Context())
	if err != nil || got != current {
		t.Fatalf("current executable not resolved: %v", err)
	}
	replacement := filepath.Join(homeDir, "versions", "replacement")
	writeNativeTestExecutable(t, replacement)
	if err := os.Remove(launcher); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(replacement, launcher); err != nil {
		t.Fatal(err)
	}
	if got != current {
		t.Fatal("changing the launcher changed the already resolved executable")
	}
}

func TestResolveNativeBinaryAcceptsInstalledVersionWithoutVersionProbe(t *testing.T) {
	homeDir, binDir := nativeBinaryTestEnvironment(t)
	path := filepath.Join(binDir, "claude")
	marker := filepath.Join(homeDir, "must-not-run")
	script := "#!/bin/sh\n: > '" + marker + "'\nexit 9\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	got, err := resolveNativeBinary(t.Context())
	if err != nil || got != path {
		t.Fatalf("installed executable was rejected: %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("native resolution executed the CLI")
	}
}

func TestResolveNativeBinaryRejectsMissingOrInvalidExecutable(t *testing.T) {
	homeDir, binDir := nativeBinaryTestEnvironment(t)
	got, err := resolveNativeBinary(t.Context())
	if err == nil || got != "" || strings.Contains(err.Error(), homeDir) {
		t.Fatal("missing executable did not fail with a private-path-free error")
	}
	path := filepath.Join(binDir, "claude")
	if err := os.WriteFile(path, []byte("not executable"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := resolveNativeBinary(t.Context()); err == nil || got != "" || strings.Contains(err.Error(), homeDir) {
		t.Fatal("non-executable file was accepted or leaked")
	}
}

func TestResolveNativeBinaryHonorsCancellation(t *testing.T) {
	_, binDir := nativeBinaryTestEnvironment(t)
	writeNativeTestExecutable(t, filepath.Join(binDir, "claude"))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	got, err := resolveNativeBinary(ctx)
	if err != context.Canceled || got != "" {
		t.Fatal("native selection ignored cancellation")
	}
	if _, err := resolveNativeBinary(nil); err == nil {
		t.Fatal("nil selection context was accepted")
	}
}
