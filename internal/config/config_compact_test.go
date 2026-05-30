package config

import (
	"os"
	"path/filepath"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestCompactFieldsUnmarshal(t *testing.T) {
	raw := []byte(`
compact-default: deny
codex-api-key:
  - api-key: "sk-A"
    compact: force_on
openai-compatibility:
  - name: "kimi"
    base-url: "https://example.com"
    compact: force_off
`)
	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.CompactDefault != "deny" {
		t.Fatalf("CompactDefault = %q, want deny", cfg.CompactDefault)
	}
	if len(cfg.CodexKey) != 1 || cfg.CodexKey[0].Compact != "force_on" {
		t.Fatalf("CodexKey compact = %+v", cfg.CodexKey)
	}
	if len(cfg.OpenAICompatibility) != 1 || cfg.OpenAICompatibility[0].Compact != "force_off" {
		t.Fatalf("OpenAICompatibility compact = %+v", cfg.OpenAICompatibility)
	}
}

func TestLoadConfigOptional_NormalizesCompactSettings(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	configYAML := []byte(`
compact-default: "  DeNy "
codex-api-key:
  - api-key: "sk-A"
    base-url: "https://example.com"
    compact: " FORCE_OFF "
openai-compatibility:
  - name: "kimi"
    base-url: "https://example.com"
    compact: " Force_On "
`)
	if err := os.WriteFile(configPath, configYAML, 0o600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	cfg, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}
	if got := cfg.CompactDefault; got != "deny" {
		t.Fatalf("CompactDefault = %q, want deny", got)
	}
	if got := cfg.CodexKey[0].Compact; got != "force_off" {
		t.Fatalf("CodexKey[0].Compact = %q, want force_off", got)
	}
	if got := cfg.OpenAICompatibility[0].Compact; got != "force_on" {
		t.Fatalf("OpenAICompatibility[0].Compact = %q, want force_on", got)
	}
}

func TestLoadConfigOptional_InvalidCompactSettingsFallback(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	configYAML := []byte(`
compact-default: "invalid"
codex-api-key:
  - api-key: "sk-A"
    base-url: "https://example.com"
    compact: "wrong"
openai-compatibility:
  - name: "kimi"
    base-url: "https://example.com"
    compact: "wrong"
`)
	if err := os.WriteFile(configPath, configYAML, 0o600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	cfg, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}
	if got := cfg.CompactDefault; got != "allow" {
		t.Fatalf("CompactDefault = %q, want allow", got)
	}
	if got := cfg.CodexKey[0].Compact; got != "auto" {
		t.Fatalf("CodexKey[0].Compact = %q, want auto", got)
	}
	if got := cfg.OpenAICompatibility[0].Compact; got != "auto" {
		t.Fatalf("OpenAICompatibility[0].Compact = %q, want auto", got)
	}
}
