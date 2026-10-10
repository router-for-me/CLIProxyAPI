package config

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestCooldownStatusCodeV8Layout(t *testing.T) {
	var cfg Config
	text := `
config-version: 8
routing:
  cooldown:
    status-code: 529
`
	if err := yaml.Unmarshal([]byte(text), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.CooldownStatusCode != 529 {
		t.Fatalf("v8 routing.cooldown.status-code = %d, want 529", cfg.CooldownStatusCode)
	}
}

func TestCooldownStatusCodeLegacyLayout(t *testing.T) {
	var cfg Config
	text := `
cooldown-status-code: 529
`
	if err := yaml.Unmarshal([]byte(text), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.CooldownStatusCode != 529 {
		t.Fatalf("legacy cooldown-status-code = %d, want 529", cfg.CooldownStatusCode)
	}
}

func TestCooldownStatusCodeAbsentKeepsDefault(t *testing.T) {
	var cfg Config
	text := `
config-version: 8
routing:
  cooldown:
    disable-cooling: false
`
	if err := yaml.Unmarshal([]byte(text), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.CooldownStatusCode != 0 {
		t.Fatalf("absent cooldown-status-code = %d, want 0", cfg.CooldownStatusCode)
	}
}
