package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	cliproxycmd "github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/cmd"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestRunSetupJSONResponseShape(t *testing.T) {
	t.Setenv("CLIPROXY_CONFIG", "")
	fixedNow := func() time.Time {
		return time.Date(2026, 2, 23, 1, 2, 3, 0, time.UTC)
	}

	exec := commandExecutor{
		setup: func(_ *config.Config, _ *cliproxycmd.SetupOptions) {},
		login: func(_ *config.Config, _ string, _ *cliproxycmd.LoginOptions) {},
		doctor: func(_ string) (map[string]any, error) {
			return map[string]any{"status": "ok"}, nil
		},
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run([]string{"setup", "--json", "--config", "/tmp/does-not-exist.yaml"}, &stdout, &stderr, fixedNow, exec)
	if exitCode != 0 {
		t.Fatalf("expected exit code 0, got %d (stderr=%q)", exitCode, stderr.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode JSON output: %v", err)
	}
	if got := payload["schema_version"]; got != responseSchemaVersion {
		t.Fatalf("schema_version = %v, want %s", got, responseSchemaVersion)
	}
	if got := payload["command"]; got != "setup" {
		t.Fatalf("command = %v, want setup", got)
	}
	if got := payload["ok"]; got != true {
		t.Fatalf("ok = %v, want true", got)
	}
	if got := payload["timestamp"]; got != "2026-02-23T01:02:03Z" {
		t.Fatalf("timestamp = %v, want 2026-02-23T01:02:03Z", got)
	}
	details, ok := payload["details"].(map[string]any)
	if !ok {
		t.Fatalf("details missing or wrong type: %#v", payload["details"])
	}
	if _, exists := details["config_path"]; !exists {
		t.Fatalf("details.config_path missing: %#v", details)
	}
}

func TestRunDoctorJSONFailureShape(t *testing.T) {
	t.Setenv("CLIPROXY_CONFIG", "")
	fixedNow := func() time.Time {
		return time.Date(2026, 2, 23, 4, 5, 6, 0, time.UTC)
	}

	exec := commandExecutor{
		setup: func(_ *config.Config, _ *cliproxycmd.SetupOptions) {},
		login: func(_ *config.Config, _ string, _ *cliproxycmd.LoginOptions) {},
		doctor: func(configPath string) (map[string]any, error) {
			return map[string]any{"config_path": configPath}, assertErr("boom")
		},
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	exitCode := run([]string{"doctor", "--json", "--config", "/tmp/missing.yaml"}, &stdout, &stderr, fixedNow, exec)
	if exitCode != 1 {
		t.Fatalf("expected exit code 1, got %d", exitCode)
	}

	text := strings.TrimSpace(stdout.String())
	var payload map[string]any
	if err := json.Unmarshal([]byte(text), &payload); err != nil {
		t.Fatalf("failed to decode JSON output: %v", err)
	}
	if got := payload["schema_version"]; got != responseSchemaVersion {
		t.Fatalf("schema_version = %v, want %s", got, responseSchemaVersion)
	}
	if got := payload["command"]; got != "doctor" {
		t.Fatalf("command = %v, want doctor", got)
	}
	if got := payload["ok"]; got != false {
		t.Fatalf("ok = %v, want false", got)
	}
	if got := payload["timestamp"]; got != "2026-02-23T04:05:06Z" {
		t.Fatalf("timestamp = %v, want 2026-02-23T04:05:06Z", got)
	}
	details, ok := payload["details"].(map[string]any)
	if !ok {
		t.Fatalf("details missing or wrong type: %#v", payload["details"])
	}
	if got, ok := details["error"].(string); !ok || !strings.Contains(got, "boom") {
		t.Fatalf("details.error = %#v, want contains boom", details["error"])
	}
}

type assertErr string

func (e assertErr) Error() string { return string(e) }
