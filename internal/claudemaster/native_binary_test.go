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
	homeDir := t.TempDir()
	binDir := filepath.Join(homeDir, "bin")
	if err := os.MkdirAll(binDir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", homeDir)
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("PATH", binDir)
	return homeDir, binDir
}

func writeNativeTestExecutable(t *testing.T, path, version string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n[ \"$1\" = \"--version\" ] || exit 1\nprintf '%s\\n' '" + version + " (Claude Code)'\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestResolveNativeBinaryResolvesReviewedLauncherSymlink(t *testing.T) {
	homeDir, binDir := nativeBinaryTestEnvironment(t)
	reviewed := filepath.Join(homeDir, "versions", NativeClaudeVersion)
	writeNativeTestExecutable(t, reviewed, NativeClaudeVersion)
	launcher := filepath.Join(binDir, "claude")
	if err := os.Symlink(reviewed, launcher); err != nil {
		t.Fatal(err)
	}
	got, err := resolveNativeBinary(t.Context())
	if err != nil || got != reviewed {
		t.Fatalf("reviewed executable not resolved: %v", err)
	}
	newer := filepath.Join(homeDir, "versions", "2.1.270")
	writeNativeTestExecutable(t, newer, "2.1.270")
	if err := os.Remove(launcher); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(newer, launcher); err != nil {
		t.Fatal(err)
	}
	if err := verifyNativeVersion(t.Context(), got); err != nil {
		t.Fatal("changing the launcher changed the resolved executable")
	}
}

func TestResolveNativeBinaryFallsBackWithoutChangingNewerLauncher(t *testing.T) {
	homeDir, binDir := nativeBinaryTestEnvironment(t)
	newer := filepath.Join(homeDir, ".local", "share", "claude", "versions", "2.1.270")
	writeNativeTestExecutable(t, newer, "2.1.270")
	reviewed := filepath.Join(filepath.Dir(newer), NativeClaudeVersion)
	writeNativeTestExecutable(t, reviewed, NativeClaudeVersion)
	launcher := filepath.Join(binDir, "claude")
	if err := os.Symlink(newer, launcher); err != nil {
		t.Fatal(err)
	}
	got, err := resolveNativeBinary(t.Context())
	if err != nil || got != reviewed {
		t.Fatalf("installed reviewed version not selected: %v", err)
	}
	unchanged, err := os.Readlink(launcher)
	if err != nil || unchanged != newer {
		t.Fatal("selection changed the user's global native launcher")
	}
}

func TestResolveNativeBinaryHonorsAbsoluteXDGDataHome(t *testing.T) {
	_, _ = nativeBinaryTestEnvironment(t)
	dataDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataDir)
	reviewed := filepath.Join(dataDir, "claude", "versions", NativeClaudeVersion)
	writeNativeTestExecutable(t, reviewed, NativeClaudeVersion)
	got, err := resolveNativeBinary(t.Context())
	if err != nil || got != reviewed {
		t.Fatalf("installed XDG version not selected: %v", err)
	}
}

func TestResolveNativeBinaryFailsClosedWithoutInstalledPin(t *testing.T) {
	homeDir, binDir := nativeBinaryTestEnvironment(t)
	writeNativeTestExecutable(t, filepath.Join(binDir, "claude"), "2.1.270")
	got, err := resolveNativeBinary(t.Context())
	if err == nil || got != "" || strings.Contains(err.Error(), homeDir) {
		t.Fatal("missing pin did not fail with a private-path-free error")
	}
	reviewed := filepath.Join(homeDir, ".local", "share", "claude", "versions", NativeClaudeVersion)
	writeNativeTestExecutable(t, reviewed, "2.1.270")
	if got, err := resolveNativeBinary(t.Context()); err == nil || got != "" {
		t.Fatal("versioned filename bypassed actual version verification")
	}
}

func TestResolveNativeBinaryRejectsRelativeXDGDataHome(t *testing.T) {
	_, _ = nativeBinaryTestEnvironment(t)
	t.Setenv("XDG_DATA_HOME", "PRIVATE-CANARY-relative")
	got, err := resolveNativeBinary(t.Context())
	if err == nil || got != "" || strings.Contains(err.Error(), "PRIVATE-CANARY") {
		t.Fatal("relative data directory was accepted or leaked")
	}
}

func TestResolveNativeBinaryHonorsCancellation(t *testing.T) {
	_, binDir := nativeBinaryTestEnvironment(t)
	marker := filepath.Join(binDir, "must-not-run")
	script := "#!/bin/sh\n: > '" + marker + "'\n"
	if err := os.WriteFile(filepath.Join(binDir, "claude"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	got, err := resolveNativeBinary(ctx)
	if err != context.Canceled || got != "" {
		t.Fatal("native selection ignored cancellation")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("canceled selection executed a native binary")
	}
	if _, err := resolveNativeBinary(nil); err == nil {
		t.Fatal("nil selection context was accepted")
	}
}
