package main

import (
	"strings"
	"testing"
	"time"
)

func TestDecodeAndCompileConfigDefaultsAndExpansion(t *testing.T) {
	cfg, errCompile := decodeAndCompileConfig([]byte(`
aliases:
  - alias: " Iterative-Model "
    targets:
      - provider: " Codex "
        model: " terra "
        repeat: 3
      - provider: Claude
        model: claude-opus-4-6
`), 7)
	if errCompile != nil {
		t.Fatalf("decodeAndCompileConfig() error = %v", errCompile)
	}
	if !cfg.Enabled || cfg.SessionTTL != time.Hour || cfg.Generation != 7 {
		t.Fatalf("compiled defaults = %#v", cfg)
	}
	alias := cfg.Aliases[0]
	if alias.Alias != "Iterative-Model" || alias.DisplayName != alias.Alias {
		t.Fatalf("alias = %#v", alias)
	}
	if !alias.RandomStart {
		t.Fatal("RandomStart = false, want default true")
	}
	want := []compiledTarget{
		{Provider: "codex", Model: "terra"},
		{Provider: "codex", Model: "terra"},
		{Provider: "codex", Model: "terra"},
		{Provider: "claude", Model: "claude-opus-4-6"},
	}
	if len(alias.Sequence) != len(want) {
		t.Fatalf("sequence length = %d, want %d", len(alias.Sequence), len(want))
	}
	for i := range want {
		if alias.Sequence[i] != want[i] {
			t.Fatalf("sequence[%d] = %#v, want %#v", i, alias.Sequence[i], want[i])
		}
	}
}

func TestDecodeAndCompileConfigAllowsDeterministicStart(t *testing.T) {
	cfg, errCompile := decodeAndCompileConfig([]byte(`
aliases:
  - alias: deterministic
    random_start: false
    targets: [{provider: codex, model: terra}]
`), 1)
	if errCompile != nil {
		t.Fatal(errCompile)
	}
	if cfg.Aliases[0].RandomStart {
		t.Fatal("RandomStart = true, want explicit false")
	}
}

func TestDecodeAndCompileConfigPreservesArbitraryOrder(t *testing.T) {
	cfg, errCompile := decodeAndCompileConfig([]byte(`
session_ttl: 30m
aliases:
  - alias: sequence
    display_name: " Sequence Display "
    targets:
      - {provider: codex, model: terra, repeat: 2}
      - {provider: claude, model: opus}
      - {provider: codex, model: terra}
      - {provider: antigravity, model: gemini-pro}
`), 1)
	if errCompile != nil {
		t.Fatalf("decodeAndCompileConfig() error = %v", errCompile)
	}
	got := cfg.Aliases[0]
	if got.DisplayName != "Sequence Display" {
		t.Fatalf("DisplayName = %q", got.DisplayName)
	}
	wantProviders := []string{"codex", "codex", "claude", "codex", "antigravity"}
	for i, want := range wantProviders {
		if got.Sequence[i].Provider != want {
			t.Fatalf("provider[%d] = %q, want %q", i, got.Sequence[i].Provider, want)
		}
	}
}

func TestDecodeAndCompileConfigRejectsInvalidValues(t *testing.T) {
	tests := map[string]string{
		"no aliases":        `enabled: true`,
		"blank alias":       "aliases:\n- alias: ' '\n  targets: [{provider: codex, model: terra}]",
		"duplicate alias":   "aliases:\n- alias: Test\n  targets: [{provider: codex, model: terra}]\n- alias: test\n  targets: [{provider: claude, model: opus}]",
		"suffix conflict":   "aliases:\n- alias: Test\n  targets: [{provider: codex, model: terra}]\n- alias: test(high)\n  targets: [{provider: claude, model: opus}]",
		"missing targets":   "aliases:\n- alias: test",
		"blank provider":    "aliases:\n- alias: test\n  targets: [{provider: ' ', model: terra}]",
		"blank model":       "aliases:\n- alias: test\n  targets: [{provider: codex, model: ' '}]",
		"zero repeat":       "aliases:\n- alias: test\n  targets: [{provider: codex, model: terra, repeat: 0}]",
		"negative repeat":   "aliases:\n- alias: test\n  targets: [{provider: codex, model: terra, repeat: -1}]",
		"invalid ttl":       "session_ttl: later\naliases:\n- alias: test\n  targets: [{provider: codex, model: terra}]",
		"too short ttl":     "session_ttl: 59s\naliases:\n- alias: test\n  targets: [{provider: codex, model: terra}]",
		"too long ttl":      "session_ttl: 25h\naliases:\n- alias: test\n  targets: [{provider: codex, model: terra}]",
		"sequence too long": "aliases:\n- alias: test\n  targets: [{provider: codex, model: terra, repeat: 65537}]",
	}
	for name, raw := range tests {
		t.Run(name, func(t *testing.T) {
			if _, errCompile := decodeAndCompileConfig([]byte(raw), 1); errCompile == nil {
				t.Fatal("decodeAndCompileConfig() error = nil, want validation error")
			}
		})
	}
}

func TestDisabledConfigMayHaveNoAliases(t *testing.T) {
	cfg, errCompile := decodeAndCompileConfig([]byte("enabled: false"), 1)
	if errCompile != nil {
		t.Fatalf("decodeAndCompileConfig() error = %v", errCompile)
	}
	if cfg.Enabled || len(cfg.Aliases) != 0 {
		t.Fatalf("compiled config = %#v", cfg)
	}
}

func TestEffectiveSequenceLimitAccepted(t *testing.T) {
	raw := "aliases:\n- alias: test\n  targets: [{provider: codex, model: terra, repeat: 65536}]"
	cfg, errCompile := decodeAndCompileConfig([]byte(raw), 1)
	if errCompile != nil {
		t.Fatalf("decodeAndCompileConfig() error = %v", errCompile)
	}
	if len(cfg.Aliases[0].Sequence) != maxEffectiveTargets {
		t.Fatalf("sequence length = %d", len(cfg.Aliases[0].Sequence))
	}
}

func TestAliasLookupIsSuffixAware(t *testing.T) {
	cfg, errCompile := decodeAndCompileConfig([]byte("aliases:\n- alias: MixedCase\n  targets: [{provider: codex, model: terra}]"), 1)
	if errCompile != nil {
		t.Fatal(errCompile)
	}
	if cfg.ByLookup[strings.ToLower("MixedCase")] == nil {
		t.Fatal("case-insensitive lookup missing")
	}
}

func TestUnsupportedParentheticalAliasRemainsLiteral(t *testing.T) {
	cfg, errCompile := decodeAndCompileConfig([]byte(`
aliases:
  - alias: test
    targets: [{provider: codex, model: terra}]
  - alias: test(custom)
    targets: [{provider: claude, model: opus}]
`), 1)
	if errCompile != nil {
		t.Fatalf("decodeAndCompileConfig() error = %v", errCompile)
	}
	if cfg.ByLookup["test"] == nil || cfg.ByLookup["test(custom)"] == nil {
		t.Fatalf("literal parenthetical lookup missing: %#v", cfg.ByLookup)
	}
}
