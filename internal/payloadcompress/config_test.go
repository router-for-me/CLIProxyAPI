package payloadcompress

import (
	"testing"

	"gopkg.in/yaml.v3"
)

func TestDefaultConfigSafeLiteDefaults(t *testing.T) {
	cfg := DefaultConfig()
	if len(cfg.Combos) != 1 || len(cfg.Combos[0].Pipeline) != 2 {
		t.Fatalf("unexpected default combo: %+v", cfg.Combos)
	}
	lite := cfg.Combos[0].Pipeline[1]
	if lite.Engine != EngineLite {
		t.Fatalf("second default step = %q, want %q", lite.Engine, EngineLite)
	}
	if lite.Options.CompressToolResults == nil || *lite.Options.CompressToolResults {
		t.Errorf("default CompressToolResults = %v, want pointer to false", lite.Options.CompressToolResults)
	}
	if lite.Options.PreserveToolWhitespace == nil || !*lite.Options.PreserveToolWhitespace {
		t.Errorf("default PreserveToolWhitespace = %v, want pointer to true", lite.Options.PreserveToolWhitespace)
	}
}

func TestConfigParsesNewFields(t *testing.T) {
	const raw = `
enabled: true
exclusions: [" openai/gpt-4o ", "", "   ", "anthropic/*"]
hard-budget-tokens: 120000
combos:
  - name: c
    pipeline:
      - engine: lite
        options:
          preserve-tool-whitespace: false
          fidelity-gate: true
          risk-gate: false
`
	var cfg Config
	if err := yaml.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if cfg.HardBudgetTokens != 120000 {
		t.Errorf("HardBudgetTokens = %d, want 120000", cfg.HardBudgetTokens)
	}
	opts := cfg.Combos[0].Pipeline[0].Options
	if opts.PreserveToolWhitespace == nil || *opts.PreserveToolWhitespace {
		t.Errorf("PreserveToolWhitespace = %v, want pointer to false", opts.PreserveToolWhitespace)
	}
	if opts.FidelityGate == nil || !*opts.FidelityGate {
		t.Errorf("FidelityGate = %v, want pointer to true", opts.FidelityGate)
	}
	if opts.RiskGate == nil || *opts.RiskGate {
		t.Errorf("RiskGate = %v, want pointer to false", opts.RiskGate)
	}

	cfg.Sanitize()
	want := []string{"openai/gpt-4o", "anthropic/*"}
	if len(cfg.Exclusions) != len(want) {
		t.Fatalf("Exclusions = %v, want %v", cfg.Exclusions, want)
	}
	for i := range want {
		if cfg.Exclusions[i] != want[i] {
			t.Errorf("Exclusions[%d] = %q, want %q", i, cfg.Exclusions[i], want[i])
		}
	}
}

func TestSanitizeMapsRTKLevelToIntensity(t *testing.T) {
	cases := []struct {
		level string
		want  string
	}{
		{"lite", "minimal"},
		{"full", "standard"},
		{"ultra", "aggressive"},
		{"minimal", "minimal"},
		{"standard", "standard"},
		{"aggressive", "aggressive"},
	}
	for _, tc := range cases {
		cfg := Config{Enabled: true, Combos: []Combo{{
			Name:     "c",
			Pipeline: []Step{{Engine: EngineRTK, Level: tc.level}},
		}}}
		cfg.Sanitize()
		step := cfg.Combos[0].Pipeline[0]
		if step.Level != "" {
			t.Errorf("level %q: Level = %q, want stripped", tc.level, step.Level)
		}
		if step.Options.Intensity != tc.want {
			t.Errorf("level %q: Intensity = %q, want %q", tc.level, step.Options.Intensity, tc.want)
		}
	}
}

func TestSanitizeRTKLevelDoesNotOverrideExplicitIntensity(t *testing.T) {
	cfg := Config{Enabled: true, Combos: []Combo{{
		Name: "c",
		Pipeline: []Step{{
			Engine:  EngineRTK,
			Level:   "ultra",
			Options: StepOptions{Intensity: "standard"},
		}},
	}}}
	cfg.Sanitize()
	step := cfg.Combos[0].Pipeline[0]
	if step.Options.Intensity != "standard" {
		t.Errorf("Intensity = %q, want explicit standard to win", step.Options.Intensity)
	}
}

