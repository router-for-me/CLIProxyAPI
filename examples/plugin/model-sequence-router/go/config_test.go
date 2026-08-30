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
		got := alias.Sequence[i]
		if got.Provider != want[i].Provider || got.Model != want[i].Model || len(got.Efforts) != 0 {
			t.Fatalf("sequence[%d] = %#v, want %#v", i, got, want[i])
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

func TestDecodeAndCompileConfigCompilesEffortTiers(t *testing.T) {
	cfg, errCompile := decodeAndCompileConfig([]byte(`
aliases:
  - alias: tuned
    targets:
      - provider: codex
        model: sol
        repeat: 3
        efforts:
          medium: xhigh
          high: {effort: xhigh}
      - provider: claude
        model: opus-5
        efforts:
          low: {model: opus-4-8}
          medium: {model: opus-4-8, effort: medium}
`), 1)
	if errCompile != nil {
		t.Fatalf("decodeAndCompileConfig() error = %v", errCompile)
	}
	sequence := cfg.Aliases[0].Sequence
	if len(sequence) != 4 {
		t.Fatalf("sequence length = %d, want 4", len(sequence))
	}
	for index := range 3 {
		if got := sequence[index].effectiveModel("high"); got != "sol(xhigh)" {
			t.Fatalf("repeated position %d = %q, want sol(xhigh)", index, got)
		}
	}
	if got := sequence[0].effectiveModel("medium"); got != "sol(xhigh)" {
		t.Fatalf("scalar tier = %q, want sol(xhigh)", got)
	}
	claudeSlot := sequence[3]
	if got := claudeSlot.effectiveModel("low"); got != "opus-4-8(low)" {
		t.Fatalf("model-only tier = %q, want opus-4-8(low)", got)
	}
	if got := claudeSlot.effectiveModel("medium"); got != "opus-4-8(medium)" {
		t.Fatalf("full tier = %q, want opus-4-8(medium)", got)
	}
	if got := claudeSlot.effectiveModel("xhigh"); got != "opus-5(xhigh)" {
		t.Fatalf("untiered level = %q, want opus-5(xhigh)", got)
	}
}

func TestDecodeAndCompileConfigRejectsInvalidValues(t *testing.T) {
	tests := map[string]string{
		"no aliases":              `enabled: true`,
		"blank alias":             "aliases:\n- alias: ' '\n  targets: [{provider: codex, model: terra}]",
		"duplicate alias":         "aliases:\n- alias: Test\n  targets: [{provider: codex, model: terra}]\n- alias: test\n  targets: [{provider: claude, model: opus}]",
		"suffix conflict":         "aliases:\n- alias: Test\n  targets: [{provider: codex, model: terra}]\n- alias: test(high)\n  targets: [{provider: claude, model: opus}]",
		"missing targets":         "aliases:\n- alias: test",
		"blank provider":          "aliases:\n- alias: test\n  targets: [{provider: ' ', model: terra}]",
		"blank model":             "aliases:\n- alias: test\n  targets: [{provider: codex, model: ' '}]",
		"zero repeat":             "aliases:\n- alias: test\n  targets: [{provider: codex, model: terra, repeat: 0}]",
		"negative repeat":         "aliases:\n- alias: test\n  targets: [{provider: codex, model: terra, repeat: -1}]",
		"invalid ttl":             "session_ttl: later\naliases:\n- alias: test\n  targets: [{provider: codex, model: terra}]",
		"too short ttl":           "session_ttl: 59s\naliases:\n- alias: test\n  targets: [{provider: codex, model: terra}]",
		"too long ttl":            "session_ttl: 25h\naliases:\n- alias: test\n  targets: [{provider: codex, model: terra}]",
		"sequence too long":       "aliases:\n- alias: test\n  targets: [{provider: codex, model: terra, repeat: 65537}]",
		"unknown effort key":      "aliases:\n- alias: test\n  targets: [{provider: codex, model: terra, efforts: {ultra: high}}]",
		"uppercase effort key":    "aliases:\n- alias: test\n  targets: [{provider: codex, model: terra, efforts: {HIGH: xhigh}}]",
		"unknown tier effort":     "aliases:\n- alias: test\n  targets: [{provider: codex, model: terra, efforts: {high: ultra}}]",
		"tier names provider":     "aliases:\n- alias: test\n  targets: [{provider: codex, model: terra, efforts: {high: {provider: claude, model: opus}}}]",
		"unknown tier key":        "aliases:\n- alias: test\n  targets: [{provider: codex, model: terra, efforts: {high: {mode: xhigh}}}]",
		"empty tier body":         "aliases:\n- alias: test\n  targets: [{provider: codex, model: terra, efforts: {high: {}}}]",
		"decreasing effort map":   "aliases:\n- alias: test\n  targets: [{provider: codex, model: terra, efforts: {low: high, medium: low}}]",
		"blank tier model":        "aliases:\n- alias: test\n  targets: [{provider: codex, model: terra, efforts: {high: {model: ' '}}}]",
		"blank tier effort":       "aliases:\n- alias: test\n  targets: [{provider: codex, model: terra, efforts: {high: {model: sol, effort: ' '}}}]",
		"lower request emits max": "aliases:\n- alias: test\n  targets: [{provider: codex, model: terra, efforts: {xhigh: max}}]",
		"tier model pins max":     "aliases:\n- alias: test\n  targets: [{provider: codex, model: terra, efforts: {xhigh: {model: 'luna(max)'}}}]",
		"slot model pins max":     "aliases:\n- alias: test\n  targets: [{provider: codex, model: 'terra(max)', efforts: {xhigh: high}}]",
		"max request pins budget": "aliases:\n- alias: test\n  targets: [{provider: codex, model: terra, efforts: {max: {model: 'luna(8000)'}}}]",
		"max slot pins high":      "aliases:\n- alias: test\n  targets: [{provider: codex, model: 'terra(high)', efforts: {max: max}}]",
		"max request downgraded":  "aliases:\n- alias: test\n  targets: [{provider: codex, model: terra, efforts: {max: {model: sol, effort: high}}}]",
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

func TestUnavailableActionDefaultsToSkipAndRejectsOtherValues(t *testing.T) {
	const alias = "aliases:\n- alias: test\n  targets: [{provider: codex, model: terra}]"
	cfg, errCompile := decodeAndCompileConfig([]byte(alias), 1)
	if errCompile != nil {
		t.Fatal(errCompile)
	}
	if cfg.OnUnavailable != unavailableSkip {
		t.Fatalf("default on_unavailable = %q, want %q", cfg.OnUnavailable, unavailableSkip)
	}
	if got := cfg.probeLimit(cfg.Aliases[0].Sequence); got != len(cfg.Aliases[0].Sequence) {
		t.Fatalf("skip probe limit = %d, want whole sequence", got)
	}
	overloaded, errOverloaded := decodeAndCompileConfig([]byte("on_unavailable: overloaded\n"+alias), 1)
	if errOverloaded != nil {
		t.Fatal(errOverloaded)
	}
	if got := overloaded.probeLimit(overloaded.Aliases[0].Sequence); got != 1 {
		t.Fatalf("overloaded probe limit = %d, want 1", got)
	}
	for name, raw := range map[string]string{
		"unknown action":   "on_unavailable: hold\n" + alias,
		"uppercase action": "on_unavailable: SKIP\n" + alias,
		"blank action":     "on_unavailable: ''\n" + alias,
	} {
		t.Run(name, func(t *testing.T) {
			if _, errInvalid := decodeAndCompileConfig([]byte(raw), 1); errInvalid == nil {
				t.Fatal("decodeAndCompileConfig() error = nil, want validation error")
			}
		})
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
