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
}

func TestParseConfigBytes_CodexNativeCompaction(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`
codex:
  native-compaction:
    enabled: true
    trigger-tokens: 240000
    context-window: 272000
    claude-client-context-window: 1000000
    preserve-recent-tokens: 32000
    retained-message-tokens: 64000
    state-ttl: 168h
`))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}

	got := cfg.Codex.NativeCompaction
	if !got.Enabled || got.TriggerTokens != 240000 || got.ContextWindow != 272000 || got.ClaudeClientContextWindow != 1000000 || got.PreserveRecentTokens != 32000 || got.RetainedMessageTokens != 64000 || got.StateTTL != "168h" {
		t.Fatalf("native compaction config = %+v", got)
	}
}
