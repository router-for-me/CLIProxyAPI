package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/cmd"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestVersionRequested(t *testing.T) {
	for _, args := range [][]string{{"--version"}, {"-version"}, {"--version", "--config", "ignored.yaml"}} {
		if !versionRequested(args) {
			t.Fatalf("versionRequested(%q) = false", args)
		}
	}
	for _, args := range [][]string{nil, {"--config", "config.yaml"}, {"--versioned"}, {"discover", "--json"}, {"--discover-json"}} {
		if versionRequested(args) {
			t.Fatalf("versionRequested(%q) = true", args)
		}
	}
}

func TestPrintHostVersionDoesNotPolluteDiscoveryOutput(t *testing.T) {
	for _, args := range [][]string{nil, {"discover", "--json"}, {"--discover-json"}, {"--discover-json=true"}} {
		var output bytes.Buffer
		if printHostVersion(args, &output) || output.Len() != 0 {
			t.Fatalf("unexpected version output for %q: %q", args, output.String())
		}
	}
	var output bytes.Buffer
	if !printHostVersion([]string{"--version"}, &output) {
		t.Fatal("version probe was not handled")
	}
	if !strings.HasPrefix(output.String(), "CLIProxyAPI Version: ") || strings.Count(output.String(), "\n") != 1 {
		t.Fatalf("unexpected version output: %q", output.String())
	}
}

func TestHostOptionsFromEnv(t *testing.T) {
	t.Setenv("CLIPROXY_EPHEMERAL_API_KEY", "  runtime-secret  ")
	options := hostOptionsFromEnv(42)
	if options.RuntimeContractVersion != "1" || options.ParentPID != 42 || options.EphemeralAPIKey != "runtime-secret" {
		t.Fatal("host options did not preserve the runtime contract")
	}
}

func TestHostSafeModePreservesUpstreamDecisions(t *testing.T) {
	for _, key := range []string{"", " ", "runtime-secret"} {
		for _, enabled := range []bool{false, true} {
			got := applyHostSafeMode(enabled, cmd.HostOptions{EphemeralAPIKey: key})
			want := enabled && strings.TrimSpace(key) == ""
			if got != want {
				t.Fatalf("applyHostSafeMode(enabled=%v, configured=%v) = %v, want %v", enabled, strings.TrimSpace(key) != "", got, want)
			}
		}
	}
	cfg := &config.Config{SDKConfig: config.SDKConfig{APIKeys: []string{"your-api-key-1"}}}
	enabled := shouldEnableExampleAPIKeySafeMode(cfg, false, false, false, false, false)
	if !enabled {
		t.Fatal("upstream must still detect example config keys")
	}
	if applyHostSafeMode(enabled, cmd.HostOptions{EphemeralAPIKey: "runtime-secret"}) {
		t.Fatal("a configured host key must preserve desktop availability")
	}
}
