package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadConfigOptional_ClaudeHeaderDefaults(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	configYAML := []byte(`
claude-header-defaults:
  user-agent: "  claude-cli/2.1.70 (external, cli)  "
  package-version: "  0.80.0  "
  runtime-version: "  v24.5.0  "
  os: "  MacOS  "
  arch: "  arm64  "
  timeout: "  900  "
  timezone: "  Pacific/Honolulu  "
  stabilize-device-profile: false
`)
	if err := os.WriteFile(configPath, configYAML, 0o600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	cfg, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}

	if got := cfg.ClaudeHeaderDefaults.UserAgent; got != "claude-cli/2.1.70 (external, cli)" {
		t.Fatalf("UserAgent = %q, want %q", got, "claude-cli/2.1.70 (external, cli)")
	}
	if got := cfg.ClaudeHeaderDefaults.PackageVersion; got != "0.80.0" {
		t.Fatalf("PackageVersion = %q, want %q", got, "0.80.0")
	}
	if got := cfg.ClaudeHeaderDefaults.RuntimeVersion; got != "v24.5.0" {
		t.Fatalf("RuntimeVersion = %q, want %q", got, "v24.5.0")
	}
	if got := cfg.ClaudeHeaderDefaults.OS; got != "MacOS" {
		t.Fatalf("OS = %q, want %q", got, "MacOS")
	}
	if got := cfg.ClaudeHeaderDefaults.Arch; got != "arm64" {
		t.Fatalf("Arch = %q, want %q", got, "arm64")
	}
	if got := cfg.ClaudeHeaderDefaults.Timeout; got != "900" {
		t.Fatalf("Timeout = %q, want %q", got, "900")
	}
	if got := cfg.ClaudeHeaderDefaults.Timezone; got != "Pacific/Honolulu" {
		t.Fatalf("Timezone = %q, want %q", got, "Pacific/Honolulu")
	}
	if cfg.ClaudeHeaderDefaults.StabilizeDeviceProfile == nil {
		t.Fatal("StabilizeDeviceProfile = nil, want non-nil")
	}
	if got := *cfg.ClaudeHeaderDefaults.StabilizeDeviceProfile; got {
		t.Fatalf("StabilizeDeviceProfile = %v, want false", got)
	}
}

func TestParseConfigBytes_ClaudeHeaderDefaultsOperatorOverrides(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`upstream:
  claude:
    header-defaults:
      anthropic-beta-strip: [" cache-diagnosis-2026-04-07 ", "", "cache-diagnosis-2026-04-07"]
      anthropic-beta-append: ["context-1m-2025-08-07", " context-1m-2025-08-07 ", "advisor-tool-2026-03-01"]
      inject-diagnostics: false
      client-request-id: " PassThrough "
`))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	hd := cfg.ClaudeHeaderDefaults
	if want := []string{"cache-diagnosis-2026-04-07"}; !reflect.DeepEqual(hd.AnthropicBetaStrip, want) {
		t.Fatalf("AnthropicBetaStrip = %q, want %q", hd.AnthropicBetaStrip, want)
	}
	if want := []string{"context-1m-2025-08-07", "advisor-tool-2026-03-01"}; !reflect.DeepEqual(hd.AnthropicBetaAppend, want) {
		t.Fatalf("AnthropicBetaAppend = %q, want %q", hd.AnthropicBetaAppend, want)
	}
	if hd.InjectDiagnostics == nil || *hd.InjectDiagnostics {
		t.Fatalf("InjectDiagnostics = %v, want explicit false", hd.InjectDiagnostics)
	}
	if hd.ClientRequestID != ClaudeClientRequestIDPassthrough {
		t.Fatalf("ClientRequestID = %q, want %q", hd.ClientRequestID, ClaudeClientRequestIDPassthrough)
	}

	cfg, err = ParseConfigBytes([]byte("claude-header-defaults: {client-request-id: sometimes}\n"))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if got := cfg.ClaudeHeaderDefaults.ClientRequestID; got != "" {
		t.Fatalf("ClientRequestID = %q, want unknown mode reset to default", got)
	}
}
