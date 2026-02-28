package fallback

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

// helperConfig returns a sanitized config with fallback enabled and the given rules/overrides.
func helperConfig(rules []config.ModelFallbackRule, overrides map[string]config.ModelFallbackOverride) *config.Config {
	cfg := &config.Config{
		ModelFallback: config.ModelFallback{
			Enabled:        true,
			Rules:          rules,
			ModelOverrides: overrides,
		},
	}
	cfg.SanitizeModelFallback()
	return cfg
}

func TestPlan_NilConfig(t *testing.T) {
	chain := Plan("gpt-4", nil)
	if len(chain) != 1 || chain[0] != "gpt-4" {
		t.Fatalf("expected [gpt-4], got %v", chain)
	}
}

func TestPlan_Disabled(t *testing.T) {
	cfg := &config.Config{ModelFallback: config.ModelFallback{Enabled: false}}
	chain := Plan("gpt-4", cfg)
	if len(chain) != 1 || chain[0] != "gpt-4" {
		t.Fatalf("expected [gpt-4], got %v", chain)
	}
}

func TestPlan_SingleRuleMatch(t *testing.T) {
	cfg := helperConfig([]config.ModelFallbackRule{
		{From: "gpt-4", To: []string{"gpt-4o", "gpt-3.5-turbo"}},
	}, nil)
	chain := Plan("gpt-4", cfg)
	// MaxAttempts defaults to 3 after sanitize, so we get original + 2 candidates
	expected := []string{"gpt-4", "gpt-4o", "gpt-3.5-turbo"}
	if len(chain) != len(expected) {
		t.Fatalf("expected %v, got %v", expected, chain)
	}
	for i, m := range expected {
		if chain[i] != m {
			t.Errorf("chain[%d] = %q, want %q", i, chain[i], m)
		}
	}
}

func TestPlan_MaxAttemptsTruncates(t *testing.T) {
	cfg := helperConfig([]config.ModelFallbackRule{
		{From: "gpt-4", To: []string{"gpt-4o", "gpt-3.5-turbo", "claude-3"}},
	}, nil)
	// Default MaxAttempts is 3, so chain = original + first 2 candidates
	chain := Plan("gpt-4", cfg)
	if len(chain) != 3 {
		t.Fatalf("expected 3 attempts, got %d: %v", len(chain), chain)
	}
	if chain[2] != "gpt-3.5-turbo" {
		t.Errorf("chain[2] = %q, want gpt-3.5-turbo", chain[2])
	}
}

func TestPlan_SuffixPreservation(t *testing.T) {
	cfg := helperConfig([]config.ModelFallbackRule{
		{From: "gpt-4", To: []string{"gpt-4o", "claude-3"}},
	}, nil)
	chain := Plan("gpt-4(8192)", cfg)
	expected := []string{"gpt-4(8192)", "gpt-4o(8192)", "claude-3(8192)"}
	if len(chain) != len(expected) {
		t.Fatalf("expected %v, got %v", expected, chain)
	}
	for i, m := range expected {
		if chain[i] != m {
			t.Errorf("chain[%d] = %q, want %q", i, chain[i], m)
		}
	}
}

func TestPlan_CandidateAlreadyHasSuffix(t *testing.T) {
	cfg := helperConfig([]config.ModelFallbackRule{
		{From: "gpt-4", To: []string{"gpt-4o(4096)"}},
	}, nil)
	chain := Plan("gpt-4(8192)", cfg)
	// candidate already has suffix, don't double-add
	if len(chain) < 2 {
		t.Fatalf("expected at least 2 entries, got %v", chain)
	}
	if chain[1] != "gpt-4o(4096)" {
		t.Errorf("chain[1] = %q, want gpt-4o(4096)", chain[1])
	}
}

func TestPlan_SelfReferenceRemoval(t *testing.T) {
	// SanitizeModelFallback removes self-references from rules
	cfg := helperConfig([]config.ModelFallbackRule{
		{From: "gpt-4", To: []string{"gpt-4", "gpt-4o"}},
	}, nil)
	chain := Plan("gpt-4", cfg)
	// "gpt-4" in To is removed by sanitize; chain = [gpt-4, gpt-4o]
	if len(chain) != 2 {
		t.Fatalf("expected 2 entries, got %v", chain)
	}
	if chain[1] != "gpt-4o" {
		t.Errorf("chain[1] = %q, want gpt-4o", chain[1])
	}
}

