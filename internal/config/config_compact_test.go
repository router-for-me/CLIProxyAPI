package config

import (
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
