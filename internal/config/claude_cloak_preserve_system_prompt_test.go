package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigOptional_CloakPreserveSystemPrompt(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	configYAML := []byte(`
claude-api-key:
  - api-key: "sk-preserve-true"
    cloak:
      preserve-system-prompt: true
  - api-key: "sk-preserve-false"
    cloak:
      preserve-system-prompt: false
  - api-key: "sk-preserve-unset"
    cloak:
      mode: "always"
`)
	if err := os.WriteFile(configPath, configYAML, 0o600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	cfg, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}

	if len(cfg.ClaudeKey) != 3 {
		t.Fatalf("ClaudeKey count = %d, want 3", len(cfg.ClaudeKey))
	}

	enabled := cfg.ClaudeKey[0].Cloak
	if enabled == nil || enabled.PreserveSystemPrompt == nil {
		t.Fatal("ClaudeKey[0].Cloak.PreserveSystemPrompt = nil, want non-nil")
	}
	if got := *enabled.PreserveSystemPrompt; !got {
		t.Fatalf("ClaudeKey[0].Cloak.PreserveSystemPrompt = %v, want true", got)
	}

	disabled := cfg.ClaudeKey[1].Cloak
	if disabled == nil || disabled.PreserveSystemPrompt == nil {
		t.Fatal("ClaudeKey[1].Cloak.PreserveSystemPrompt = nil, want non-nil")
	}
	if got := *disabled.PreserveSystemPrompt; got {
		t.Fatalf("ClaudeKey[1].Cloak.PreserveSystemPrompt = %v, want false", got)
	}

	unset := cfg.ClaudeKey[2].Cloak
	if unset == nil {
		t.Fatal("ClaudeKey[2].Cloak = nil, want non-nil")
	}
	if unset.PreserveSystemPrompt != nil {
		t.Fatalf("ClaudeKey[2].Cloak.PreserveSystemPrompt = %v, want nil", *unset.PreserveSystemPrompt)
	}
}
