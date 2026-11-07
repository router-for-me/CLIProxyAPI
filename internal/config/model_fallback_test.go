package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestModelFallbackTargetsMatchesBaseModelAndCarriesSuffix(t *testing.T) {
	fallback := ModelFallbackConfig{Enabled: true, Chains: map[string][]string{
		"gpt-6-astra": {"claude-opus-5-5", "gpt-6.1-terra(low)"},
	}}
	if got := fallback.Targets("gpt-6-astra"); !reflect.DeepEqual(got, []string{"claude-opus-5-5", "gpt-6.1-terra(low)"}) {
		t.Fatalf("Targets(exact) = %v", got)
	}
	if got := fallback.Targets("GPT-6-Astra(high)"); !reflect.DeepEqual(got, []string{"claude-opus-5-5(high)", "gpt-6.1-terra(low)"}) {
		t.Fatalf("Targets(suffix) = %v, want suffix carried to targets without their own", got)
	}
	if got := fallback.Targets("unknown"); got != nil {
		t.Fatalf("Targets(unknown) = %v, want nil", got)
	}
	fallback.Enabled = false
	if got := fallback.Targets("gpt-6-astra"); got != nil {
		t.Fatalf("disabled Targets = %v, want nil", got)
	}
}

func TestModelFallbackConditionsDefaultsAndParsing(t *testing.T) {
	defaults := ModelFallbackConfig{}.Conditions()
	if !defaults.Statuses[429] || !defaults.Statuses[529] || !defaults.QuotaExhausted || !defaults.AuthUnavailable || defaults.Any5xx {
		t.Fatalf("default conditions = %+v", defaults)
	}
	custom := ModelFallbackConfig{On: []string{" 503 ", "5XX", "Quota_Exhausted"}}.Conditions()
	if !custom.Statuses[503] || !custom.Any5xx || !custom.QuotaExhausted || custom.AuthUnavailable || custom.Statuses[429] {
		t.Fatalf("custom conditions = %+v", custom)
	}
}

func TestModelFallbackValidateRejectsRequestFaultsAndBadChains(t *testing.T) {
	valid := ModelFallbackConfig{Enabled: true, MaxHops: 2, On: []string{"429", "5xx", "quota_exhausted", "auth_unavailable", "408"}, Chains: map[string][]string{"a": {"b"}}}
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate(valid) = %v", err)
	}
	for name, invalid := range map[string]ModelFallbackConfig{
		"400":           {On: []string{"400"}},
		"401":           {On: []string{"401"}},
		"403":           {On: []string{"403"}},
		"404":           {On: []string{"404"}},
		"413":           {On: []string{"413"}},
		"422":           {On: []string{"422"}},
		"unknown token": {On: []string{"sometimes"}},
		"negative hops": {MaxHops: -1},
		"too many hops": {MaxHops: MaxModelFallbackHops + 1},
		"empty chain":   {Chains: map[string][]string{"a": {}}},
		"empty target":  {Chains: map[string][]string{"a": {" "}}},
		"self target":   {Chains: map[string][]string{"a": {"A"}}},
		"empty source":  {Chains: map[string][]string{" ": {"b"}}},
	} {
		if err := invalid.Validate(); err == nil {
			t.Fatalf("Validate(%s) = nil, want error", name)
		}
	}
}

func TestSanitizeModelFallbackDropsInvalidEntries(t *testing.T) {
	cfg := &Config{}
	cfg.Routing.ModelFallback = ModelFallbackConfig{
		Enabled: true,
		MaxHops: 99,
		On:      []string{"429", "400", "429", "QUOTA_EXHAUSTED"},
		Chains:  map[string][]string{" a ": {"b", "A", "b", " ", "c"}, "": {"x"}, "empty": {}},
	}
	cfg.SanitizeModelFallback()
	got := cfg.Routing.ModelFallback
	if got.MaxHops != MaxModelFallbackHops {
		t.Fatalf("MaxHops = %d, want cap %d", got.MaxHops, MaxModelFallbackHops)
	}
	if !reflect.DeepEqual(got.On, []string{"429", "quota_exhausted"}) {
		t.Fatalf("On = %v", got.On)
	}
	if !reflect.DeepEqual(got.Chains, map[string][]string{"a": {"b", "c"}}) {
		t.Fatalf("Chains = %v", got.Chains)
	}
}

func TestModelFallbackParsesFromV8AndLegacyLayouts(t *testing.T) {
	block := `
  model-fallback:
    enabled: true
    max-hops: 2
    on: [429, quota_exhausted, auth_unavailable, 529]
    chains:
      gpt-6-astra: [claude-opus-5-5]
      coder: [claude-opus-5-5, gpt-6.1-terra]
`
	want := ModelFallbackConfig{
		Enabled: true,
		MaxHops: 2,
		On:      []string{"429", "quota_exhausted", "auth_unavailable", "529"},
		Chains:  map[string][]string{"gpt-6-astra": {"claude-opus-5-5"}, "coder": {"claude-opus-5-5", "gpt-6.1-terra"}},
	}
	for name, raw := range map[string]string{
		"v8":     "config-version: 8\nrouting:" + block,
		"legacy": "port: 8317\nrouting:" + block,
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadConfig(path)
			if err != nil {
				t.Fatalf("LoadConfig: %v", err)
			}
			if !reflect.DeepEqual(cfg.Routing.ModelFallback, want) {
				t.Fatalf("ModelFallback = %+v, want %+v", cfg.Routing.ModelFallback, want)
			}
			parsed, err := ParseConfigBytes([]byte(raw))
			if err != nil {
				t.Fatalf("ParseConfigBytes: %v", err)
			}
			if !reflect.DeepEqual(parsed.Routing.ModelFallback, want) {
				t.Fatalf("parsed ModelFallback = %+v, want %+v", parsed.Routing.ModelFallback, want)
			}
		})
	}
	if err := ValidateV8Config([]byte("config-version: 8\nrouting:" + block)); err != nil {
		t.Fatalf("ValidateV8Config(valid) = %v", err)
	}
	invalid := strings.Replace(block, "529]", "401]", 1)
	if err := ValidateV8Config([]byte("config-version: 8\nrouting:" + invalid)); err == nil {
		t.Fatal("ValidateV8Config accepted a 401 fallback condition")
	}
}

func TestModelFallbackEmptyConditionsNeverEnableDefaults(t *testing.T) {
	for _, on := range [][]string{{}, {"401"}} {
		cfg := &Config{}
		cfg.Routing.ModelFallback = ModelFallbackConfig{Enabled: true, On: on}
		cfg.SanitizeModelFallback()
		conditions := cfg.Routing.ModelFallback.Conditions()
		if len(conditions.Statuses) > 0 || conditions.Any5xx || conditions.QuotaExhausted || conditions.AuthUnavailable {
			t.Fatalf("explicit conditions %v broadened to %+v", on, conditions)
		}
	}
	cfg, err := ParseConfigBytes([]byte("config-version: 8\nrouting:\n  model-fallback:\n    enabled: true\n    on: []\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Routing.ModelFallback.Conditions().Statuses) != 0 {
		t.Fatal("empty YAML conditions enabled defaults")
	}
}
