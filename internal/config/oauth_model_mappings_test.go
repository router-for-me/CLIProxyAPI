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
