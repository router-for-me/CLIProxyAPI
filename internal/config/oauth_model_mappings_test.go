package config

import "testing"

func TestSanitizeOAuthModelMappings_PreservesForkFlag(t *testing.T) {
	cfg := &Config{
		OAuthModelMappings: map[string][]ModelNameMapping{
			" CoDeX ": {
				{Name: " gpt-5 ", Alias: " g5 ", Fork: true},
				{Name: "gpt-6", Alias: "g6"},
			},
		},
	}

	cfg.SanitizeOAuthModelMappings()

	mappings := cfg.OAuthModelMappings["codex"]
	if len(mappings) != 2 {
		t.Fatalf("expected 2 sanitized mappings, got %d", len(mappings))
	}
	if mappings[0].Name != "gpt-5" || mappings[0].Alias != "g5" || !mappings[0].Fork {
		t.Fatalf("expected first mapping to be gpt-5->g5 fork=true, got name=%q alias=%q fork=%v", mappings[0].Name, mappings[0].Alias, mappings[0].Fork)
	}
	if mappings[1].Name != "gpt-6" || mappings[1].Alias != "g6" || mappings[1].Fork {
		t.Fatalf("expected second mapping to be gpt-6->g6 fork=false, got name=%q alias=%q fork=%v", mappings[1].Name, mappings[1].Alias, mappings[1].Fork)
	}
}

func TestSanitizeOAuthModelMappings_AllowsMultipleAliasesForSameName(t *testing.T) {
	cfg := &Config{
		OAuthModelMappings: map[string][]ModelNameMapping{
			"antigravity": {
				{Name: "gemini-claude-opus-4-5-thinking", Alias: "claude-opus-4-5-20251101", Fork: true},
				{Name: "gemini-claude-opus-4-5-thinking", Alias: "claude-opus-4-5-20251101-thinking", Fork: true},
				{Name: "gemini-claude-opus-4-5-thinking", Alias: "claude-opus-4-5", Fork: true},
			},
		},
	}

	cfg.SanitizeOAuthModelMappings()

	mappings := cfg.OAuthModelMappings["antigravity"]
	expected := []ModelNameMapping{
		{Name: "gemini-claude-opus-4-5-thinking", Alias: "claude-opus-4-5-20251101", Fork: true},
		{Name: "gemini-claude-opus-4-5-thinking", Alias: "claude-opus-4-5-20251101-thinking", Fork: true},
		{Name: "gemini-claude-opus-4-5-thinking", Alias: "claude-opus-4-5", Fork: true},
	}
	if len(mappings) != len(expected) {
		t.Fatalf("expected %d sanitized mappings, got %d", len(expected), len(mappings))
	}
	for i, exp := range expected {
		if mappings[i].Name != exp.Name || mappings[i].Alias != exp.Alias || mappings[i].Fork != exp.Fork {
			t.Fatalf("expected mapping %d to be name=%q alias=%q fork=%v, got name=%q alias=%q fork=%v", i, exp.Name, exp.Alias, exp.Fork, mappings[i].Name, mappings[i].Alias, mappings[i].Fork)
		}
	}
}
