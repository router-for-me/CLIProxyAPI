package cliproxy

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// A per-auth rename and a global fork that share one client-visible alias must BOTH
// survive the merge: the fork registers the id, the rename decides what goes upstream.
// Before the (name, alias) key, the per-auth entry evicted the fork and the id was never
// routable. Exercised live with Codex "gpt-reserve" on 2026-09-02.
func TestOAuthModelAliasesForAuthKeepsForkAlongsidePerAuthRename(t *testing.T) {
	cfg := &config.Config{OAuthModelAlias: map[string][]config.OAuthModelAlias{
		"codex": {{Name: "gpt-5.6-luna", Alias: "gpt-5.6-luna-reserve", Fork: true}},
	}}
	attrs := map[string]string{
		"model_aliases": `[{"name":"gpt-reserve","alias":"gpt-5.6-luna-reserve"}]`,
	}
	got := oauthModelAliasesForAuth(cfg, "codex", attrs)
	if len(got) != 2 {
		t.Fatalf("expected both entries to survive, got %d: %+v", len(got), got)
	}
	if got[0].Name != "gpt-reserve" || got[1].Name != "gpt-5.6-luna" || !got[1].Fork {
		t.Fatalf("expected per-auth rename first then global fork, got %+v", got)
	}
}

// A global rename (not a fork) that shares its alias with a per-auth entry is still dropped:
// keeping it would rename the catalog from the global source while the credential routes
// the id to its own upstream.
func TestOAuthModelAliasesForAuthDropsShadowedGlobalRename(t *testing.T) {
	cfg := &config.Config{OAuthModelAlias: map[string][]config.OAuthModelAlias{
		"codex": {{Name: "gpt-5.6-luna", Alias: "shared"}},
	}}
	attrs := map[string]string{"model_aliases": `[{"name":"gpt-reserve","alias":"shared"}]`}
	got := oauthModelAliasesForAuth(cfg, "codex", attrs)
	if len(got) != 1 || got[0].Name != "gpt-reserve" {
		t.Fatalf("expected only the per-auth entry, got %+v", got)
	}
}

// The pair key is a struct, not a joined string, so names and aliases that themselves
// contain the joiner cannot collide: {a->b, c} and {a, b->c} are different pairs.
func TestOAuthModelAliasesForAuthPairKeyIsCollisionFree(t *testing.T) {
	cfg := &config.Config{OAuthModelAlias: map[string][]config.OAuthModelAlias{
		"codex": {{Name: "a", Alias: "b->c", Fork: true}},
	}}
	attrs := map[string]string{"model_aliases": `[{"name":"a->b","alias":"c"}]`}
	got := oauthModelAliasesForAuth(cfg, "codex", attrs)
	if len(got) != 2 {
		t.Fatalf("expected both pairs to survive, got %d: %+v", len(got), got)
	}
}

// Identical (name, alias) pairs still collapse, with the per-auth copy winning.
func TestOAuthModelAliasesForAuthStillDedupesSamePair(t *testing.T) {
	cfg := &config.Config{OAuthModelAlias: map[string][]config.OAuthModelAlias{
		"codex": {{Name: "gpt-5.6-luna", Alias: "luna-fast"}},
	}}
	attrs := map[string]string{"model_aliases": `[{"name":"gpt-5.6-luna","alias":"luna-fast","force-mapping":true}]`}
	got := oauthModelAliasesForAuth(cfg, "codex", attrs)
	if len(got) != 1 || !got[0].ForceMapping {
		t.Fatalf("expected one entry, the per-auth one, got %+v", got)
	}
}
