//go:build linux || darwin || freebsd

package pluginstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestAuthCommandTimeoutKillsShellChildren(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	resolver := newAuthResolver(100 * time.Millisecond)
	auth := []AuthConfig{{
		Match:        "https://plugins.example/",
		Type:         AuthTypeBearer,
		TokenCommand: authCommandWithChildPID(pidFile),
	}}

	started := time.Now()
	errAuth := applyPluginStoreAuthContext(context.Background(), resolver, nil, auth, "https://plugins.example/a", RequestKindArtifact)
	elapsed := time.Since(started)
	if errAuth == nil || errAuth.Error() != "plugin store auth token-command timed out" {
		t.Fatalf("applyPluginStoreAuthContext() error = %v, want token-command timeout", errAuth)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("auth command elapsed = %v, want near timeout plus wait delay", elapsed)
	}
	assertAuthCommandChildGone(t, authCommandChildPID(t, pidFile))
}

func TestAuthCommandCallerCancellationKillsShellChildren(t *testing.T) {
	pidFile := filepath.Join(t.TempDir(), "child.pid")
	resolver := NewAuthResolver()
	auth := []AuthConfig{{
		Match:        "https://plugins.example/",
		Type:         AuthTypeBearer,
		TokenCommand: authCommandWithChildPID(pidFile),
	}}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	errResult := make(chan error, 1)
	started := time.Now()
	go func() {
		errResult <- applyPluginStoreAuthContext(ctx, resolver, nil, auth, "https://plugins.example/a", RequestKindArtifact)
	}()
	childPID := authCommandChildPID(t, pidFile)
	cancel()

	if errAuth := <-errResult; !errors.Is(errAuth, context.Canceled) {
		t.Fatalf("applyPluginStoreAuthContext() error = %v, want context.Canceled", errAuth)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("auth command elapsed = %v, want near cancellation plus wait delay", elapsed)
	}
	assertAuthCommandChildGone(t, childPID)
}

func authCommandWithChildPID(pidFile string) string {
	return fmt.Sprintf("sleep 2 & child=$!; printf '%%s' \"$child\" > %q; wait", pidFile)
}

func authCommandChildPID(t *testing.T, pidFile string) int {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		data, errRead := os.ReadFile(pidFile)
		if errRead == nil {
			pid, errParse := strconv.Atoi(string(data))
			if errParse != nil || pid <= 0 {
				t.Fatalf("child pid = %q, want positive integer", data)
			}
			t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
			return pid
		}
		if !errors.Is(errRead, os.ErrNotExist) {
			t.Fatalf("read child pid: %v", errRead)
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("child pid was not written")
	return 0
}

func assertAuthCommandChildGone(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		errKill := syscall.Kill(pid, 0)
		if errors.Is(errKill, syscall.ESRCH) {
			return
		}
		if errKill != nil {
			t.Fatalf("check child process: %v", errKill)
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("child process %d is still running", pid)
}