func TestSanitizeRejectsUnknownLevels(t *testing.T) {
	cases := []struct {
		engine string
		level  string
	}{
		{EngineRTK, "bogus"},
		{EngineCaveman, "bogus"},
		{EngineLite, "full"},
	}
	for _, tc := range cases {
		cfg := Config{Enabled: true, Combos: []Combo{{
			Name:     "c",
			Pipeline: []Step{{Engine: tc.engine, Level: tc.level}},
		}}}
		cfg.Sanitize()
		step := cfg.Combos[0].Pipeline[0]
		if step.Level != "" {
			t.Errorf("%s level %q: Level = %q, want rejected", tc.engine, tc.level, step.Level)
		}
		if step.Options.Intensity != "" {
			t.Errorf("%s level %q: Intensity = %q, want empty", tc.engine, tc.level, step.Options.Intensity)
		}
	}
}

func TestSanitizeKeepsCavemanLevel(t *testing.T) {
	cfg := Config{Enabled: true, Combos: []Combo{{
		Name:     "c",
		Pipeline: []Step{{Engine: EngineCaveman, Level: "ultra"}},
	}}}
	cfg.Sanitize()
	if got := cfg.Combos[0].Pipeline[0].Level; got != "ultra" {
		t.Errorf("caveman Level = %q, want ultra", got)
	}
}

func TestSanitizeMaxToolLengthDefaultsTo2000(t *testing.T) {
	for _, in := range []int{0, -5} {
		cfg := Config{Enabled: true, Combos: []Combo{{
			Name:     "c",
			Pipeline: []Step{{Engine: EngineLite, Options: StepOptions{MaxToolLength: in}}},
		}}}
		cfg.Sanitize()
		if got := cfg.Combos[0].Pipeline[0].Options.MaxToolLength; got != 2000 {
			t.Errorf("MaxToolLength %d normalized to %d, want 2000", in, got)
		}
	}
	cfg := Config{Enabled: true, Combos: []Combo{{
		Name:     "c",
		Pipeline: []Step{{Engine: EngineLite, Options: StepOptions{MaxToolLength: 10}}},
	}}}
	cfg.Sanitize()
	if got := cfg.Combos[0].Pipeline[0].Options.MaxToolLength; got != 10 {
		t.Errorf("MaxToolLength = %d, want 10 kept", got)
	}
}

func TestSanitizeHardBudgetTokens(t *testing.T) {
	cfg := Config{Enabled: true, HardBudgetTokens: -1}
	cfg.Sanitize()
	if cfg.HardBudgetTokens != 0 {
		t.Errorf("negative HardBudgetTokens = %d, want disabled (0)", cfg.HardBudgetTokens)
	}
	cfg = Config{Enabled: true, HardBudgetTokens: 5000}
	cfg.Sanitize()
	if cfg.HardBudgetTokens != 5000 {
		t.Errorf("positive HardBudgetTokens = %d, want kept", cfg.HardBudgetTokens)
	}
}

func TestSanitizeConfiguredEmptyCombosResolveOff(t *testing.T) {
	cfg := Config{Enabled: true, Combos: []Combo{{
		Name:     "c",
		Pipeline: []Step{{Engine: "does-not-exist"}},
	}}}
	cfg.Sanitize()
	if len(cfg.Combos) != 0 {
		t.Fatalf("Combos = %+v, want none (off)", cfg.Combos)
	}
	if got := cfg.resolve("gpt-4o"); got != nil {
		t.Errorf("resolve = %+v, want nil (off)", got)
	}

	// No pipeline configured at all still gets the built-in safe default.
	cfg = Config{Enabled: true}
	cfg.Sanitize()
	if len(cfg.Combos) != 1 {
		t.Fatalf("Combos = %+v, want the built-in default", cfg.Combos)
	}
}
