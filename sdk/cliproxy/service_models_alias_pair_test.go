package cliproxy

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// catalogOf is the set of upstream ids the catalog holds, as applyOAuthModelAliasForAuth
// derives it from the model list.
func catalogOf(ids ...string) map[string]struct{} {
	return catalogModelIDs(func() []*ModelInfo {
		out := make([]*ModelInfo, 0, len(ids))
		for _, id := range ids {
			out = append(out, &ModelInfo{ID: id})
		}
		return out
	}())
}

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
	catalog := catalogOf("gpt-5.6-luna")
	got := oauthModelAliasesForAuth(cfg, "codex", attrs, catalog, nil)
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
	catalog := catalogOf("gpt-5.6-luna")
	got := oauthModelAliasesForAuth(cfg, "codex", attrs, catalog, nil)
	if len(got) != 1 || got[0].Name != "gpt-reserve" {
		t.Fatalf("expected only the per-auth entry, got %+v", got)
	}
}

// When the per-auth source model is itself in the catalog, the global fork is not needed
// and would win catalog order over the per-auth rename, leaving the per-auth source
// exposed under its own id while requests for the alias route to it. Drop the fork.
func TestOAuthModelAliasesForAuthDropsGlobalForkWhenPerAuthSourceIsInCatalog(t *testing.T) {
	cfg := &config.Config{OAuthModelAlias: map[string][]config.OAuthModelAlias{
		"codex": {{Name: "gpt-5.6-luna", Alias: "gpt-5.6-luna-reserve", Fork: true}},
	}}
	attrs := map[string]string{"model_aliases": `[{"name":"gpt-reserve","alias":"gpt-5.6-luna-reserve"}]`}
	catalog := catalogOf("gpt-5.6-luna", "gpt-reserve")
	got := oauthModelAliasesForAuth(cfg, "codex", attrs, catalog, nil)
	if len(got) != 1 || got[0].Name != "gpt-reserve" {
		t.Fatalf("expected only the per-auth rename, got %+v", got)
	}
	// End to end: the catalog ends up with luna, and reserve renamed to the alias, once.
	models := []*ModelInfo{{ID: "gpt-5.6-luna"}, {ID: "gpt-reserve"}}
	out := applyOAuthModelAliasForAuth(cfg, "codex", "oauth", attrs, nil, models)
	ids := make([]string, 0, len(out))
	for _, m := range out {
		ids = append(ids, m.ID)
	}
	if len(ids) != 2 || ids[0] != "gpt-5.6-luna" || ids[1] != "gpt-5.6-luna-reserve" {
		t.Fatalf("expected [gpt-5.6-luna gpt-5.6-luna-reserve], got %v", ids)
	}
}

// A per-auth source removed by oauth-excluded-models must not read as a hidden model. If the
// global fork survived, X would be registered from the fork while request-time resolution
// (per-auth first) sent X to the excluded model. Both the merge and the end-to-end catalog
// pin that the fork is dropped and X is not registered.
func TestOAuthModelAliasesForAuthDropsGlobalForkWhenPerAuthSourceIsExcluded(t *testing.T) {
	cfg := &config.Config{OAuthModelAlias: map[string][]config.OAuthModelAlias{
		"codex": {{Name: "gpt-5.6-luna", Alias: "gpt-5.6-luna-reserve", Fork: true}},
	}}
	attrs := map[string]string{
		"model_aliases":   `[{"name":"gpt-reserve","alias":"gpt-5.6-luna-reserve"}]`,
		"excluded_models": "gpt-reserve",
	}
	// The catalog after exclusions: gpt-reserve is gone, exactly as a hidden model would be.
	got := oauthModelAliasesForAuth(cfg, "codex", attrs, catalogOf("gpt-5.6-luna"), []string{"gpt-reserve"})
	if len(got) != 1 || got[0].Name != "gpt-reserve" {
		t.Fatalf("expected the fork to be dropped, got %+v", got)
	}
	// The exclusion may come from the global oauth-excluded-models table rather than the
	// attribute, as for an SDK-created credential; the caller passes whatever it filtered with.
	delete(attrs, "excluded_models")
	excluded := []string{"gpt-reserve"}
	models := applyExcludedModels([]*ModelInfo{{ID: "gpt-5.6-luna"}, {ID: "gpt-reserve"}}, excluded)
	out := applyOAuthModelAliasForAuth(cfg, "codex", "oauth", attrs, excluded, models)
	for _, m := range out {
		if m.ID == "gpt-5.6-luna-reserve" {
			t.Fatalf("excluded source must not be reachable through the alias, got %+v", out)
		}
	}
}

