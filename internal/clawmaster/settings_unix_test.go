//go:build !windows

// Unix only: the fake git is a shell script.

package clawmaster

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNativeProjectRootsCancelsRunningGit(t *testing.T) {
	dir := canonicalTestTempDir(t)
	gitPath := filepath.Join(dir, "git")
	const helper = "#!/bin/sh\nexec \"$CLAW_MASTER_GIT_TEST_BINARY\" -test.run=^TestGitDiscoveryCancellationHelper$\n"
	if err := os.WriteFile(gitPath, []byte(helper), 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CLAW_MASTER_GIT_TEST_BINARY", os.Args[0])
	t.Setenv("CLAW_MASTER_GIT_TEST_HELPER", "1")
	t.Setenv("CLAW_MASTER_GIT_TEST_LISTENER", listener.Addr().String())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := nativeProjectRoots(ctx, dir)
		result <- err
	}()
	ready := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			ready <- err
			return
		}
		defer func() { _ = conn.Close() }()
		_, err = io.ReadFull(conn, make([]byte, 1))
		ready <- err
		// Keep the child blocked until context cancellation closes its socket.
		_, _ = io.Copy(io.Discard, conn)
	}()
	select {
	case err := <-ready:
		if err != nil {
			t.Fatal(err)
		}
	case err := <-result:
		t.Fatalf("Git helper exited before the cancellation test: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("Git helper did not acknowledge startup")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled Git was treated as a successful/non-Git fallback: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("cancelled Git process did not stop")
	}
}
