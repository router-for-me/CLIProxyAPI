package auth

import (
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// TestCompileOrderedCandidatesFromConfig_PreservesConfigOrder proves AC#1+2:
// repeated alias entries for the same channel compile into an ordered candidate
// pool that preserves config order, and there is no round-robin rotation.
func TestCompileOrderedCandidatesFromConfig_PreservesConfigOrder(t *testing.T) {
	aliases := map[string][]internalconfig.OAuthModelAlias{
		"claude": {
			{Name: "claude-opus-4", Alias: "claude-sonnet-4"},
			{Name: "gpt-5", Alias: "claude-sonnet-4"},
			{Name: "kimi-k3", Alias: "claude-sonnet-4"},
		},
	}

	chain := compileOrderedCandidatesFromConfig(aliases, "claude", "claude-sonnet-4")
	if len(chain) != 3 {
		t.Fatalf("expected 3 ordered candidates, got %d: %+v", len(chain), chain)
	}
	wantModels := []string{"claude-opus-4", "gpt-5", "kimi-k3"}
	for i, want := range wantModels {
		if chain[i].UpstreamModel != want {
			t.Fatalf("chain[%d].UpstreamModel = %q, want %q (sequential order must be preserved)", i, chain[i].UpstreamModel, want)
		}
	}
	if chain[0].Channel != "claude" {
		t.Fatalf("chain[0].Channel = %q, want claude", chain[0].Channel)
	}
}

// TestCompileOrderedCandidatesFromConfig_NoCrossTalk proves that aliases for
// different channels do not leak into each other's chains.
func TestCompileOrderedCandidatesFromConfig_NoCrossTalk(t *testing.T) {
	aliases := map[string][]internalconfig.OAuthModelAlias{
		"claude": {
			{Name: "claude-opus-4", Alias: "claude-sonnet-4"},
		},
		"codex": {
			{Name: "gpt-5", Alias: "gpt-4"},
		},
	}
	if chain := compileOrderedCandidatesFromConfig(aliases, "claude", "gpt-4"); len(chain) != 0 {
		t.Fatalf("claude channel must not resolve codex aliases; got chain=%+v", chain)
	}
	if chain := compileOrderedCandidatesFromConfig(aliases, "codex", "claude-sonnet-4"); len(chain) != 0 {
		t.Fatalf("codex channel must not resolve claude aliases; got chain=%+v", chain)
	}
}

// TestCompileOrderedCandidatesFromConfig_Deterministic proves that two calls
// with the same config produce identical chains (no round-robin cursor).
func TestCompileOrderedCandidatesFromConfig_Deterministic(t *testing.T) {
	aliases := map[string][]internalconfig.OAuthModelAlias{
		"claude": {
			{Name: "claude-opus-4", Alias: "x"},
			{Name: "gpt-5", Alias: "x"},
			{Name: "kimi-k3", Alias: "x"},
		},
	}
	first := compileOrderedCandidatesFromConfig(aliases, "claude", "x")
	second := compileOrderedCandidatesFromConfig(aliases, "claude", "x")
	if len(first) != len(second) {
		t.Fatalf("non-deterministic chain length: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i] != second[i] {
			t.Fatalf("non-deterministic chain at index %d: %+v vs %+v", i, first[i], second[i])
		}
	}
}

// TestCompileOrderedCandidatesFromConfig_ThinkingSuffix proves the thinking
// suffix on the requested alias is preserved on every candidate's upstream
// model, but an upstream's own suffix always wins.
func TestCompileOrderedCandidatesFromConfig_ThinkingSuffix(t *testing.T) {
	aliases := map[string][]internalconfig.OAuthModelAlias{
		"claude": {
			{Name: "claude-opus-4", Alias: "claude-sonnet-4"},
			{Name: "gpt-5(high)", Alias: "claude-sonnet-4"}, // upstream's own suffix wins
		},
	}
	chain := compileOrderedCandidatesFromConfig(aliases, "claude", "claude-sonnet-4(8192)")
	if len(chain) != 2 {
		t.Fatalf("expected 2 candidates, got %d: %+v", len(chain), chain)
	}
	if chain[0].UpstreamModel != "claude-opus-4(8192)" {
		t.Fatalf("expected first candidate to inherit suffix, got %q", chain[0].UpstreamModel)
	}
	if chain[1].UpstreamModel != "gpt-5(high)" {
		t.Fatalf("expected second candidate to keep its own suffix, got %q", chain[1].UpstreamModel)
	}
}

// TestResolveOrderedCandidates_ManagerBacked proves that a Manager configured
// via SetOAuthModelAlias resolves ordered candidates from its live table.
func TestResolveOrderedCandidates_ManagerBacked(t *testing.T) {
	m := newTestManager(t)
	m.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{
		"claude": {
			{Name: "claude-opus-4", Alias: "sonnet"},
			{Name: "gpt-5", Alias: "sonnet"},
		},
	})

	chain := m.resolveOrderedCandidates("claude", "sonnet")
	if len(chain) != 2 {
		t.Fatalf("expected 2 candidates from manager, got %d: %+v", len(chain), chain)
	}
	if chain[0].UpstreamModel != "claude-opus-4" || chain[1].UpstreamModel != "gpt-5" {
		t.Fatalf("unexpected chain order: %+v", chain)
	}

	// No chain for an unmapped alias.
	if chain := m.resolveOrderedCandidates("claude", "opus"); len(chain) != 0 {
		t.Fatalf("expected empty chain for unmapped alias, got %+v", chain)
	}
	// No chain for a different channel.
	if chain := m.resolveOrderedCandidates("codex", "sonnet"); len(chain) != 0 {
		t.Fatalf("expected empty chain for foreign channel, got %+v", chain)
	}
}

// newTestManager builds a minimal Manager suitable for ordered-pool unit tests.
// It avoids loading real auths/executors and only wires the alias table.
func newTestManager(t *testing.T) *Manager {
	t.Helper()
	m := &Manager{}
	m.oauthModelAlias.Store(&oauthModelAliasTable{})
	return m
}
