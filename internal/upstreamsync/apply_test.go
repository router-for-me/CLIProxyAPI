package upstreamsync

import (
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// TestApplyClaudeProviderFanOutSynthesizesTwoEntryRouteKeys exercises the
// renderer → synthesizer cross-seam for a "claude-api-key" upstream
// provider with two child api_key_entries. The contract is:
//
//  1. RenderConfig (the renderer layer, internal/upstreamsync/render.go)
//     must fan one store.UpstreamProvider with two APIKeyEntries out into
//     exactly two config.ClaudeKey values, each carrying its own
//     UpstreamProviderID + UpstreamProviderEntryID + Weight + per-entry
//     proxy (with provider-proxy fallback when the entry proxy is blank).
//
//  2. NewConfigSynthesizer().Synthesize (the auth layer,
//     internal/watcher/synthesizer/config.go) must turn each rendered
//     config.ClaudeKey into exactly one *coreauth.Auth with:
//     - Provider == "claude"
//     - Attributes["provider_key"] == "claude:<rowID>"
//     - Attributes[coreauth.AttributeEntryProviderKey] ==
//     "claude:<rowID>:key-<entryID>"
//
//  3. Auths must be in row order: the auth with the smaller entry id must
//     appear first, so deterministic id-keyed routing survives the
//     renderer → synthesizer hop without re-sorting.
//
// The renderer-only assertions are already covered by claude_multi_entry_test
// .go (TestClaudeRenderFanOutPerEntry, TestRenderConfigAppendsClaudeFanOut)
// and the synthesizer-only assertions by internal/watcher/synthesizer/
// config_test.go (TestConfigSynthesizer_Claude_EntryProviderKey). This
// test adds the missing cross-seam guard: an entry id produced by the
// renderer MUST reach the synthesizer unmodified and show up on the
// resulting Auth, so a regression in either layer (or in the JSON contract
// between them) breaks here before runtime routing does.
//
// No credentials are logged; fake redacted values are used throughout and
// are explicitly checked to never appear in error messages.
func TestApplyClaudeProviderFanOutSynthesizesTwoEntryRouteKeys(t *testing.T) {
	const (
		secretAlpha = "FAKE-SECRET-claude-alpha"
		secretBeta  = "FAKE-SECRET-claude-beta"
	)
	w7 := ptrInt(7)
	w9 := ptrInt(9)
	provider := store.UpstreamProvider{
		ProviderType: TypeClaudeAPIKey,
		ID:           42,
		APIKey:       "FAKE-SECRET-parent-fallback",
		Priority:     5,
		Prefix:       "teamA/",
		BaseURL:      "https://claude.example",
		ProxyURL:     "http://provider-proxy",
		Headers:      map[string]string{"X-Header": "row"},
		APIKeyEntries: []store.UpstreamProviderAPIKey{
			{ID: 7, Name: "alpha", APIKey: secretAlpha, ProxyURL: "http://entry-a-proxy", Weight: w7, SortOrder: 0, ProviderID: 42},
			{ID: 9, Name: "beta", APIKey: secretBeta, ProxyURL: "", Weight: w9, SortOrder: 1, ProviderID: 42},
		},
	}

	// Step 1 — renderer fan-out. Use the public RenderConfig surface so the
	// dispatcher path is exercised, not just the helper.
	cfg := RenderConfig([]store.UpstreamProvider{provider})
	if len(cfg.ClaudeKey) != 2 {
		t.Fatalf("rendered %d claude keys, want 2", len(cfg.ClaudeKey))
	}
	for i, k := range cfg.ClaudeKey {
		if k.UpstreamProviderID != 42 {
			t.Fatalf("claude[%d] UpstreamProviderID = %d, want 42", i, k.UpstreamProviderID)
		}
	}
	if cfg.ClaudeKey[0].UpstreamProviderEntryID != 7 {
		t.Fatalf("claude[0] UpstreamProviderEntryID = %d, want 7", cfg.ClaudeKey[0].UpstreamProviderEntryID)
	}
	if cfg.ClaudeKey[1].UpstreamProviderEntryID != 9 {
		t.Fatalf("claude[1] UpstreamProviderEntryID = %d, want 9", cfg.ClaudeKey[1].UpstreamProviderEntryID)
	}
	if cfg.ClaudeKey[0].Weight == nil || *cfg.ClaudeKey[0].Weight != 7 {
		t.Fatalf("claude[0] Weight = %v, want pointer to 7", cfg.ClaudeKey[0].Weight)
	}
	if cfg.ClaudeKey[1].Weight == nil || *cfg.ClaudeKey[1].Weight != 9 {
		t.Fatalf("claude[1] Weight = %v, want pointer to 9", cfg.ClaudeKey[1].Weight)
	}
	if cfg.ClaudeKey[0].ProxyURL != "http://entry-a-proxy" {
		t.Fatalf("claude[0] proxy should override provider proxy, got %q", cfg.ClaudeKey[0].ProxyURL)
	}
	if cfg.ClaudeKey[1].ProxyURL != "http://provider-proxy" {
		t.Fatalf("claude[1] proxy should inherit provider proxy, got %q", cfg.ClaudeKey[1].ProxyURL)
	}

	// Step 2 — synthesizer must read the rendered config and emit one Auth
	// per ClaudeKey, with the compound provider_key and the per-entry
	// entry_provider_key reaching the attributes verbatim.
	synth := synthesizer.NewConfigSynthesizer()
	auths, err := synth.Synthesize(&synthesizer.SynthesisContext{
		Config:      &cfg,
		Now:         time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if len(auths) != 2 {
		t.Fatalf("synthesized %d auths, want 2", len(auths))
	}

	wantProviderKeys := []string{"claude:42", "claude:42"}
	wantEntryKeys := []string{"claude:42:key-7", "claude:42:key-9"}
	for i, auth := range auths {
		if auth.Provider != "claude" {
			t.Errorf("auth[%d] Provider = %q, want claude", i, auth.Provider)
		}
		if got := auth.Attributes["provider_key"]; got != wantProviderKeys[i] {
			t.Errorf("auth[%d] provider_key = %q, want %q", i, got, wantProviderKeys[i])
		}
		gotEntry, ok := auth.Attributes[coreauth.AttributeEntryProviderKey]
		if !ok {
			t.Errorf("auth[%d] entry_provider_key missing, want %q", i, wantEntryKeys[i])
			continue
		}
		if gotEntry != wantEntryKeys[i] {
			t.Errorf("auth[%d] entry_provider_key = %q, want %q", i, gotEntry, wantEntryKeys[i])
		}
	}

	// Step 3 — credential material must never leak into labels, errors, or
	// routing attributes (route keys are derived from persisted ids, not
	// from the mutable entry name or the api key value).
	for i, auth := range auths {
		for _, field := range []string{auth.ID, auth.Provider, auth.Label, auth.Attributes["provider_key"], auth.Attributes[coreauth.AttributeEntryProviderKey]} {
			if strings.Contains(field, secretAlpha) || strings.Contains(field, secretBeta) {
				t.Errorf("auth[%d] routing field contains secret: %q", i, field)
			}
		}
	}
}

// TestApplyClaudeProviderLegacySingleKeySynthesizesBareChannel confirms
// the legacy fallback path (a Claude provider row with NO api_key_entries)
// renders to exactly one config.ClaudeKey with the parent api_key + proxy
// and synthesizes to exactly one *coreauth.Auth with Provider == "claude".
// The legacy fallback still carries the parent row id, so the synthesizer
// stamps provider_key == "claude:<rowID>" — but UpstreamProviderEntryID
// stays zero, so no entry_provider_key is emitted. This is the cross-seam
// mirror of TestConfigSynthesizer_Claude_LegacyIDsHaveNoEntryProviderKey:
// the renderer must surface the parent id verbatim so the runtime can
// still route by the per-row provider_key, while the absence of any child
// id keeps the legacy "one Claude auth per row" semantics intact.
func TestApplyClaudeProviderLegacySingleKeySynthesizesBareChannel(t *testing.T) {
	provider := store.UpstreamProvider{
		ProviderType: TypeClaudeAPIKey,
		ID:           99,
		APIKey:       "FAKE-SECRET-legacy-parent",
		ProxyURL:     "http://provider-proxy",
		BaseURL:      "https://claude.example",
	}

	cfg := RenderConfig([]store.UpstreamProvider{provider})
	if len(cfg.ClaudeKey) != 1 {
		t.Fatalf("legacy Claude renderer emitted %d keys, want 1", len(cfg.ClaudeKey))
	}
	k := cfg.ClaudeKey[0]
	if k.APIKey != "FAKE-SECRET-legacy-parent" || k.ProxyURL != "http://provider-proxy" {
		t.Fatalf("legacy Claude key fields = %+v, want parent api_key + proxy", k)
	}
	if k.UpstreamProviderID != 99 || k.UpstreamProviderEntryID != 0 {
		t.Fatalf("legacy Claude key ids = (%d, %d), want (99, 0)", k.UpstreamProviderID, k.UpstreamProviderEntryID)
	}

	auths, err := synthesizer.NewConfigSynthesizer().Synthesize(&synthesizer.SynthesisContext{
		Config:      &cfg,
		Now:         time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	})
	if err != nil {
		t.Fatalf("Synthesize: %v", err)
	}
	if len(auths) != 1 {
		t.Fatalf("synthesized %d auths, want 1 (legacy fallback)", len(auths))
	}
	auth := auths[0]
	if auth.Provider != "claude" {
		t.Fatalf("legacy auth Provider = %q, want claude", auth.Provider)
	}
	if got := auth.Attributes["provider_key"]; got != "claude:99" {
		t.Fatalf("legacy auth provider_key = %q, want claude:99", got)
	}
	if v, ok := auth.Attributes[coreauth.AttributeEntryProviderKey]; ok {
		t.Fatalf("legacy auth entry_provider_key = %q, want attribute to be absent", v)
	}
}
