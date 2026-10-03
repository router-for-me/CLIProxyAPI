package config

import "testing"

func TestSanitizeOAuthModelAlias_PreservesOptionalFields(t *testing.T) {
	cfg := &Config{
		OAuthModelAlias: map[string][]OAuthModelAlias{
			" CoDeX ": {
				{Name: " gpt-5 ", Alias: " g5 ", Fork: true, DisplayName: " GPT Five ", ForceMapping: true},
				{Name: "gpt-6", Alias: "g6"},
			},
		},
	}

	cfg.SanitizeOAuthModelAlias()

	aliases := cfg.OAuthModelAlias["codex"]
	if len(aliases) != 2 {
		t.Fatalf("expected 2 sanitized aliases, got %d", len(aliases))
	}
	if aliases[0].Name != "gpt-5" || aliases[0].Alias != "g5" || !aliases[0].Fork || aliases[0].DisplayName != "GPT Five" || !aliases[0].ForceMapping {
		t.Fatalf("unexpected sanitized first alias: %+v", aliases[0])
	}
	if aliases[1].Name != "gpt-6" || aliases[1].Alias != "g6" || aliases[1].Fork || aliases[1].DisplayName != "" || aliases[1].ForceMapping {
		t.Fatalf("unexpected sanitized second alias: %+v", aliases[1])
	}
}

func TestSanitizeOAuthModelAlias_AllowsMultipleAliasesForSameName(t *testing.T) {
	cfg := &Config{
		OAuthModelAlias: map[string][]OAuthModelAlias{
			"antigravity": {
				{Name: "gemini-claude-opus-4-5-thinking", Alias: "claude-opus-4-5-20251101", Fork: true},
				{Name: "gemini-claude-opus-4-5-thinking", Alias: "claude-opus-4-5-20251101-thinking", Fork: true},
				{Name: "gemini-claude-opus-4-5-thinking", Alias: "claude-opus-4-5", Fork: true},
			},
		},
	}

	cfg.SanitizeOAuthModelAlias()

	aliases := cfg.OAuthModelAlias["antigravity"]
	expected := []OAuthModelAlias{
		{Name: "gemini-claude-opus-4-5-thinking", Alias: "claude-opus-4-5-20251101", Fork: true},
		{Name: "gemini-claude-opus-4-5-thinking", Alias: "claude-opus-4-5-20251101-thinking", Fork: true},
		{Name: "gemini-claude-opus-4-5-thinking", Alias: "claude-opus-4-5", Fork: true},
	}
	if len(aliases) != len(expected) {
		t.Fatalf("expected %d sanitized aliases, got %d", len(expected), len(aliases))
	}
	for i, exp := range expected {
		if aliases[i].Name != exp.Name || aliases[i].Alias != exp.Alias || aliases[i].Fork != exp.Fork {
			t.Fatalf("expected alias %d to be name=%q alias=%q fork=%v, got name=%q alias=%q fork=%v", i, exp.Name, exp.Alias, exp.Fork, aliases[i].Name, aliases[i].Alias, aliases[i].Fork)
		}
	}
}

func TestParseConfigOAuthMetaChannel(t *testing.T) {
	const yamlConfig = `
oauth-model-alias:
  meta:
    - name: "muse-spark-1.3"
      alias: "muse-latest"
      fork: true
      force-mapping: true
oauth-excluded-models:
  meta:
    - "muse-spark-1.1"
oauth-request-scoped-errors:
  meta:
    - status: 400
      match:
        - "context_length_exceeded"
      action: "stop"
`

	cfg, err := ParseConfigBytes([]byte(yamlConfig))
	if err != nil {
		t.Fatalf("ParseConfigBytes failed: %v", err)
	}

	aliases, ok := cfg.OAuthModelAlias["meta"]
	if !ok || len(aliases) != 1 {
		t.Fatalf("oauth-model-alias[meta] missing or len != 1: %#v", aliases)
	}
	if aliases[0].Name != "muse-spark-1.3" || aliases[0].Alias != "muse-latest" || !aliases[0].Fork || !aliases[0].ForceMapping {
		t.Fatalf("unexpected meta alias: %+v", aliases[0])
	}

	excluded, ok := cfg.OAuthExcludedModels["meta"]
	if !ok || len(excluded) != 1 || excluded[0] != "muse-spark-1.1" {
		t.Fatalf("oauth-excluded-models[meta] = %#v, want [muse-spark-1.1]", excluded)
	}

	rules, ok := cfg.OAuthRequestScopedErrors["meta"]
	if !ok || len(rules) != 1 {
		t.Fatalf("oauth-request-scoped-errors[meta] missing or len != 1: %#v", rules)
	}
	if rules[0].Status != 400 || rules[0].Action != "stop" || len(rules[0].Match) != 1 || rules[0].Match[0] != "context_length_exceeded" {
		t.Fatalf("unexpected meta request-scoped error rule: %+v", rules[0])
	}
}
