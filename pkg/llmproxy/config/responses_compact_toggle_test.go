package config

import "testing"

func TestIsResponsesCompactEnabled_DefaultTrue(t *testing.T) {
	var cfg *Config
	if !cfg.IsResponsesCompactEnabled() {
		t.Fatal("nil config should default responses compact to enabled")
	}

	cfg = &Config{}
	if !cfg.IsResponsesCompactEnabled() {
		t.Fatal("unset responses compact toggle should default to enabled")
	}
}

func TestIsResponsesCompactEnabled_RespectsToggle(t *testing.T) {
	enabled := true
	disabled := false

	cfgEnabled := &Config{ResponsesCompactEnabled: &enabled}
	if !cfgEnabled.IsResponsesCompactEnabled() {
		t.Fatal("expected explicit true toggle to enable responses compact")
	}

	cfgDisabled := &Config{ResponsesCompactEnabled: &disabled}
	if cfgDisabled.IsResponsesCompactEnabled() {
		t.Fatal("expected explicit false toggle to disable responses compact")
	}
}
