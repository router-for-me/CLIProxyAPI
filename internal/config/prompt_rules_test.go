package config

import (
	"strings"
	"testing"
)

func TestValidatePromptRules_Valid(t *testing.T) {
	cfg := &Config{
		PromptRules: []PromptRule{
			{
				Name:    "inject-style",
				Enabled: true,
				Target:  "system",
				Action:  "inject",
				Content: "<!-- pr:s --> JSON only.",
				Marker:  "<!-- pr:s -->",
			},
			{
				Name:    "strip-buddy",
				Enabled: true,
				Target:  "system",
				Action:  "strip",
				Pattern: `Buddy[^\n]*\n?`,
			},
		},
	}
	if err := cfg.ValidatePromptRules(); err != nil {
		t.Fatalf("expected valid; got %v", err)
	}
}

func TestValidatePromptRules_AcceptsEmptyMarkerOnInject(t *testing.T) {
	// v2: marker is optional. Empty marker means boundary mode.
	cfg := &Config{
		PromptRules: []PromptRule{{
			Name:    "boundary-inject",
			Enabled: true,
			Target:  "system",
			Action:  "inject",
			Content: "Always answer in JSON.",
		}},
	}
	if err := cfg.ValidatePromptRules(); err != nil {
		t.Fatalf("empty marker should be valid (boundary mode); got %v", err)
	}
}

func TestValidatePromptRules_AcceptsMarkerNotInContent(t *testing.T) {
	// v2: marker no longer needs to appear inside content. The marker is now
	// an anchor against the target text, not a sentinel embedded in content.
	cfg := &Config{
		PromptRules: []PromptRule{{
			Name:    "anchor-inject",
			Enabled: true,
			Target:  "system",
			Action:  "inject",
			Content: " (proxy)",
			Marker:  "qwen",
		}},
	}
	if err := cfg.ValidatePromptRules(); err != nil {
		t.Fatalf("marker outside content should be valid; got %v", err)
	}
}

func TestValidatePromptRules_RejectsInjectMissingContent(t *testing.T) {
	cfg := &Config{
		PromptRules: []PromptRule{{
			Name:    "no-content",
			Enabled: true,
			Target:  "system",
			Action:  "inject",
			Content: "",
		}},
	}
	err := cfg.ValidatePromptRules()
	if err == nil {
		t.Fatal("expected error when content is empty")
	}
	if !strings.Contains(err.Error(), "content is required") {
		t.Fatalf("error message lacks expected reason: %v", err)
	}
}

func TestValidatePromptRules_RejectsInvalidRegex(t *testing.T) {
	cfg := &Config{
		PromptRules: []PromptRule{{
			Name:    "bad-regex",
			Enabled: true,
			Target:  "system",
			Action:  "strip",
			Pattern: "(unclosed",
		}},
	}
	err := cfg.ValidatePromptRules()
	if err == nil {
		t.Fatal("expected error for unclosed regex")
	}
	if !strings.Contains(err.Error(), "invalid regex") {
		t.Fatalf("error message lacks expected reason: %v", err)
	}
}

func TestValidatePromptRules_RejectsLongPattern(t *testing.T) {
	cfg := &Config{
		PromptRules: []PromptRule{{
			Name:    "long",
			Enabled: true,
			Target:  "system",
			Action:  "strip",
			Pattern: strings.Repeat("a", PromptRulePatternMaxLen+1),
		}},
	}
	err := cfg.ValidatePromptRules()
	if err == nil {
		t.Fatal("expected error for over-long pattern")
	}
	if !strings.Contains(err.Error(), "exceeds max") {
		t.Fatalf("error message lacks expected reason: %v", err)
	}
}

