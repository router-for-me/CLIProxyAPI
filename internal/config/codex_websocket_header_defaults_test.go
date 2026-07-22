package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigOptional_CodexHeaderDefaults(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	configYAML := []byte(`
codex-header-defaults:
  user-agent: "  my-codex-client/1.0  "
  beta-features: "  feature-a,feature-b  "
`)
	if err := os.WriteFile(configPath, configYAML, 0o600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	cfg, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}

	if got := cfg.CodexHeaderDefaults.UserAgent; got != "my-codex-client/1.0" {
		t.Fatalf("UserAgent = %q, want %q", got, "my-codex-client/1.0")
	}
	if got := cfg.CodexHeaderDefaults.BetaFeatures; got != "feature-a,feature-b" {
		t.Fatalf("BetaFeatures = %q, want %q", got, "feature-a,feature-b")
	}
}

func TestLoadConfigOptional_CodexIdentityConfuse(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	configYAML := []byte(`
codex:
  identity-confuse: true
  connect-timeout-seconds: 11
  response-header-timeout-seconds: 22
  first-event-timeout-seconds: 33
  stream-idle-timeout-seconds: 44
  websocket-ping-interval-seconds: 5
`)
	if err := os.WriteFile(configPath, configYAML, 0o600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	cfg, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}

	if !cfg.Codex.IdentityConfuse {
		t.Fatalf("IdentityConfuse = false, want true")
	}
	if got := cfg.Codex.ConnectTimeoutSeconds; got != 11 {
		t.Fatalf("ConnectTimeoutSeconds = %d, want 11", got)
	}
	if got := cfg.Codex.ResponseHeaderTimeoutSeconds; got != 22 {
		t.Fatalf("ResponseHeaderTimeoutSeconds = %d, want 22", got)
	}
	if got := cfg.Codex.FirstEventTimeoutSeconds; got != 33 {
		t.Fatalf("FirstEventTimeoutSeconds = %d, want 33", got)
	}
	if got := cfg.Codex.StreamIdleTimeoutSeconds; got != 44 {
		t.Fatalf("StreamIdleTimeoutSeconds = %d, want 44", got)
	}
	if got := cfg.Codex.WebsocketPingIntervalSeconds; got != 5 {
		t.Fatalf("WebsocketPingIntervalSeconds = %d, want 5", got)
	}
}