// The exclusion may name the client-visible alias rather than the source. Exclusions run
// before aliases, so a surviving fork would recreate the excluded id from the global source
// while requests for it route to the per-auth upstream. The fork is dropped for that too.
func TestOAuthModelAliasesForAuthDropsGlobalForkWhenAliasIsExcluded(t *testing.T) {
	cfg := &config.Config{OAuthModelAlias: map[string][]config.OAuthModelAlias{
		"codex": {{Name: "gpt-5.6-luna", Alias: "gpt-5.6-luna-reserve", Fork: true}},
	}}
	attrs := map[string]string{"model_aliases": `[{"name":"gpt-reserve","alias":"gpt-5.6-luna-reserve"}]`}
	excluded := []string{"gpt-5.6-luna-*"}
	got := oauthModelAliasesForAuth(cfg, "codex", attrs, catalogOf("gpt-5.6-luna"), excluded)
	if len(got) != 1 || got[0].Name != "gpt-reserve" {
		t.Fatalf("expected the fork to be dropped, got %+v", got)
	}
	models := applyExcludedModels([]*ModelInfo{{ID: "gpt-5.6-luna"}}, excluded)
	out := applyOAuthModelAliasForAuth(cfg, "codex", "oauth", attrs, excluded, models)
	for _, m := range out {
		if m.ID == "gpt-5.6-luna-reserve" {
			t.Fatalf("excluded alias must not be recreated, got %+v", out)
		}
	}
}

// A per-auth source supplied by a static plugin is appended after the alias pass, so it is
// absent from the catalog the alias pass sees. The caller passes the ids it will end up
// serving, plugin models included, so a plugin-backed source is not read as hidden.
func TestOAuthModelAliasesForAuthSeesPluginSourcesAsPresent(t *testing.T) {
	cfg := &config.Config{OAuthModelAlias: map[string][]config.OAuthModelAlias{
		"codex": {{Name: "gpt-5.6-luna", Alias: "gpt-5.6-luna-reserve", Fork: true}},
	}}
	attrs := map[string]string{"model_aliases": `[{"name":"gpt-reserve","alias":"gpt-5.6-luna-reserve"}]`}
	// Upstream catalog without the plugin model: the fork would be kept.
	if got := oauthModelAliasesForAuth(cfg, "codex", attrs, catalogOf("gpt-5.6-luna"), nil); len(got) != 2 {
		t.Fatalf("without the plugin source the fork is a needed fallback, got %+v", got)
	}
	// Final catalog including the plugin-supplied source: the fork is dropped.
	models := []*ModelInfo{{ID: "gpt-5.6-luna"}}
	out := applyOAuthModelAliasForAuthWithCatalog(cfg, "codex", "oauth", attrs, nil, models, catalogOf("gpt-5.6-luna", "gpt-reserve"))
	if len(out) != 1 || out[0].ID != "gpt-5.6-luna" {
		t.Fatalf("expected the fork dropped and no alias registered from the global source, got %+v", out)
	}
}

// The pair key is a struct, not a joined string, so names and aliases that themselves
// contain the joiner cannot collide: {a->b, c} and {a, b->c} are different pairs.
func TestOAuthModelAliasesForAuthPairKeyIsCollisionFree(t *testing.T) {
	cfg := &config.Config{OAuthModelAlias: map[string][]config.OAuthModelAlias{
		"codex": {{Name: "a", Alias: "b->c", Fork: true}},
	}}
	attrs := map[string]string{"model_aliases": `[{"name":"a->b","alias":"c"}]`}
	catalog := catalogOf("gpt-5.6-luna")
	got := oauthModelAliasesForAuth(cfg, "codex", attrs, catalog, nil)
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
	catalog := catalogOf("gpt-5.6-luna")
	got := oauthModelAliasesForAuth(cfg, "codex", attrs, catalog, nil)
	if len(got) != 1 || !got[0].ForceMapping {
		t.Fatalf("expected one entry, the per-auth one, got %+v", got)
	}
}
