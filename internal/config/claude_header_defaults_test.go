package config

import (
	"os"
	"path/filepath"
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
  preserve-native-identity: true
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
	if !cfg.ClaudeHeaderDefaults.PreserveNativeIdentity {
		t.Fatal("PreserveNativeIdentity = false, want true")
	}
}

func TestParseConfigBytes_ClaudeHeaderDefaultsPreserveNativeIdentityV8Layout(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte("upstream: {claude: {header-defaults: {preserve-native-identity: true}}}\n"))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if !cfg.ClaudeHeaderDefaults.PreserveNativeIdentity {
		t.Fatal("PreserveNativeIdentity = false, want true")
	}

	migrated, _, err := NormalizeConfigLayout([]byte("claude-header-defaults: {preserve-native-identity: true}\n"), true)
	if err != nil {
		t.Fatalf("NormalizeConfigLayout() error = %v", err)
	}
	roundTrip, err := ParseConfigBytes(migrated)
	if err != nil {
		t.Fatalf("ParseConfigBytes(migrated) error = %v", err)
	}
	if !roundTrip.ClaudeHeaderDefaults.PreserveNativeIdentity {
		t.Fatalf("PreserveNativeIdentity lost after migration:\n%s", migrated)
	}
}

func TestSaveConfigPreserveComments_DisablesPreserveNativeIdentity(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("upstream:\n  claude:\n    header-defaults:\n      preserve-native-identity: true\n"), 0o600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}
	cfg, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}
	if !cfg.ClaudeHeaderDefaults.PreserveNativeIdentity {
		t.Fatal("PreserveNativeIdentity = false, want true before disabling")
	}

	cfg.ClaudeHeaderDefaults.PreserveNativeIdentity = false
	if err := SaveConfigPreserveComments(configPath, cfg); err != nil {
		t.Fatalf("SaveConfigPreserveComments() error = %v", err)
	}
	reloaded, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional(reloaded) error = %v", err)
	}
	if reloaded.ClaudeHeaderDefaults.PreserveNativeIdentity {
		saved, _ := os.ReadFile(configPath)
		t.Fatalf("PreserveNativeIdentity re-enabled after save:\n%s", saved)
	}
}