func TestPlan_CaseInsensitiveFromMatching(t *testing.T) {
	cfg := helperConfig([]config.ModelFallbackRule{
		{From: "GPT-4", To: []string{"gpt-4o"}},
	}, nil)
	chain := Plan("gpt-4", cfg)
	if len(chain) != 2 {
		t.Fatalf("expected 2 entries, got %v", chain)
	}
	if chain[1] != "gpt-4o" {
		t.Errorf("chain[1] = %q, want gpt-4o", chain[1])
	}
}

func TestPlan_ModelOverrideTakesPrecedence(t *testing.T) {
	cfg := helperConfig(
		[]config.ModelFallbackRule{
			{From: "gpt-4", To: []string{"gpt-4o"}},
		},
		map[string]config.ModelFallbackOverride{
			"gpt-4": {To: []string{"claude-3", "gemini-pro"}},
		},
	)
	chain := Plan("gpt-4", cfg)
	// Override should be used instead of rule
	if len(chain) < 2 {
		t.Fatalf("expected at least 2 entries, got %v", chain)
	}
	if chain[1] != "claude-3" {
		t.Errorf("chain[1] = %q, want claude-3", chain[1])
	}
}

func TestPlan_NoMatchingRule(t *testing.T) {
	cfg := helperConfig([]config.ModelFallbackRule{
		{From: "gpt-4", To: []string{"gpt-4o"}},
	}, nil)
	chain := Plan("claude-3", cfg)
	if len(chain) != 1 || chain[0] != "claude-3" {
		t.Fatalf("expected [claude-3], got %v", chain)
	}
}

// P1 regression: ModelOverrides.MaxAttempts must be capped at 10 by SanitizeModelFallback.
func TestPlan_OverrideMaxAttemptsCapped(t *testing.T) {
	cfg := helperConfig(
		[]config.ModelFallbackRule{
			{From: "gpt-4", To: []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l", "m", "n", "o"}},
		},
		map[string]config.ModelFallbackOverride{
			"gpt-4": {MaxAttempts: 100}, // should be capped to 10
		},
	)
	chain := Plan("gpt-4", cfg)
	if len(chain) > 10 {
		t.Fatalf("expected chain length <= 10 (capped), got %d: %v", len(chain), chain)
	}
	// Verify the override's MaxAttempts was actually capped in config
	if cfg.ModelFallback.ModelOverrides["gpt-4"].MaxAttempts != 10 {
		t.Errorf("expected override MaxAttempts capped to 10, got %d", cfg.ModelFallback.ModelOverrides["gpt-4"].MaxAttempts)
	}
}

// P2 regression: ModelOverrides keys must be case-insensitive after SanitizeModelFallback.
func TestPlan_OverrideCaseInsensitive(t *testing.T) {
	cfg := helperConfig(
		[]config.ModelFallbackRule{
			{From: "gpt-4", To: []string{"rule-fallback"}},
		},
		map[string]config.ModelFallbackOverride{
			"GPT-4": {To: []string{"override-fallback"}}, // uppercase key
		},
	)
	chain := Plan("gpt-4", cfg) // lowercase request
	if len(chain) < 2 {
		t.Fatalf("expected at least 2 entries, got %v", chain)
	}
	// Override should be found despite case mismatch
	if chain[1] != "override-fallback" {
		t.Errorf("expected override-fallback (override should match case-insensitively), got %q", chain[1])
	}
}

// Regression: override MaxAttempts should also work case-insensitively.
func TestPlan_OverrideMaxAttemptsCaseInsensitive(t *testing.T) {
	cfg := helperConfig(
		nil,
		map[string]config.ModelFallbackOverride{
			"gpt-4": {MaxAttempts: 2, To: []string{"a", "b", "c"}},
		},
	)
	// Request model casing differs from override key.
	chain := Plan("GPT-4", cfg)
	if len(chain) != 2 {
		t.Fatalf("expected chain length 2 (override max-attempts), got %d: %v", len(chain), chain)
	}
	if chain[1] != "a" {
		t.Fatalf("expected first override candidate 'a', got %q", chain[1])
	}
}

// P3: Stream.Enabled should inherit from parent Enabled when nil.
func TestSanitize_StreamEnabledInheritsFromParent(t *testing.T) {
	cfg := &config.Config{
		ModelFallback: config.ModelFallback{
			Enabled: false,
		},
	}
	cfg.SanitizeModelFallback()
	if cfg.ModelFallback.Stream.Enabled == nil {
		t.Fatal("expected Stream.Enabled to be set, got nil")
	}
	if *cfg.ModelFallback.Stream.Enabled != false {
		t.Errorf("expected Stream.Enabled to inherit false from parent, got true")
	}
}
