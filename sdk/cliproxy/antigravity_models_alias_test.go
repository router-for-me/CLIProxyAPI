package cliproxy

import (
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func antigravityAuth(aliases string) *coreauth.Auth {
	return &coreauth.Auth{
		ID:       "antigravity-alias-test",
		Provider: "antigravity",
		Attributes: map[string]string{
			"auth_kind":     coreauth.AuthKindOAuth,
			"model_aliases": aliases,
		},
	}
}

// The capability probe maps a registered id back to the upstream the credential is actually
// sent. The (name, alias) merge can return a per-auth rename and a global fork under one
// alias; request-time resolution consults the per-auth entry, so the reverse map must too.
func TestBuildAntigravityReverseAliasMapPrefersPerAuthEntry(t *testing.T) {
	cfg := &config.Config{OAuthModelAlias: map[string][]config.OAuthModelAlias{
		"antigravity": {{Name: "gemini-3-pro", Alias: "gemini-3-pro-preview", Fork: true}},
	}}
	auth := antigravityAuth(`[{"name":"gemini-3-pro-internal","alias":"gemini-3-pro-preview"}]`)
	got := (&Service{cfg: cfg}).buildAntigravityReverseAliasMap(auth)
	if got["gemini-3-pro-preview"] != "gemini-3-pro-internal" {
		t.Fatalf("alias resolved to %q, want the per-auth upstream gemini-3-pro-internal", got["gemini-3-pro-preview"])
	}
}

// A global fork with no per-auth entry for its alias must stay in the map: the id exists only
// because the fork registered it.
func TestBuildAntigravityReverseAliasMapKeepsUnshadowedGlobalFork(t *testing.T) {
	cfg := &config.Config{OAuthModelAlias: map[string][]config.OAuthModelAlias{
		"antigravity": {{Name: "gemini-3-pro", Alias: "gemini-3-pro-preview", Fork: true}},
	}}
	auth := antigravityAuth(`[{"name":"gemini-3-flash-internal","alias":"gemini-3-flash"}]`)
	got := (&Service{cfg: cfg}).buildAntigravityReverseAliasMap(auth)
	if got["gemini-3-pro-preview"] != "gemini-3-pro" {
		t.Fatalf("forked alias resolved to %q, want gemini-3-pro", got["gemini-3-pro-preview"])
	}
	if got["gemini-3-flash"] != "gemini-3-flash-internal" {
		t.Fatalf("per-auth alias resolved to %q, want gemini-3-flash-internal", got["gemini-3-flash"])
	}
	// resolveAntigravityUpstreamModelID strips the credential prefix before the lookup.
	if id := resolveAntigravityUpstreamModelID("ag/gemini-3-pro-preview", "ag", got); id != "gemini-3-pro" {
		t.Fatalf("prefixed id resolved to %q, want gemini-3-pro", id)
	}
}