func TestValidatePromptRules_RejectsDuplicateNames(t *testing.T) {
	cfg := &Config{
		PromptRules: []PromptRule{
			{
				Name: "dup", Enabled: true, Target: "system", Action: "inject",
				Content: "<!-- pr:d --> a", Marker: "<!-- pr:d -->",
			},
			{
				Name: "dup", Enabled: true, Target: "system", Action: "inject",
				Content: "<!-- pr:d --> b", Marker: "<!-- pr:d -->",
			},
		},
	}
	err := cfg.ValidatePromptRules()
	if err == nil {
		t.Fatal("expected duplicate-name rejection")
	}
	if !strings.Contains(err.Error(), "duplicate name") {
		t.Fatalf("error message lacks expected reason: %v", err)
	}
}

func TestValidatePromptRules_RejectsUnknownProtocol(t *testing.T) {
	prev := promptRuleProtocolValidator
	t.Cleanup(func() { promptRuleProtocolValidator = prev })
	SetPromptRuleProtocolValidator(func(p string) bool {
		return p == "" || p == "openai" || p == "claude" || p == "gemini"
	})
	cfg := &Config{
		PromptRules: []PromptRule{{
			Name: "rule", Enabled: true, Target: "system", Action: "inject",
			Content: "<!-- m --> a", Marker: "<!-- m -->",
			Models: []PayloadModelRule{{Name: "*", Protocol: "bogus"}},
		}},
	}
	err := cfg.ValidatePromptRules()
	if err == nil {
		t.Fatal("expected error for unknown protocol")
	}
	if !strings.Contains(err.Error(), "not a recognized source format") {
		t.Fatalf("error message lacks expected reason: %v", err)
	}
}

func TestValidatePromptRules_RejectsBadTargetAndAction(t *testing.T) {
	cases := []struct {
		name string
		rule PromptRule
		want string
	}{
		{
			"bad target",
			PromptRule{Name: "x", Target: "user-prompt", Action: "inject", Content: "<!-- m --> a", Marker: "<!-- m -->"},
			"invalid target",
		},
		{
			"bad action",
			PromptRule{Name: "x", Target: "system", Action: "replace", Content: "a", Marker: "a"},
			"invalid action",
		},
		{
			"missing name",
			PromptRule{Target: "system", Action: "inject", Content: "<!-- m --> a", Marker: "<!-- m -->"},
			"name is required",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &Config{PromptRules: []PromptRule{tc.rule}}
			err := cfg.ValidatePromptRules()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q; got %v", tc.want, err)
			}
		})
	}
}

func TestSanitizePromptRules_DropsInvalidAndDuplicates(t *testing.T) {
	cfg := &Config{
		PromptRules: []PromptRule{
			{Name: "ok", Enabled: true, Target: "system", Action: "inject", Content: "always JSON."},
			{Name: "no-content", Enabled: true, Target: "system", Action: "inject", Content: ""}, // missing content
			{Name: "ok", Enabled: true, Target: "user", Action: "strip", Pattern: "x"},           // duplicate name
			{Name: "bad-regex", Enabled: true, Target: "user", Action: "strip", Pattern: "(unclosed"},
		},
	}
	cfg.SanitizePromptRules()
	if got := len(cfg.PromptRules); got != 1 {
		t.Fatalf("expected 1 rule to survive sanitization; got %d: %+v", got, cfg.PromptRules)
	}
	if cfg.PromptRules[0].Name != "ok" {
		t.Fatalf("unexpected surviving rule: %+v", cfg.PromptRules[0])
	}
}

func TestNormalizePromptRules_Defaults(t *testing.T) {
	cfg := &Config{
		PromptRules: []PromptRule{{
			Name: "  Spaced  ", Enabled: true,
			Target: " SYSTEM ", Action: " Inject ",
			Content: "<!-- m --> a", Marker: "<!-- m -->",
			Position: "", // default to "append"
		}},
	}
	cfg.NormalizePromptRules()
	r := cfg.PromptRules[0]
	if r.Name != "Spaced" {
		t.Fatalf("name should be trimmed: %q", r.Name)
	}
	if r.Target != "system" {
		t.Fatalf("target should be lowercased: %q", r.Target)
	}
	if r.Action != "inject" {
		t.Fatalf("action should be lowercased: %q", r.Action)
	}
	if r.Position != "append" {
		t.Fatalf("position should default to append: %q", r.Position)
	}
}
