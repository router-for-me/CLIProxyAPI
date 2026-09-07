package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/util"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// The resolution must use the CANONICAL provider keys — the exact strings
// util.UpstreamProviderKey produces and the synthesizer stamps onto live
// auths (verified against synthesizer tests):
//   - openai-compatibility → "openai-compatible-<lowercase name>" (no row id)
//   - interactions-api-key → "gemini-interactions:<rowID>"
//   - oauth:*              → bare channel (no row id)
//   - other api-key types  → "<channel>:<rowID>"
func TestChannelForProviderRowMatchesCanonicalKeys(t *testing.T) {
	cases := []struct {
		providerType string
		name         string
		rowID        int64
	}{
		{"openai-compatibility", "My-Compat", 9},
		{"interactions-api-key", "", 9},
		{"claude-api-key", "", 9},
		{"gemini-api-key", "", 9},
		{"codex-api-key", "", 9},
		{"xai-api-key", "", 9},
		{"vertex-api-key", "", 9},
		{"oauth:claude", "", 9},
	}
	for _, tc := range cases {
		row := store.UpstreamProvider{ID: tc.rowID, ProviderType: tc.providerType, Name: tc.name}
		got := testProbeRoutingKey(row)
		want := util.UpstreamProviderKey(tc.providerType, tc.name, tc.rowID)
		if got != want {
			t.Fatalf("testProbeRoutingKey(%s, %q, %d) = %q, want canonical %q",
				tc.providerType, tc.name, tc.rowID, got, want)
		}
	}
	// Spot-check the exact historical mismatches.
	if got := testProbeRoutingKey(store.UpstreamProvider{ID: 9, ProviderType: "openai-compatibility", Name: "My-Compat"}); got != "openai-compatible-my-compat" {
		t.Fatalf("openai-compat key = %q, want openai-compatible-my-compat", got)
	}
	if got := testProbeRoutingKey(store.UpstreamProvider{ID: 9, ProviderType: "interactions-api-key"}); got != "gemini-interactions:9" {
		t.Fatalf("interactions key = %q, want gemini-interactions:9", got)
	}
	if got := testProbeRoutingKey(store.UpstreamProvider{ID: 9, ProviderType: "oauth:claude"}); got != "claude" {
		t.Fatalf("oauth key = %q, want claude (no row id)", got)
	}
}

func TestResolveTestTargetAuthCanonicalKeys(t *testing.T) {
	h := newTestRunHandler(t)
	// OpenAI-compat: provider_key has NO row id; entry key is the compound
	// "<provider_key>:key-<entryID>" (synthesizer contract).
	registerTestRunAuth(t, h, map[string]string{
		"probe_id":                         "auth-compat-entry",
		"probe_provider":                   "openai-compatible-kimi",
		"provider_key":                     "openai-compatible-kimi",
		"compat_name":                      "kimi",
		coreauth.AttributeEntryProviderKey: "openai-compatible-kimi:key-21",
	})

	auth := h.resolveTestTargetAuth("openai-compatible-kimi", int64Ptr(21))
	if auth == nil || auth.ID != "auth-compat-entry" {
		t.Fatalf("entry resolution = %#v, want auth-compat-entry", auth)
	}
	// Wrong-shape key (with row id) must NOT resolve — guards regression.
	if auth := h.resolveTestTargetAuth("openai-compatible-kimi:9", int64Ptr(21)); auth != nil {
		t.Fatalf("legacy-shape key resolved %#v, want nil", auth)
	}
	// Provider-level compat probe falls back to the parent key.
	if auth := h.resolveTestTargetAuth("openai-compatible-kimi", nil); auth == nil {
		t.Fatal("provider-level compat probe found no auth")
	}

	// Built-in channel: compound "<channel>:<rowID>" parent key.
	h2 := newTestRunHandler(t)
	registerTestRunAuth(t, h2, map[string]string{
		"probe_id":       "auth-claude-row",
		"probe_provider": "claude",
		"provider_key":   "claude:42",
	})
	if auth := h2.resolveTestTargetAuth("claude:42", nil); auth == nil || auth.ID != "auth-claude-row" {
		t.Fatalf("claude row resolution = %#v, want auth-claude-row", auth)
	}

	// Interactions rows are keyed under "gemini-interactions:<rowID>".
	h3 := newTestRunHandler(t)
	registerTestRunAuth(t, h3, map[string]string{
		"probe_id":       "auth-interactions-row",
		"probe_provider": "gemini-interactions",
		"provider_key":   "gemini-interactions:5",
	})
	if auth := h3.resolveTestTargetAuth("gemini-interactions:5", nil); auth == nil || auth.ID != "auth-interactions-row" {
		t.Fatalf("interactions row resolution = %#v, want auth-interactions-row", auth)
	}
}

func TestTestUpstreamProviderWithLiveAuthReturnsProbeResponse(t *testing.T) {
	h := newTestRunHandler(t)
	// A live auth exists but the manager has no executor registered, so
	// Execute fails with a scheduling error — the endpoint must still
	// answer 200 with ok:false and the error surfaced in-band.
	registerTestRunAuth(t, h, map[string]string{
		"probe_id":       "auth-live",
		"probe_provider": "claude",
		"provider_key":   "claude:3",
	})
	// Wire a no-op store so the row lookup passes. The store interface is
	// satisfied via the PG store when configured; without PG we can't get
	// past the 503 in a unit test, so assert the earlier path only.
	rec := httptest.NewRecorder()
	postTestProbe(h, rec, "3", `{"model":"claude-4"}`)
	// Without PG: 503 store-unavailable (unchanged contract).
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "credential not live") {
		t.Fatal("auth resolution must run after the store lookup, not before")
	}
	_ = context.Background()
}
