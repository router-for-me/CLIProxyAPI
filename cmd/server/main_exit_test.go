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
			os.Args = append([]string{os.Args[0]}, os.Args[index+1:]...)
			flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ExitOnError)
			main()
			return
		}
	}
	t.Fatal("missing server arguments")
}

func newServerExitCommand(t *testing.T, dir string, args ...string) (*exec.Cmd, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	cmdArgs := append([]string{"-test.run=^TestServerExitHelper$", "--"}, args...)
	command := exec.CommandContext(ctx, os.Args[0], cmdArgs...)
	command.Dir = dir
	// Isolate from host store environment variables and prevent external network calls.
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
	stdout := &bytes.Buffer{}
	stderr := &bytes.Buffer{}
	command.Stdout = stdout
	command.Stderr = stderr
	return command, stdout, stderr
}

func writeTestServerConfig(t *testing.T, dir string, port int) string {
	t.Helper()
	configPath := filepath.Join(dir, "config.yaml")
	content := fmt.Sprintf(
		"config-version: 8\nserver:\n  host: 127.0.0.1\n  port: %d\nmanagement:\n  secret-key: \"$2a$10$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy\"\n  disable-control-panel: true\noauth:\n  auth-dir: %q\naccess:\n  api-keys: [test-server-exit-key]\n",
		port,
		filepath.Join(dir, "auths"),
	)
	if errWrite := os.WriteFile(configPath, []byte(content), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	return configPath
}

func TestMissingConfigExitsWithFailure(t *testing.T) {
	dir := t.TempDir()
	missingPath := filepath.Join(dir, "missing.yaml")
	command, stdout, stderr := newServerExitCommand(t, dir, "--config", missingPath)
	errRun := command.Run()
	if command.ProcessState == nil || command.ProcessState.ExitCode() != 1 {
		t.Fatalf("expected exit code 1 for missing config, got err=%v exit=%d\nstdout:\n%s\nstderr:\n%s",
			errRun, command.ProcessState.ExitCode(), stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "failed to load config") {
		t.Fatalf("expected config load error on stderr, got:\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String(), "failed to load config") {
		t.Fatalf("did not expect config load error on stdout, got:\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	}
}

func TestMalformedConfigExitsWithFailure(t *testing.T) {
	dir := t.TempDir()
	invalidPath := filepath.Join(dir, "invalid.yaml")
	if errWrite := os.WriteFile(invalidPath, []byte("routing: [\n"), 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	command, stdout, stderr := newServerExitCommand(t, dir, "--config", invalidPath)
	errRun := command.Run()
	if command.ProcessState == nil || command.ProcessState.ExitCode() != 1 {
		t.Fatalf("expected exit code 1 for malformed config, got err=%v exit=%d\nstdout:\n%s\nstderr:\n%s",
			errRun, command.ProcessState.ExitCode(), stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "failed to load config") {
		t.Fatalf("expected config load error on stderr, got:\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String(), "failed to load config") || strings.Contains(stdout.String(), "failed to parse plugin bootstrap config") {
		t.Fatalf("did not expect config error/warn diagnostics on stdout, got:\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	}
}

func TestTUIClientFailureExitsWithFailure(t *testing.T) {
	dir := t.TempDir()
	configPath := writeTestServerConfig(t, dir, 18317)
	command, stdout, stderr := newServerExitCommand(t, dir, "--config", configPath, "--tui")
	errRun := command.Run()
	if command.ProcessState == nil || command.ProcessState.ExitCode() != 1 {
		t.Fatalf("expected exit code 1 for TUI failure, got err=%v exit=%d\nstdout:\n%s\nstderr:\n%s",
			errRun, command.ProcessState.ExitCode(), stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "TUI error:") {
		t.Fatalf("expected TUI error on stderr, got:\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	}
}

func TestTUIStandaloneEmbeddedServerNotReadyExitsWithFailure(t *testing.T) {
	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatal(errListen)
	}
	t.Cleanup(func() {
		_ = listener.Close()
	})
	go func() {
		for {
			conn, errAccept := listener.Accept()
			if errAccept != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	dir := t.TempDir()
	configPath := writeTestServerConfig(t, dir, listener.Addr().(*net.TCPAddr).Port)
	command, stdout, stderr := newServerExitCommand(t, dir, "--config", configPath, "--tui", "--standalone", "--local-model")
	errRun := command.Run()
	if command.ProcessState == nil || command.ProcessState.ExitCode() != 1 {
		t.Fatalf("expected exit code 1 when standalone embedded server is not ready, got err=%v exit=%d\nstdout:\n%s\nstderr:\n%s",
			errRun, command.ProcessState.ExitCode(), stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "TUI error: embedded server is not ready") {
		t.Fatalf("expected embedded server not ready error on stderr, got:\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	}
}

func TestTUIStandaloneFailureExitsWithFailure(t *testing.T) {
	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatal(errListen)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if errClose := listener.Close(); errClose != nil {
		t.Fatal(errClose)
	}

	dir := t.TempDir()
	configPath := writeTestServerConfig(t, dir, port)
	command, stdout, stderr := newServerExitCommand(t, dir, "--config", configPath, "--tui", "--standalone", "--local-model")
	errRun := command.Run()
	if command.ProcessState == nil || command.ProcessState.ExitCode() != 1 {
		t.Fatalf("expected exit code 1 for standalone TUI failure, got err=%v exit=%d\nstdout:\n%s\nstderr:\n%s",
			errRun, command.ProcessState.ExitCode(), stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "TUI error:") {
		t.Fatalf("expected TUI error on stderr, got:\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	}
}

func TestServerOccupiedPortExitsWithFailure(t *testing.T) {
	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatal(errListen)
	}
	t.Cleanup(func() {
		_ = listener.Close()
	})

	dir := t.TempDir()
	configPath := writeTestServerConfig(t, dir, listener.Addr().(*net.TCPAddr).Port)
	command, stdout, stderr := newServerExitCommand(t, dir, "--config", configPath, "--local-model")
	errRun := command.Run()
	if command.ProcessState == nil || command.ProcessState.ExitCode() != 1 {
		t.Fatalf("expected exit code 1 for occupied port, got err=%v exit=%d\nstdout:\n%s\nstderr:\n%s",
			errRun, command.ProcessState.ExitCode(), stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "failed to start HTTP server") {
		t.Fatalf("expected HTTP bind failure on stderr, got:\nstdout:\n%s\nstderr:\n%s", stdout.String(), stderr.String())
	}
}

func TestServerInterruptExitsSuccessfully(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("os.Interrupt cannot be sent to a process on Windows")
	}
	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatal(errListen)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if errClose := listener.Close(); errClose != nil {
		t.Fatal(errClose)
	}

	dir := t.TempDir()
	configPath := writeTestServerConfig(t, dir, port)
	command, stdout, stderr := newServerExitCommand(t, dir, "--config", configPath, "--local-model")
	if errStart := command.Start(); errStart != nil {
		t.Fatal(errStart)
	}
	t.Cleanup(func() {
		_ = command.Process.Kill()
	})

	done := make(chan error, 1)
	go func() {
		done <- command.Wait()
	}()

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
			t.Fatalf("server exited before listening: %v\nstdout:\n%s\nstderr:\n%s", errWait, stdout.String(), stderr.String())
		case <-deadline.C:
			t.Fatal("server did not start listening")
		case <-ticker.C:
		}
	}

	if errSignal := command.Process.Signal(os.Interrupt); errSignal != nil {
		t.Fatal(errSignal)
	}
	if errWait := <-done; errWait != nil {
		t.Fatalf("expected a successful interrupt shutdown, got %v\nstdout:\n%s\nstderr:\n%s", errWait, stdout.String(), stderr.String())
	}
}
