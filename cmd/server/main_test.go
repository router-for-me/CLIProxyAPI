package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/buildinfo"
)

func TestHandleVersionFlags_Short(t *testing.T) {
	restore := setBuildInfoForTest(t, "v6.9.0-alpha.1", "abc123", "2026-03-22T00:00:00Z")
	defer restore()

	var out bytes.Buffer
	handled, err := handleVersionFlags(&out, true, false)
	if err != nil {
		t.Fatalf("handleVersionFlags returned error: %v", err)
	}
	if !handled {
		t.Fatal("expected version flag to be handled")
	}
	if got := out.String(); got != "6.9.0-alpha.1\n" {
		t.Fatalf("short version output = %q, want %q", got, "6.9.0-alpha.1\n")
	}
}

func TestHandleVersionFlags_JSONFallback(t *testing.T) {
	restore := setBuildInfoForTest(t, "dev", "none", "unknown")
	defer restore()

	var out bytes.Buffer
	handled, err := handleVersionFlags(&out, false, true)
	if err != nil {
		t.Fatalf("handleVersionFlags returned error: %v", err)
	}
	if !handled {
		t.Fatal("expected version-json flag to be handled")
	}

	var payload map[string]any
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode json output: %v", err)
	}
	if got := payload["version"]; got != "0.0.0" {
		t.Fatalf("json version = %v, want %q", got, "0.0.0")
	}
	if got := payload["rawVersion"]; got != "dev" {
		t.Fatalf("json rawVersion = %v, want %q", got, "dev")
	}
	if got := payload["validSemver"]; got != false {
		t.Fatalf("json validSemver = %v, want false", got)
	}
}

func setBuildInfoForTest(t *testing.T, version string, commit string, buildDate string) func() {
	t.Helper()

	prevVersion := buildinfo.Version
	prevCommit := buildinfo.Commit
	prevBuildDate := buildinfo.BuildDate

	buildinfo.Version = version
	buildinfo.Commit = commit
	buildinfo.BuildDate = buildDate

	return func() {
		buildinfo.Version = prevVersion
		buildinfo.Commit = prevCommit
		buildinfo.BuildDate = prevBuildDate
	}
}
