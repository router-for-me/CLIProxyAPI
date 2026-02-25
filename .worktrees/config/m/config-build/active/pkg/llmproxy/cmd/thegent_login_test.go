package cmd

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunThegentLoginWithRunner_Success(t *testing.T) {
	mockRunner := func(spec NativeCLISpec) (int, error) {
		if spec.Name != "thegent" {
			t.Errorf("mockRunner got spec.Name = %q, want thegent", spec.Name)
		}
		if len(spec.Args) != 3 || spec.Args[0] != "cliproxy" || spec.Args[1] != "login" || spec.Args[2] != "codex" {
			t.Errorf("mockRunner got spec.Args = %v, want [cliproxy login codex]", spec.Args)
		}
		return 0, nil
	}
	var stdout, stderr bytes.Buffer
	code := RunThegentLoginWithRunner(mockRunner, &stdout, &stderr, "codex")
	if code != 0 {
		t.Errorf("RunThegentLoginWithRunner(success) = %d, want 0", code)
	}
	if !strings.Contains(stdout.String(), "TheGent authentication successful") {
		t.Errorf("stdout missing success message: %q", stdout.String())
	}
	if stderr.Len() > 0 {
		t.Errorf("stderr should be empty on success, got: %q", stderr.String())
	}
}

func TestRunThegentLoginWithRunner_EmptyProvider(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := RunThegentLoginWithRunner(nil, &stdout, &stderr, "   ")
	if code != 1 {
		t.Errorf("RunThegentLoginWithRunner(empty provider) = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), "provider is required") {
		t.Errorf("stderr missing provider-required message: %q", stderr.String())
	}
}

func TestRunThegentLoginWithRunner_CLINotFound(t *testing.T) {
	mockRunner := func(NativeCLISpec) (int, error) {
		return -1, &mockErr{msg: "thegent CLI not found"}
	}
	var stdout, stderr bytes.Buffer
	code := RunThegentLoginWithRunner(mockRunner, &stdout, &stderr, "codex")
	if code != 1 {
		t.Errorf("RunThegentLoginWithRunner(not found) = %d, want 1", code)
	}
	if !strings.Contains(stderr.String(), thegentInstallHint) {
		t.Errorf("stderr missing install hint: %q", stderr.String())
	}
}
