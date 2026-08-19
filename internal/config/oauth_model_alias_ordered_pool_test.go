package config

import "testing"

// Ordered pools allow the same alias to map to multiple upstream models for
// sequential failover; sanitization must preserve those entries in config
// order and only drop fully identical (name+alias) duplicates.
func TestSanitizeOAuthModelAlias_PreservesOrderedPoolDuplicates(t *testing.T) {
	cfg := &Config{
		OAuthModelAlias: map[string][]OAuthModelAlias{
			"codex": {
				{Name: "gpt-5", Alias: "g5"},
				{Name: "gpt-5-mini", Alias: "g5"},
				{Name: "gpt-5", Alias: "g5"}, // exact duplicate: dropped
				{Name: "gpt-5-nano", Alias: "g5"},
				{Name: "gpt-6", Alias: "g6"},
			},
		},
	}

	cfg.SanitizeOAuthModelAlias()

	aliases := cfg.OAuthModelAlias["codex"]
	expected := []OAuthModelAlias{
		{Name: "gpt-5", Alias: "g5"},
		{Name: "gpt-5-mini", Alias: "g5"},
		{Name: "gpt-5-nano", Alias: "g5"},
		{Name: "gpt-6", Alias: "g6"},
	}
	if len(aliases) != len(expected) {
		t.Fatalf("expected %d sanitized aliases, got %d: %+v", len(expected), len(aliases), aliases)
	}
	for i, exp := range expected {
		if aliases[i].Name != exp.Name || aliases[i].Alias != exp.Alias {
			t.Fatalf("expected alias %d to be name=%q alias=%q, got name=%q alias=%q", i, exp.Name, exp.Alias, aliases[i].Name, aliases[i].Alias)
		}
	}
}

// Case-insensitive exact duplicates (same name+alias pair) must still be
// collapsed even when the raw strings differ in case or surrounding spaces.
func TestSanitizeOAuthModelAlias_CollapsesIdenticalEntriesCaseInsensitively(t *testing.T) {
	cfg := &Config{
		OAuthModelAlias: map[string][]OAuthModelAlias{
			"codex": {
				{Name: " GPT-5 ", Alias: " g5 "},
				{Name: "gpt-5", Alias: "G5"},
			},
		},
	}

	cfg.SanitizeOAuthModelAlias()

	aliases := cfg.OAuthModelAlias["codex"]
	if len(aliases) != 1 {
		t.Fatalf("expected 1 sanitized alias after collapsing identical entries, got %d: %+v", len(aliases), aliases)
	}
	if aliases[0].Name != "GPT-5" || aliases[0].Alias != "g5" {
		t.Fatalf("expected first occurrence preserved as name=%q alias=%q, got name=%q alias=%q", "GPT-5", "g5", aliases[0].Name, aliases[0].Alias)
	}
}
