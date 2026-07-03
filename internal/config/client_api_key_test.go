package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestClientAPIKeysUnmarshalYAML_mixed(t *testing.T) {
	raw := `
api-keys:
  - plain-key
  - key: key-with-alias
    model-aliases:
      - name: deepseek-v4
        alias: claude-opus-4.7
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	cfg.SanitizeClientAPIKeys()
	if len(cfg.ClientAPIKeys) != 2 {
		t.Fatalf("len = %d, want 2", len(cfg.ClientAPIKeys))
	}
	if cfg.ClientAPIKeys[0].Key != "plain-key" || len(cfg.ClientAPIKeys[0].ModelAliases) != 0 {
		t.Fatalf("entry0 = %#v", cfg.ClientAPIKeys[0])
	}
	if cfg.ClientAPIKeys[1].Key != "key-with-alias" {
		t.Fatalf("entry1 key = %q", cfg.ClientAPIKeys[1].Key)
	}
	if len(cfg.ClientAPIKeys[1].ModelAliases) != 1 || cfg.ClientAPIKeys[1].ModelAliases[0].Alias != "claude-opus-4.7" {
		t.Fatalf("entry1 aliases = %#v", cfg.ClientAPIKeys[1].ModelAliases)
	}
	keys := cfg.ClientAPIKeys.APIKeyStrings()
	if len(keys) != 2 || keys[0] != "plain-key" {
		t.Fatalf("APIKeyStrings = %#v", keys)
	}
}
