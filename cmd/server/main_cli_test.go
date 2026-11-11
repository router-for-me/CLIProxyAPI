package main

import (
	"bytes"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestArgvRequestsVersion(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{name: "long flag", args: []string{"--version"}, want: true},
		{name: "short flag", args: []string{"-version"}, want: true},
		{name: "subcommand", args: []string{"version"}, want: true},
		{name: "assigned true", args: []string{"--version=true"}, want: true},
		{name: "assigned false", args: []string{"--version=false"}, want: false},
		{name: "last flag wins", args: []string{"--version=false", "--version"}, want: true},
		{name: "last flag wins false", args: []string{"--version", "--version=false"}, want: false},
		{name: "after config", args: []string{"--config", "config.yaml", "--version"}, want: true},
		{name: "stops at terminator", args: []string{"--", "--version"}, want: false},
		{name: "not a flag", args: []string{"--config", "config.yaml"}, want: false},
		{name: "operand then version", args: []string{"not-a-command", "--version"}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := argvRequestsVersion(tt.args); got != tt.want {
				t.Fatalf("argvRequestsVersion(%v) = %t, want %t", tt.args, tt.want, got)
			}
		})
	}
}

func TestUnexpectedOperandsError(t *testing.T) {
	if err := unexpectedOperandsError(nil); err != nil {
		t.Fatalf("nil operands: %v", err)
	}
	if err := unexpectedOperandsError([]string{}); err != nil {
		t.Fatalf("empty operands: %v", err)
	}
	err := unexpectedOperandsError([]string{"not-a-subcommand"})
	if err == nil || !strings.Contains(err.Error(), "not-a-subcommand") {
		t.Fatalf("expected the leftover operand in the error, got %v", err)
	}
}

const serverCLIHelperEnv = "CLIPROXY_TEST_SERVER_CLI_HELPER"

func TestServerCLIHelper(t *testing.T) {
	if os.Getenv(serverCLIHelperEnv) != "1" {
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

func serverCLICommand(t *testing.T, args ...string) *exec.Cmd {
	t.Helper()
	ctxArgs := append([]string{"-test.run=^TestServerCLIHelper$", "--"}, args...)
	command := exec.Command(os.Args[0], ctxArgs...)
	command.Dir = t.TempDir()
	command.Env = append(os.Environ(),
		serverCLIHelperEnv+"=1",
		"HOME_JWT=", "home_jwt=",
		"PGSTORE_DSN=", "pgstore_dsn=",
		"GITSTORE_GIT_URL=", "gitstore_git_url=",
		"OBJECTSTORE_ENDPOINT=", "objectstore_endpoint=",
		"DEPLOY=", "WRITABLE_PATH=", "writable_path=",
		"HTTP_PROXY=http://127.0.0.1:1", "HTTPS_PROXY=http://127.0.0.1:1",
		"NO_PROXY=127.0.0.1,localhost",
	)
	return command
}

func TestVersionFlagExitsSuccessfully(t *testing.T) {
	for _, args := range [][]string{{"--version"}, {"-version"}, {"version"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			command := serverCLICommand(t, args...)
			var stdout, stderr bytes.Buffer
			command.Stdout = &stdout
			command.Stderr = &stderr
			err := command.Run()
			if command.ProcessState == nil || command.ProcessState.ExitCode() != 0 {
				t.Fatalf("expected exit 0, got %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
			}
			if !strings.Contains(stdout.String(), "CLIProxyAPI Version:") {
				t.Fatalf("expected version banner on stdout, got %q", stdout.String())
			}
		})
	}
}

func TestUnexpectedOperandExitsWithUsage(t *testing.T) {
	command := serverCLICommand(t, "--config", filepath.Join(t.TempDir(), "missing.yaml"), "not-a-subcommand")
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if command.ProcessState == nil || command.ProcessState.ExitCode() != 2 {
		t.Fatalf("expected exit 2, got %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "not-a-subcommand") {
		t.Fatalf("expected leftover operand in stderr, got %q", stderr.String())
	}
}

func TestDiscoverUnexpectedOperandExitsWithUsage(t *testing.T) {
	command := serverCLICommand(t, "discover", "--json", "stray")
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if command.ProcessState == nil || command.ProcessState.ExitCode() != 2 {
		t.Fatalf("expected exit 2, got %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "stray") {
		t.Fatalf("expected leftover operand in stderr, got %q", stderr.String())
	}
}

func TestMissingConfigExitsWithFailure(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.yaml")
	command := serverCLICommand(t, "--config", missing)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	if command.ProcessState == nil || command.ProcessState.ExitCode() != 1 {
		t.Fatalf("expected exit 1, got %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "failed to load config") {
		t.Fatalf("expected config error on stderr, got %q (stdout=%q)", stderr.String(), stdout.String())
	}
}
