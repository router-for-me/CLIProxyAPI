package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

const serverExitHelperEnv = "CLIPROXY_TEST_SERVER_EXIT_HELPER"

func TestServerExitHelper(t *testing.T) {
	if os.Getenv(serverExitHelperEnv) != "1" {
		return
	}
	for index, arg := range os.Args {
		if arg == "--" {
			os.Args = append(os.Args[:1], os.Args[index+1:]...)
			flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ExitOnError)
			main()
			return
		}
	}
	t.Fatal("missing server arguments")
}

func serverExitCommand(t *testing.T, port int) (*exec.Cmd, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	config := fmt.Sprintf("config-version: 8\nserver:\n  host: 127.0.0.1\n  port: %d\nmanagement:\n  disable-control-panel: true\noauth:\n  auth-dir: %q\naccess:\n  api-keys: [test-server-exit-key]\n", port, filepath.Join(dir, "auths"))
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestServerExitHelper$", "--", "--config", configPath, "--local-model")
	command.Dir = dir
	// Do not inherit remote credential stores or allow background version probes onto the network.
	command.Env = append(os.Environ(),
		serverExitHelperEnv+"=1",
		"HOME_JWT=", "home_jwt=",
		"PGSTORE_DSN=", "pgstore_dsn=",
		"GITSTORE_GIT_URL=", "gitstore_git_url=",
		"OBJECTSTORE_ENDPOINT=", "objectstore_endpoint=",
		"DEPLOY=", "WRITABLE_PATH=", "writable_path=",
		"HTTP_PROXY=http://127.0.0.1:1", "HTTPS_PROXY=http://127.0.0.1:1",
		"NO_PROXY=127.0.0.1,localhost",
	)
	output := &bytes.Buffer{}
	command.Stdout = output
	command.Stderr = output
	return command, output
}

func TestServerOccupiedPortExitsWithFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	command, output := serverExitCommand(t, listener.Addr().(*net.TCPAddr).Port)
	err = command.Run()
	if command.ProcessState == nil || command.ProcessState.ExitCode() != 1 {
		t.Fatalf("expected exit code 1 for an occupied port, got %v; output:\n%s", err, output.String())
	}
	if !strings.Contains(output.String(), "failed to start HTTP server") {
		t.Fatalf("expected an HTTP bind failure, output:\n%s", output.String())
	}
}

func TestServerInterruptExitsSuccessfully(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.Interrupt cannot be sent to a process on Windows")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	command, output := serverExitCommand(t, port)
	if errStart := command.Start(); errStart != nil {
		t.Fatal(errStart)
	}
	t.Cleanup(func() { _ = command.Process.Kill() })
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		connection, errDial := net.DialTimeout("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)), 100*time.Millisecond)
		if errDial == nil {
			_ = connection.Close()
			break
		}
		select {
		case errWait := <-done:
			t.Fatalf("server exited before listening: %v; output:\n%s", errWait, output.String())
		case <-deadline.C:
			t.Fatal("server did not start listening")
		case <-ticker.C:
		}
	}
	if errSignal := command.Process.Signal(os.Interrupt); errSignal != nil {
		t.Fatal(errSignal)
	}
	if errWait := <-done; errWait != nil {
		t.Fatalf("expected a successful interrupt shutdown, got %v; output:\n%s", errWait, output.String())
	}
}
