package auth

import (
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// TestOrderedPoolSurvivesSanitizedConfigLoad proves the production path
// (YAML parse → SanitizeOAuthModelAlias → SetOAuthModelAlias) keeps an
// ordered failover chain of two or more candidates for a repeated alias.
func TestOrderedPoolSurvivesSanitizedConfigLoad(t *testing.T) {
	yamlPayload := []byte(`
host: "127.0.0.1"
port: 8317
oauth-model-alias:
  codex:
    - name: "gpt-5"
      alias: "g5"
    - name: "gpt-5-mini"
      alias: "g5"
    - name: "gpt-5-nano"
      alias: "g5"
`)

	cfg, errParse := internalconfig.ParseConfigBytes(yamlPayload)
	if errParse != nil {
		t.Fatalf("ParseConfigBytes: %v", errParse)
	}
	aliases := cfg.OAuthModelAlias["codex"]
	if len(aliases) < 3 {
		t.Fatalf("sanitized config dropped ordered pool: got %d entries %+v", len(aliases), aliases)
	}

	m := newTestManager(t)
	m.SetOAuthModelAlias(cfg.OAuthModelAlias)

	chain := m.resolveOrderedCandidates("codex", "g5")
	if len(chain) != 3 {
		t.Fatalf("expected 3 ordered candidates after sanitized load, got %d: %+v", len(chain), chain)
	}
	want := []string{"gpt-5", "gpt-5-mini", "gpt-5-nano"}
	for i, model := range want {
		if chain[i].UpstreamModel != model {
			t.Fatalf("chain[%d].UpstreamModel = %q, want %q", i, chain[i].UpstreamModel, model)
		}
	}
}
