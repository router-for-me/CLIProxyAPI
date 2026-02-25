package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestRunRooLoginWithRunner_Success(t *testing.T) {
	mockRunner := func(spec NativeCLISpec) (int, error) {
		if spec.Name != "roo" {
			t.Errorf("mockRunner got spec.Name = %q, want roo", spec.Name)
		}
		return 0, nil
	}
	var stdout, stderr bytes.Buffer
	code := RunRooLoginWithRunner(mockRunner, &stdout, &stderr)
	if code != 0 {
		t.Errorf("RunRooLoginWithRunner(success) = %d, want 0", code)
	}
	out := stdout.String()
	if !strings.Contains(out, "Roo authentication successful!") {
		t.Errorf("stdout missing success message: %q", out)
	}
	if !strings.Contains(out, "roo: block") {
		t.Errorf("stdout missing config hint: %q", out)
	}
	if stderr.Len() > 0 {
		t.Errorf("stderr should be empty on success, got: %q", stderr.String())
	}
}

func TestRunRooLoginWithRunner_CLINotFound(t *testing.T) {
	mockRunner := func(NativeCLISpec) (int, error) {
		return -1, errRooNotFound
	}
	var stdout, stderr bytes.Buffer
	code := RunRooLoginWithRunner(mockRunner, &stdout, &stderr)
	if code != 1 {
		t.Errorf("RunRooLoginWithRunner(not found) = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), rooInstallHint) {
		t.Errorf("stderr missing install hint: %q", stderr.String())
	}
}

var errRooNotFound = &mockErr{msg: "roo CLI not found"}

type mockErr struct{ msg string }

func (e *mockErr) Error() string { return e.msg }

func TestRunRooLoginWithRunner_CLIExitsNonZero(t *testing.T) {
	mockRunner := func(NativeCLISpec) (int, error) {
		return 42, nil // CLI exited with 42
	}
	var stdout, stderr bytes.Buffer
	code := RunRooLoginWithRunner(mockRunner, &stdout, &stderr)
	if code != 42 {
		t.Errorf("RunRooLoginWithRunner(exit 42) = %d, want 42", code)
	}
	if strings.Contains(stdout.String(), "Roo authentication successful!") {
		t.Errorf("should not print success when CLI exits non-zero")
	}
}

func TestRunKiloLoginWithRunner_Success(t *testing.T) {
	mockRunner := func(spec NativeCLISpec) (int, error) {
		if spec.Name != "kilo" {
			t.Errorf("mockRunner got spec.Name = %q, want kilo", spec.Name)
		}
		return 0, nil
	}
	var stdout, stderr bytes.Buffer
	code := RunKiloLoginWithRunner(mockRunner, &stdout, &stderr)
	if code != 0 {
		t.Errorf("RunKiloLoginWithRunner(success) = %d, want 0", code)
	}
	out := stdout.String()
	if !strings.Contains(out, "Kilo authentication successful!") {
		t.Errorf("stdout missing success message: %q", out)
	}
	if !strings.Contains(out, "kilo: block") {
		t.Errorf("stdout missing config hint: %q", out)
	}
}

func TestRunKiloLoginWithRunner_CLINotFound(t *testing.T) {
	mockRunner := func(NativeCLISpec) (int, error) {
		return -1, &mockErr{msg: "kilo CLI not found"}
	}
	var stdout, stderr bytes.Buffer
	code := RunKiloLoginWithRunner(mockRunner, &stdout, &stderr)
	if code != 1 {
		t.Errorf("RunKiloLoginWithRunner(not found) = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), kiloInstallHint) {
		t.Errorf("stderr missing install hint: %q", stderr.String())
	}
}

func TestDoRooLogin_DoesNotPanic(t *testing.T) {
	// DoRooLogin calls os.Exit, so we can't test it directly without subprocess.
	// Verify the function exists and accepts config.
	cfg := &config.Config{}
	opts := &LoginOptions{}
	// This would os.Exit - we just ensure it compiles and the signature is correct
	_ = cfg
	_ = opts
	// Run the testable helper instead
	code := RunRooLoginWithRunner(func(NativeCLISpec) (int, error) { return 0, nil }, nil, nil)
	if code != 0 {
		t.Errorf("RunRooLoginWithRunner = %d, want 0", code)
	}
}
