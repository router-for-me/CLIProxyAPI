package upstreamsync

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

func poolID(v int64) *int64 { return &v }

// TestBuildClaudeKeyResolvesStandardPool verifies the composite URL lands on
// the rendered key when the entry binds an active standard pool.
func TestBuildClaudeKeyResolvesStandardPool(t *testing.T) {
	look := poolLookup{7: {ID: 7, Name: "p", ProxyURL: "socks5://10.0.0.1:1080", NoProxy: ".corp, x.example", IsActive: true, StrictProxy: true}}
	provider := store.UpstreamProvider{ID: 1, ProviderType: "claude-api-key", APIKey: "k"}
	entry := store.UpstreamProviderAPIKey{APIKey: "k", ProxyPoolID: poolID(7)}

	key := buildClaudeKeyWithPools(provider, entry, look)
	want := "socks5://10.0.0.1:1080?no_proxy=.corp%2Cx.example&strict=true"
	if key.ProxyURL != want {
		t.Fatalf("ProxyURL = %q, want %q", key.ProxyURL, want)
	}
	if key.RelayBaseURL != "" {
		t.Fatalf("relay must be empty for standard pool, got %q", key.RelayBaseURL)
	}
}

func TestBuildClaudeKeyResolvesRelayPool(t *testing.T) {
	look := poolLookup{8: {ID: 8, ProxyURL: "https://myrelay.workers.dev", Type: "cloudflare", IsActive: true}}
	key := buildClaudeKeyWithPools(store.UpstreamProvider{ID: 1, ProviderType: "claude-api-key", APIKey: "k"},
		store.UpstreamProviderAPIKey{APIKey: "k", ProxyPoolID: poolID(8)}, look)
	if key.ProxyURL != "" {
		t.Fatalf("standard proxy must stay empty for relay pool, got %q", key.ProxyURL)
	}
	if key.RelayBaseURL != "https://myrelay.workers.dev" {
		t.Fatalf("RelayBaseURL = %q", key.RelayBaseURL)
	}
}

// TestBuildClaudeKeyLooseStrictRendering pins strict=false rendering.
func TestBuildClaudeKeyLooseStrictRendering(t *testing.T) {
	look := poolLookup{7: {ID: 7, ProxyURL: "http://a:1", IsActive: true, StrictProxy: false}}
	key := buildClaudeKeyWithPools(store.UpstreamProvider{ID: 1, ProviderType: "claude-api-key", APIKey: "k"},
		store.UpstreamProviderAPIKey{APIKey: "k", ProxyPoolID: poolID(7)}, look)
	if key.ProxyURL != "http://a:1?strict=false" {
		t.Fatalf("ProxyURL = %q, want loose suffix", key.ProxyURL)
	}
}

func TestBuildClaudeKeyInactiveOrMissingPoolFallsThrough(t *testing.T) {
	look := poolLookup{9: {ID: 9, ProxyURL: "http://x:1", IsActive: false}}
	// row-level manual proxy must win when the pool is inactive
	key := buildClaudeKeyWithPools(
		store.UpstreamProvider{ID: 1, ProviderType: "claude-api-key", APIKey: "row", ProxyURL: "http://row:1"},
		store.UpstreamProviderAPIKey{APIKey: "k", ProxyPoolID: poolID(9)}, look)
	if key.ProxyURL != "http://row:1" {
		t.Fatalf("inactive pool must fall through to row proxy, got %q", key.ProxyURL)
	}
	// missing pool id (deleted) also falls through
	key = buildClaudeKeyWithPools(store.UpstreamProvider{ID: 1, ProviderType: "claude-api-key", APIKey: "row"},
		store.UpstreamProviderAPIKey{APIKey: "k", ProxyPoolID: poolID(999)}, look)
	if key.ProxyURL != "" {
		t.Fatalf("missing pool must yield empty, got %q", key.ProxyURL)
	}
}

// TestBuildClaudeKeyEntryOverridesRowPool: entry binding wins over row binding.
func TestBuildClaudeKeyEntryOverridesRowPool(t *testing.T) {
	look := poolLookup{
		7: {ID: 7, ProxyURL: "http://entry-pool:1", IsActive: true, StrictProxy: true},
		8: {ID: 8, ProxyURL: "http://row-pool:1", IsActive: true},
	}
	key := buildClaudeKeyWithPools(
		store.UpstreamProvider{ID: 1, ProviderType: "claude-api-key", APIKey: "k", ProxyPoolID: poolID(8)},
		store.UpstreamProviderAPIKey{APIKey: "k", ProxyPoolID: poolID(7)}, look)
	if key.ProxyURL != "http://entry-pool:1?strict=true" {
		t.Fatalf("entry pool must win, got %q", key.ProxyURL)
	}
}

// TestOpenAICompatResolvesEntryPools mirrors the claude assertions on the
// compat shape (entries carry their own ProxyURL/RelayBaseURL; row-level
// pool is the default when an entry leaves proxy_pool_id nil).
func TestOpenAICompatResolvesEntryPools(t *testing.T) {
	look := poolLookup{
		7: {ID: 7, ProxyURL: "http://a:1", IsActive: true, StrictProxy: false},
		8: {ID: 8, ProxyURL: "https://relay.deno.net", Type: "deno", IsActive: true},
	}
	provider := store.UpstreamProvider{
		ID: 1, ProviderType: "openai-compatibility", Name: "compat", ProxyPoolID: poolID(7),
		APIKeyEntries: []store.UpstreamProviderAPIKey{
			{APIKey: "k1"},                         // inherits row pool
			{APIKey: "k2", ProxyPoolID: poolID(8)}, // relay override
		},
	}
	got := openAICompatFromProviderWithPools(provider, look)
	if got.ProxyURL != "http://a:1?strict=false" || got.RelayBaseURL != "" {
		t.Fatalf("row-level = %q / %q", got.ProxyURL, got.RelayBaseURL)
	}
	if got.APIKeyEntries[0].ProxyURL != "http://a:1?strict=false" {
		t.Fatalf("inherited entry = %q", got.APIKeyEntries[0].ProxyURL)
	}
	if got.APIKeyEntries[1].RelayBaseURL != "https://relay.deno.net" || got.APIKeyEntries[1].ProxyURL != "" {
		t.Fatalf("relay entry = %q / %q", got.APIKeyEntries[1].ProxyURL, got.APIKeyEntries[1].RelayBaseURL)
	}
}

// TestSeedRoundTripsPoolBinding: providerFromClaudeKey must carry ProxyPoolID
// back into the store row (first-boot seed path).
func TestSeedRoundTripsPoolBinding(t *testing.T) {
	id := int64(7)
	k := config.ClaudeKey{APIKey: "k", ProxyPoolID: &id}
	p := providerFromClaudeKey(k)
	if p.ProxyPoolID == nil || *p.ProxyPoolID != 7 {
		t.Fatalf("seed binding lost: %v", p.ProxyPoolID)
	}
}
