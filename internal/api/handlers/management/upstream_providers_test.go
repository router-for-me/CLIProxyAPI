package management

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

func TestUpstreamProviderEntryRequestRoundTrip(t *testing.T) {
	const payload = `{"provider_type":"openai-compatibility","api_key_entries":[{"id":42,"name":"Team-A","api_key":"entry-key","proxy_url":"http://proxy.example"}]}`

	var req upstreamProviderReq
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	got := toUpstreamProvider(&req)
	if len(got.APIKeyEntries) != 1 {
		t.Fatalf("got %d API key entries, want 1", len(got.APIKeyEntries))
	}
	entry := got.APIKeyEntries[0]
	if entry.ID != 42 || entry.Name != "Team-A" || entry.APIKey != "entry-key" || entry.ProxyURL != "http://proxy.example" {
		t.Fatalf("converted entry = %+v, want id/name/key/proxy preserved", entry)
	}

	encoded, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	if !strings.Contains(string(encoded), `"id":42`) || !strings.Contains(string(encoded), `"name":"Team-A"`) {
		t.Fatalf("encoded request omitted entry identity: %s", encoded)
	}
}

// TestUpstreamProviderEntryRequestWeightRoundTrip verifies the optional Weight
// field on upstreamProviderEntryReq: it must decode from JSON when present,
// map into store.UpstreamProviderAPIKey.Weight, omit cleanly when absent, and
// not appear in encoded output when nil (so the dashboard's "no weight" state
// stays distinct from "weight 0" once the picker is wired up).
func TestUpstreamProviderEntryRequestWeightRoundTrip(t *testing.T) {
	withWeight := `{"provider_type":"openai-compatibility","api_key_entries":[{"id":1,"name":"a","api_key":"k1","weight":42}]}`
	var withReq upstreamProviderReq
	if err := json.Unmarshal([]byte(withWeight), &withReq); err != nil {
		t.Fatalf("decode weighted request: %v", err)
	}
	if len(withReq.APIKeyEntries) != 1 {
		t.Fatalf("withWeight entries = %d, want 1", len(withReq.APIKeyEntries))
	}
	if withReq.APIKeyEntries[0].Weight == nil || *withReq.APIKeyEntries[0].Weight != 42 {
		t.Fatalf("withWeight entry weight = %v, want pointer to 42", withReq.APIKeyEntries[0].Weight)
	}
	gotWith := toUpstreamProvider(&withReq)
	if len(gotWith.APIKeyEntries) != 1 {
		t.Fatalf("gotWith entries = %d, want 1", len(gotWith.APIKeyEntries))
	}
	if gotWith.APIKeyEntries[0].Weight == nil || *gotWith.APIKeyEntries[0].Weight != 42 {
		t.Fatalf("gotWith entry weight = %v, want pointer to 42", gotWith.APIKeyEntries[0].Weight)
	}

	// Encode back and confirm the weight survives the round trip.
	encodedWith, err := json.Marshal(withReq)
	if err != nil {
		t.Fatalf("encode withWeight: %v", err)
	}
	if !strings.Contains(string(encodedWith), `"weight":42`) {
		t.Fatalf("encoded weighted request missing weight: %s", encodedWith)
	}

	// Omitted weight must decode as nil (not 0) and re-encode without emitting
	// a `weight` key — the dashboard distinguishes "no weight" from "weight 0".
	withoutWeight := `{"provider_type":"openai-compatibility","api_key_entries":[{"id":2,"name":"b","api_key":"k2"}]}`
	var withoutReq upstreamProviderReq
	if err := json.Unmarshal([]byte(withoutWeight), &withoutReq); err != nil {
		t.Fatalf("decode unweighted request: %v", err)
	}
	if withoutReq.APIKeyEntries[0].Weight != nil {
		t.Fatalf("withoutWeight entry weight = %v, want nil", withoutReq.APIKeyEntries[0].Weight)
	}
	gotWithout := toUpstreamProvider(&withoutReq)
	if gotWithout.APIKeyEntries[0].Weight != nil {
		t.Fatalf("gotWithout entry weight = %v, want nil", gotWithout.APIKeyEntries[0].Weight)
	}
	encodedWithout, err := json.Marshal(withoutReq)
	if err != nil {
		t.Fatalf("encode withoutWeight: %v", err)
	}
	if strings.Contains(string(encodedWithout), `"weight"`) {
		t.Fatalf("encoded unweighted request leaked weight key: %s", encodedWithout)
	}
}

// TestUpstreamProviderResponseExposesEntryWeight verifies that store
// rows with a non-nil Weight field surface through UpstreamProviderResponse
// JSON so the dashboard editor can read the persisted weight back. A nil
// Weight must not emit a `weight` key.
func TestUpstreamProviderResponseExposesEntryWeight(t *testing.T) {
	const existingResponseKey = "existing-response-key"
	weight := 99
	response := toUpstreamProviderResponse(store.UpstreamProvider{
		ID:           7,
		ProviderType: "openai-compatibility",
		Name:         "gateway",
		APIKeyEntries: []store.UpstreamProviderAPIKey{{
			ID:       42,
			Name:     "team-a",
			APIKey:   existingResponseKey,
			ProxyURL: "http://proxy.example",
			Weight:   &weight,
		}},
	})

	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("encode weighted response: %v", err)
	}
	if !strings.Contains(string(raw), `"weight":99`) {
		t.Fatalf("weighted response missing weight field: %s", raw)
	}
	if strings.Contains(string(raw), existingResponseKey) == false {
		t.Fatalf("weighted response stripped api key: %s", raw)
	}

	// Nil weight must not appear in JSON output.
	nilResponse := toUpstreamProviderResponse(store.UpstreamProvider{
		ID:           8,
		ProviderType: "openai-compatibility",
		Name:         "no-weight",
		APIKeyEntries: []store.UpstreamProviderAPIKey{{
			ID:     43,
			Name:   "team-b",
			APIKey: "no-weight-key",
		}},
	})
	nilRaw, err := json.Marshal(nilResponse)
	if err != nil {
		t.Fatalf("encode nil-weight response: %v", err)
	}
	if strings.Contains(string(nilRaw), `"weight"`) {
		t.Fatalf("nil-weight response emitted weight key: %s", nilRaw)
	}
}

func TestUpstreamProviderResponseExposesEntryIdentity(t *testing.T) {
	const existingResponseKey = "existing-response-key"
	response := toUpstreamProviderResponse(store.UpstreamProvider{
		ID:           7,
		ProviderType: "openai-compatibility",
		Name:         "gateway",
		APIKeyEntries: []store.UpstreamProviderAPIKey{{
			ID:       42,
			Name:     "team-a",
			APIKey:   existingResponseKey,
			ProxyURL: "http://proxy.example",
		}},
	})

	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("encode response: %v", err)
	}
	var decoded struct {
		APIKeyEntries []struct {
			ID       int64  `json:"id"`
			Name     string `json:"name"`
			APIKey   string `json:"api_key"`
			ProxyURL string `json:"proxy_url"`
		} `json:"api_key_entries"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(decoded.APIKeyEntries) != 1 {
		t.Fatalf("response has %d API key entries, want 1", len(decoded.APIKeyEntries))
	}
	entry := decoded.APIKeyEntries[0]
	if entry.ID != 42 || entry.Name != "team-a" || entry.ProxyURL != "http://proxy.example" {
		t.Fatalf("response entry identity = %+v, want id/name/proxy preserved", entry)
	}
	if entry.APIKey != existingResponseKey {
		t.Fatalf("response API key = %q, want existing response behavior", entry.APIKey)
	}
}

// TestToUpstreamProviderMapsRoutingStrategyAndEntryPriority covers the DTO
// boundary for pool routing: the row-level routing_strategy is canonicalized
// here (a raw "failover" from the dashboard is stored as "round-robin"), and
// each entry's optional priority pointer is copied verbatim (nil stays nil =
// inherit the row-level priority). The response side embeds the store row
// verbatim, so the canonical strategy and entry priorities round-trip to the
// dashboard editor.
func TestToUpstreamProviderMapsRoutingStrategyAndEntryPriority(t *testing.T) {
	intPtr := func(v int) *int { return &v }
	p := toUpstreamProvider(&upstreamProviderReq{
		ProviderType:    "claude-api-key",
		RoutingStrategy: "failover",
		APIKeyEntries: []upstreamProviderEntryReq{
			{APIKey: "k1", Priority: intPtr(10)},
			{APIKey: "k2"}, // inherit
		},
	})
	if p.RoutingStrategy != "round-robin" {
		t.Fatalf("RoutingStrategy = %q, want round-robin (failover canonicalized at the boundary)", p.RoutingStrategy)
	}
	if p.APIKeyEntries[0].Priority == nil || *p.APIKeyEntries[0].Priority != 10 {
		t.Fatalf("entry 0 priority = %#v, want *10", p.APIKeyEntries[0].Priority)
	}
	if p.APIKeyEntries[1].Priority != nil {
		t.Fatalf("entry 1 priority = %#v, want nil", p.APIKeyEntries[1].Priority)
	}

	// The persisted row must surface through the dashboard response with the
	// canonical strategy and the explicit priority; a nil priority must stay
	// absent (distinct from an explicit 0).
	raw, err := json.Marshal(toUpstreamProviderResponse(store.UpstreamProvider{
		ID:              1,
		ProviderType:    "claude-api-key",
		RoutingStrategy: p.RoutingStrategy,
		APIKeyEntries:   p.APIKeyEntries,
	}))
	if err != nil {
		t.Fatalf("encode response: %v", err)
	}
	encoded := string(raw)
	if !strings.Contains(encoded, `"routing_strategy":"round-robin"`) {
		t.Fatalf("response missing canonical routing_strategy: %s", encoded)
	}
	if !strings.Contains(encoded, `"priority":10`) {
		t.Fatalf("response missing entry priority: %s", encoded)
	}
	// The nil-priority entry must omit the key entirely (the row-level
	// `priority` scalar is always emitted by the store row; only the per-entry
	// projection is optional). Assert on the decoded entries rather than the
	// raw JSON suffix so the check stays stable against emission order and
	// other omitempty fields.
	var decoded struct {
		APIKeyEntries []struct {
			APIKey   string `json:"api_key"`
			Priority *int   `json:"priority"`
		} `json:"api_key_entries"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(decoded.APIKeyEntries) != 2 {
		t.Fatalf("decoded %d entries, want 2", len(decoded.APIKeyEntries))
	}
	if decoded.APIKeyEntries[1].APIKey != "k2" {
		t.Fatalf("decoded entry 1 api key = %q, want k2", decoded.APIKeyEntries[1].APIKey)
	}
	if decoded.APIKeyEntries[1].Priority != nil {
		t.Fatalf("nil-priority entry decoded priority = %#v, want nil", decoded.APIKeyEntries[1].Priority)
	}
}

// TestToUpstreamProviderMapsCircuitBreaker covers the bool passthrough at the
// DTO boundary (design G3): circuit_breaker true survives toUpstreamProvider
// untouched (no validation or canonicalization — it is a plain bool), and the
// response side surfaces the persisted value back to the dashboard editor.
func TestToUpstreamProviderMapsCircuitBreaker(t *testing.T) {
	p := toUpstreamProvider(&upstreamProviderReq{
		ProviderType:   "claude-api-key",
		CircuitBreaker: true,
	})
	if !p.CircuitBreaker {
		t.Fatal("CircuitBreaker = false, want true (plain passthrough)")
	}
	if p := toUpstreamProvider(&upstreamProviderReq{ProviderType: "claude-api-key"}); p.CircuitBreaker {
		t.Fatal("omitted CircuitBreaker = true, want false (default off)")
	}

	// The persisted row must surface the opt-in through the dashboard response.
	raw, err := json.Marshal(toUpstreamProviderResponse(store.UpstreamProvider{
		ID:             1,
		ProviderType:   "claude-api-key",
		CircuitBreaker: true,
	}))
	if err != nil {
		t.Fatalf("encode response: %v", err)
	}
	if !strings.Contains(string(raw), `"circuit_breaker":true`) {
		t.Fatalf("response missing circuit_breaker: %s", raw)
	}
}

// TestValidateUpstreamProviderRequestRoutingStrategy verifies the pre-store
// gate: an unknown strategy is rejected with the descriptive fixable error,
// while canonical values, the Model Routes aliases, and the global-normalizer
// shorthands pass through (they are canonicalized in toUpstreamProvider).
func TestValidateUpstreamProviderRequestRoutingStrategy(t *testing.T) {
	for _, raw := range []string{"bogus", "fill first", "fail-over"} {
		err := validateUpstreamProviderRequest(&upstreamProviderReq{RoutingStrategy: raw})
		if err == nil {
			t.Fatalf("validateUpstreamProviderRequest(%q) = nil, want error", raw)
		}
		if !strings.Contains(err.Error(), "invalid routing strategy") {
			t.Fatalf("error for %q = %v, want the descriptive invalid-routing-strategy message", raw, err)
		}
	}
	for _, raw := range []string{"", "   ", "failover", "priority", "round-robin", "weighted-round-robin", "wrr", "fill-first", "ff", "power-of-two-choices", "p2c", "two-random-choices", "least-used", "least-busy"} {
		if err := validateUpstreamProviderRequest(&upstreamProviderReq{RoutingStrategy: raw}); err != nil {
			t.Fatalf("validateUpstreamProviderRequest(%q) = %v, want nil", raw, err)
		}
	}
}

// intPtr is a small package-level helper for pointer-to-int literals in DTO
// tests (distinct from the local closures inside individual test functions).
func intPtr(v int) *int { return &v }

func TestUpstreamProviderErrorResponseEntryNameConflictIsSafe(t *testing.T) {
	const secretValue = "entry-secret-must-not-appear"
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/upstream-providers", nil)

	(&Handler{}).upstreamProviderErrorResponse(ctx, fmt.Errorf(
		"postgres store: duplicate upstream provider api key entry name: %s", secretValue,
	))

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusConflict, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "already exists") {
		t.Fatalf("conflict response = %s, want safe conflict message", body)
	}
	if strings.Contains(body, secretValue) {
		t.Fatalf("conflict response leaked API key value: %s", body)
	}
}

// TestUpstreamProviderClaudeAPIKeyTwoEntriesRequestResponse covers the
// management JSON envelope for a "claude-api-key" provider with two
// api_key_entries (with weights) end-to-end: the request body decodes into
// the management request struct, the in-memory store row preserves both
// entries (id, name, api_key, proxy_url, weight), and the rendered response
// (which the dashboard reads back after Create/Update) exposes both
// entries plus their weights. This locks the management seam before the
// renderer/synthesizer/runtime layers see the rows. Fake redacted credentials
// are used throughout; values never appear in any test failure message.
func TestUpstreamProviderClaudeAPIKeyTwoEntriesRequestResponse(t *testing.T) {
	const (
		createPayload = `{
			"provider_type": "claude-api-key",
			"prefix": "teamA/",
			"base_url": "https://claude.example",
			"proxy_url": "http://provider-proxy",
			"rebuild_mid_system_message": true,
			"experimental_cch_signing": true,
			"api_key_entries": [
				{"id": 7, "name": "alpha", "api_key": "FAKE-SECRET-claude-alpha", "proxy_url": "http://entry-a-proxy", "weight": 7},
				{"id": 9, "name": "beta",  "api_key": "FAKE-SECRET-claude-beta",  "proxy_url": "",                "weight": 9}
			]
		}`
		secretAlpha = "FAKE-SECRET-claude-alpha"
		secretBeta  = "FAKE-SECRET-claude-beta"
	)

	var req upstreamProviderReq
	if err := json.Unmarshal([]byte(createPayload), &req); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	if req.ProviderType != "claude-api-key" {
		t.Fatalf("provider_type = %q, want claude-api-key", req.ProviderType)
	}
	if len(req.APIKeyEntries) != 2 {
		t.Fatalf("decoded %d api_key_entries, want 2", len(req.APIKeyEntries))
	}
	if req.APIKeyEntries[0].Weight == nil || *req.APIKeyEntries[0].Weight != 7 {
		t.Fatalf("first entry weight = %v, want pointer to 7", req.APIKeyEntries[0].Weight)
	}
	if req.APIKeyEntries[1].Weight == nil || *req.APIKeyEntries[1].Weight != 9 {
		t.Fatalf("second entry weight = %v, want pointer to 9", req.APIKeyEntries[1].Weight)
	}

	// The in-memory store row the PG CRUD would write must mirror the
	// request: ids, names, proxies, weights, and per-entry api keys.
	row := toUpstreamProvider(&req)
	if row.ProviderType != "claude-api-key" {
		t.Fatalf("store row provider_type = %q, want claude-api-key", row.ProviderType)
	}
	if !row.RebuildMidSystemMessage || !row.ExperimentalCCHSigning {
		t.Fatalf("store row missing row-level toggles: %+v", row)
	}
	if row.Prefix != "teamA/" || row.ProxyURL != "http://provider-proxy" {
		t.Fatalf("store row scalar fields = %+v, want prefix/proxy preserved", row)
	}
	if len(row.APIKeyEntries) != 2 {
		t.Fatalf("store row entries = %d, want 2", len(row.APIKeyEntries))
	}
	for _, e := range row.APIKeyEntries {
		if e.APIKey == "" {
			t.Fatalf("store row entry stripped api key: %+v", e)
		}
	}
	if row.APIKeyEntries[0].ID != 7 || row.APIKeyEntries[0].Name != "alpha" ||
		row.APIKeyEntries[0].ProxyURL != "http://entry-a-proxy" ||
		row.APIKeyEntries[0].Weight == nil || *row.APIKeyEntries[0].Weight != 7 {
		t.Fatalf("first entry mapping = %+v, want id/name/proxy/weight preserved", row.APIKeyEntries[0])
	}
	if row.APIKeyEntries[1].ID != 9 || row.APIKeyEntries[1].Name != "beta" ||
		row.APIKeyEntries[1].ProxyURL != "" ||
		row.APIKeyEntries[1].Weight == nil || *row.APIKeyEntries[1].Weight != 9 {
		t.Fatalf("second entry mapping = %+v, want id/name/proxy/weight preserved", row.APIKeyEntries[1])
	}

	// The dashboard reads back through toUpstreamProviderResponse, which
	// embeds the store row verbatim plus a computed provider_key. Both
	// entries must round-trip with id/name/proxy/weight preserved and the
	// api keys must remain visible (the response is internal, the dashboard
	// editor masks them on the client side). The response must also stamp
	// the provider-level provider_key "claude:42" so the route picker can
	// resolve the row before any auth has registered.
	response := toUpstreamProviderResponse(store.UpstreamProvider{
		ID:                      42,
		ProviderType:            "claude-api-key",
		Prefix:                  row.Prefix,
		BaseURL:                 row.BaseURL,
		ProxyURL:                row.ProxyURL,
		RebuildMidSystemMessage: row.RebuildMidSystemMessage,
		ExperimentalCCHSigning:  row.ExperimentalCCHSigning,
		APIKeyEntries:           row.APIKeyEntries,
	})
	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("encode response: %v", err)
	}
	encoded := string(raw)
	if !strings.Contains(encoded, `"provider_key":"claude:42"`) {
		t.Fatalf("response missing compound provider_key: %s", encoded)
	}
	if !strings.Contains(encoded, `"weight":7`) || !strings.Contains(encoded, `"weight":9`) {
		t.Fatalf("response missing weight fields: %s", encoded)
	}
	// Two entries with both ids must be visible (the dashboard needs the id
	// to round-trip identity on subsequent PUTs).
	if !strings.Contains(encoded, `"id":7`) || !strings.Contains(encoded, `"id":9`) {
		t.Fatalf("response missing entry ids: %s", encoded)
	}
	// Credentials stay visible in the response (internal endpoint). Their
	// values are fakes; the assertion below protects against accidental
	// removal only.
	if !strings.Contains(encoded, secretAlpha) || !strings.Contains(encoded, secretBeta) {
		t.Fatalf("response stripped entry api keys: %s", encoded)
	}

	// A subsequent PUT (omit-then-resend round trip) must preserve retained
	// ids, allocate a new id for a brand-new entry, and drop the omitted
	// entry entirely. The legacy single-key path (no api_key_entries at all)
	// must still project exactly one config.ClaudeKey from the parent's
	// api_key + proxy.
	putPayload := `{
		"provider_type": "claude-api-key",
		"prefix": "teamA/",
		"base_url": "https://claude.example",
		"proxy_url": "http://provider-proxy",
		"rebuild_mid_system_message": true,
		"experimental_cch_signing": true,
		"api_key_entries": [
			{"id": 9, "name": "beta",  "api_key": "FAKE-SECRET-claude-beta",  "proxy_url": "",                "weight": 9},
			{"name": "gamma",            "api_key": "FAKE-SECRET-claude-gamma", "proxy_url": "http://entry-c-proxy", "weight": 4}
		]
	}`
	var putReq upstreamProviderReq
	if err := json.Unmarshal([]byte(putPayload), &putReq); err != nil {
		t.Fatalf("decode PUT request: %v", err)
	}
	putRow := toUpstreamProvider(&putReq)
	if len(putRow.APIKeyEntries) != 2 {
		t.Fatalf("PUT store row entries = %d, want 2 (id=9 retained + new gamma)", len(putRow.APIKeyEntries))
	}
	if putRow.APIKeyEntries[0].ID != 9 || putRow.APIKeyEntries[0].Name != "beta" {
		t.Fatalf("PUT did not preserve retained id=9: %+v", putRow.APIKeyEntries[0])
	}
	if putRow.APIKeyEntries[1].ID != 0 {
		t.Fatalf("PUT should leave the new entry id at 0 (the DB allocates it), got %d", putRow.APIKeyEntries[1].ID)
	}
	if putRow.APIKeyEntries[1].Name != "gamma" || putRow.APIKeyEntries[1].ProxyURL != "http://entry-c-proxy" {
		t.Fatalf("PUT new entry fields not copied: %+v", putRow.APIKeyEntries[1])
	}
	if putRow.APIKeyEntries[1].Weight == nil || *putRow.APIKeyEntries[1].Weight != 4 {
		t.Fatalf("PUT new entry weight = %v, want pointer to 4", putRow.APIKeyEntries[1].Weight)
	}

	// Legacy path: a Claude provider with api_key_entries empty must still
	// map cleanly into a store row whose APIKeyEntries slice is empty so
	// the renderer can fall back to the parent's api_key + proxy.
	legacyPayload := `{"provider_type":"claude-api-key","api_key":"FAKE-SECRET-legacy","proxy_url":"http://provider-proxy"}`
	var legacyReq upstreamProviderReq
	if err := json.Unmarshal([]byte(legacyPayload), &legacyReq); err != nil {
		t.Fatalf("decode legacy request: %v", err)
	}
	legacyRow := toUpstreamProvider(&legacyReq)
	if legacyRow.APIKey != "FAKE-SECRET-legacy" {
		t.Fatalf("legacy store row api key = %q, want parent api_key", legacyRow.APIKey)
	}
	if len(legacyRow.APIKeyEntries) != 0 {
		t.Fatalf("legacy store row fabricated entries: %+v", legacyRow.APIKeyEntries)
	}
}

// TestUpstreamProviderProxyPoolBindingDTORoundTrip mirrors
// TestUpstreamProviderEntryRequestRoundTrip for the proxy_pool_id binding
// fields on the row and its entries.
func TestUpstreamProviderProxyPoolBindingDTORoundTrip(t *testing.T) {
	const payload = `{"provider_type":"openai-compatibility","proxy_pool_id":7,"api_key_entries":[{"id":1,"api_key":"k","proxy_pool_id":9}]}`
	var req upstreamProviderReq
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		t.Fatalf("decode: %v", err)
	}
	got := toUpstreamProvider(&req)
	if got.ProxyPoolID == nil || *got.ProxyPoolID != 7 {
		t.Fatalf("row binding = %v", got.ProxyPoolID)
	}
	if got.APIKeyEntries[0].ProxyPoolID == nil || *got.APIKeyEntries[0].ProxyPoolID != 9 {
		t.Fatalf("entry binding = %v", got.APIKeyEntries[0].ProxyPoolID)
	}

	// Absent field decodes nil (not 0).
	absent := `{"provider_type":"claude-api-key"}`
	var req2 upstreamProviderReq
	if err := json.Unmarshal([]byte(absent), &req2); err != nil {
		t.Fatalf("decode absent: %v", err)
	}
	if req2.ProxyPoolID != nil {
		t.Fatalf("absent binding must be nil, got %v", *req2.ProxyPoolID)
	}

	// Response round-trip: the embedded store row exposes proxy_pool_id.
	encoded, err := json.Marshal(toUpstreamProviderResponse(got))
	if err != nil {
		t.Fatalf("encode response: %v", err)
	}
	if !strings.Contains(string(encoded), `"proxy_pool_id":7`) {
		t.Fatalf("response must carry proxy_pool_id: %s", encoded)
	}
}

// TestToUpstreamProviderCarriesAutoDisableFields pins the DTO boundary for the
// auto-disable feature (plan decision #7: re-enable = dashboard PUT carrying
// auto_disabled:false through the existing CRUD). The PUT body must carry the
// provider-level codes/cooldown and the per-entry concurrency + runtime auto
// flags into the store row so a save round-trips them, and a re-enable PUT
// (auto_disabled:false, empty reason) must map to clear-able store values.
func TestToUpstreamProviderCarriesAutoDisableFields(t *testing.T) {
	const payload = `{
		"provider_type": "openai-compatibility",
		"name": "auto-dto",
		"auto_disable_error_codes": ["401", "account_suspended"],
		"auto_disable_cooldown_seconds": 3600,
		"api_key_entries": [
			{"id": 5, "api_key": "k1", "name": "alpha", "max_concurrent": 3, "max_wait_ms": 250,
			 "auto_disabled": false, "auto_disabled_reason": ""},
			{"id": 6, "api_key": "k2", "name": "beta", "max_concurrent": 0, "max_wait_ms": 0,
			 "auto_disabled": true, "auto_disabled_at": "2026-09-26T12:00:00Z", "auto_disabled_reason": "matched 401 upstream"}
		]
	}`
	var req upstreamProviderReq
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		t.Fatalf("decode request: %v", err)
	}

	// Provider-level auto-disable config decodes and maps into the store row.
	if req.AutoDisableErrorCodes == nil || len(*req.AutoDisableErrorCodes) != 2 ||
		(*req.AutoDisableErrorCodes)[0] != "401" || (*req.AutoDisableErrorCodes)[1] != "account_suspended" {
		t.Fatalf("decoded AutoDisableErrorCodes = %#v, want [401 account_suspended]", req.AutoDisableErrorCodes)
	}
	if req.AutoDisableCooldownSeconds == nil || *req.AutoDisableCooldownSeconds != 3600 {
		t.Fatalf("decoded AutoDisableCooldownSeconds = %#v, want pointer to 3600", req.AutoDisableCooldownSeconds)
	}
	row := toUpstreamProvider(&req)
	if len(row.AutoDisableErrorCodes) != len(*req.AutoDisableErrorCodes) || row.AutoDisableErrorCodes[0] != "401" {
		t.Fatalf("store row AutoDisableErrorCodes = %#v, want [401 account_suspended]", row.AutoDisableErrorCodes)
	}
	if row.AutoDisableCooldownSeconds == nil || *row.AutoDisableCooldownSeconds != 3600 {
		t.Fatalf("store row AutoDisableCooldownSeconds = %#v, want pointer to 3600", row.AutoDisableCooldownSeconds)
	}

	// Entry-level runtime + concurrency fields map into store.UpstreamProviderAPIKey.
	if len(row.APIKeyEntries) != 2 {
		t.Fatalf("store row entries = %d, want 2", len(row.APIKeyEntries))
	}
	alpha := row.APIKeyEntries[0]
	if alpha.MaxConcurrent == nil || *alpha.MaxConcurrent != 3 {
		t.Fatalf("alpha MaxConcurrent = %#v, want pointer to 3", alpha.MaxConcurrent)
	}
	if alpha.MaxWaitMs == nil || *alpha.MaxWaitMs != 250 {
		t.Fatalf("alpha MaxWaitMs = %#v, want pointer to 250", alpha.MaxWaitMs)
	}
	// A re-enable PUT: auto_disabled false must NOT be wiped to true, and the
	// empty reason must map nil so the store COALESCE can clear it.
	if alpha.AutoDisabled {
		t.Fatalf("alpha AutoDisabled = true, want false (re-enable PUT)")
	}
	if alpha.AutoDisabledAt != nil {
		t.Fatalf("alpha AutoDisabledAt = %#v, want nil (re-enable clears the timestamp)", alpha.AutoDisabledAt)
	}
	if alpha.AutoDisabledReason != "" {
		t.Fatalf("alpha AutoDisabledReason = %q, want empty", alpha.AutoDisabledReason)
	}

	beta := row.APIKeyEntries[1]
	// max_concurrent:0 / max_wait_ms:0 must remain explicit 0 pointers (unlimited,
	// distinct from nil/inherit), not collapse to nil.
	if beta.MaxConcurrent == nil || *beta.MaxConcurrent != 0 {
		t.Fatalf("beta MaxConcurrent = %#v, want pointer to 0", beta.MaxConcurrent)
	}
	if beta.MaxWaitMs == nil || *beta.MaxWaitMs != 0 {
		t.Fatalf("beta MaxWaitMs = %#v, want pointer to 0", beta.MaxWaitMs)
	}
	// A positive runtime flag write must land.
	if !beta.AutoDisabled {
		t.Fatalf("beta AutoDisabled = false, want true (positive runtime write)")
	}
	wantAt := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	if beta.AutoDisabledAt == nil || !beta.AutoDisabledAt.Equal(wantAt) {
		t.Fatalf("beta AutoDisabledAt = %#v, want %s", beta.AutoDisabledAt, wantAt)
	}
	if beta.AutoDisabledReason != "matched 401 upstream" {
		t.Fatalf("beta AutoDisabledReason = %q, want matched 401 upstream", beta.AutoDisabledReason)
	}
	// The decoded entry must also pass through the response projection so the
	// dashboard can read the persisted flags back (GET embeds the store row).
	raw, err := json.Marshal(toUpstreamProviderResponse(store.UpstreamProvider{
		ID:            1,
		ProviderType:  "openai-compatibility",
		APIKeyEntries: row.APIKeyEntries,
	}))
	if err != nil {
		t.Fatalf("encode response: %v", err)
	}
	encoded := string(raw)
	if !strings.Contains(encoded, `"auto_disabled":true`) {
		t.Fatalf("response missing auto_disabled:true: %s", encoded)
	}
	if !strings.Contains(encoded, `"max_concurrent":3`) {
		t.Fatalf("response missing max_concurrent:3: %s", encoded)
	}
}

// TestToUpstreamProviderOmitsAutoDisableFieldsWhenAbsent verifies the absent
// provider-level auto-disable fields decode to nil (PRESERVE path) so a legacy
// PUT that never mentions the feature cannot wipe configured codes/cooldown —
// including a legacy PUT that DOES carry api_key_entries (the reviewer-flagged
// regression: entry-bearing providers are exactly the ones with configured
// codes). A no-entry claude-only body is too shallow; the entries path must
// also preserve nil.
func TestToUpstreamProviderOmitsAutoDisableFieldsWhenAbsent(t *testing.T) {
	// Legacy PUT with entries but NO auto provider keys: codes/cooldown must be
	// nil on the store row (PRESERVE), not zero/empty (which would wipe).
	req := upstreamProviderReq{
		ProviderType:  "openai-compatibility",
		APIKeyEntries: []upstreamProviderEntryReq{{ID: 1, APIKey: "k", MaxConcurrent: intPtr(2)}},
	}
	row := toUpstreamProvider(&req)
	if row.AutoDisableCooldownSeconds != nil {
		t.Fatalf("absent AutoDisableCooldownSeconds = %#v, want nil (preserve)", row.AutoDisableCooldownSeconds)
	}
	if row.AutoDisableErrorCodes != nil {
		t.Fatalf("absent AutoDisableErrorCodes = %#v, want nil (preserve)", row.AutoDisableErrorCodes)
	}
	if len(row.APIKeyEntries) != 1 {
		t.Fatalf("absent-field entries = %d, want 1", len(row.APIKeyEntries))
	}
	if row.APIKeyEntries[0].MaxConcurrent == nil || *row.APIKeyEntries[0].MaxConcurrent != 2 {
		t.Fatalf("entry concurrency not mapped alongside absent provider codes: %#v", row.APIKeyEntries[0].MaxConcurrent)
	}
	// An entry present without the NEW entry-level fields must decode nil
	// concurrency and zero auto flags (never stale values).
	req2 := upstreamProviderReq{ProviderType: "openai-compatibility", APIKeyEntries: []upstreamProviderEntryReq{{APIKey: "k"}}}
	row2 := toUpstreamProvider(&req2)
	e := row2.APIKeyEntries[0]
	if e.MaxConcurrent != nil || e.MaxWaitMs != nil {
		t.Fatalf("entry with absent fields MaxConcurrent/MaxWaitMs = %#v/%#v, want nil/nil", e.MaxConcurrent, e.MaxWaitMs)
	}
	if e.AutoDisabled || e.AutoDisabledReason != "" || e.AutoDisabledAt != nil {
		t.Fatalf("entry with absent fields auto flags = %#v, want zero", e)
	}
}

// TestToUpstreamProviderExplicitEmptyCodesIsClearPath pins the pointer DTO
// semantics that make "cleared" distinguishable from "absent": a PUT carrying
// auto_disable_error_codes: [] (the dashboard after the operator removed every
// code) must decode to a NON-NIL empty slice and map to a non-nil store slice
// (=> binds '{}' => store COALESCE clears), NOT nil (=> binds NULL => store
// preserves). Same for an explicit 0 cooldown.
func TestToUpstreamProviderExplicitEmptyCodesIsClearPath(t *testing.T) {
	clearPayload := `{"provider_type":"openai-compatibility","api_key_entries":[{"id":1,"api_key":"k"}],
		"auto_disable_error_codes": [],"auto_disable_cooldown_seconds": 0}`
	var req upstreamProviderReq
	if err := json.Unmarshal([]byte(clearPayload), &req); err != nil {
		t.Fatalf("decode clear-path request: %v", err)
	}
	if req.AutoDisableErrorCodes == nil {
		t.Fatal("auto_disable_error_codes: [] decoded to nil; want non-nil empty slice (clear path)")
	}
	if len(*req.AutoDisableErrorCodes) != 0 {
		t.Fatalf("decoded empty codes = %#v, want length 0", *req.AutoDisableErrorCodes)
	}
	if req.AutoDisableCooldownSeconds == nil || *req.AutoDisableCooldownSeconds != 0 {
		t.Fatalf("decoded cooldown = %#v, want pointer to 0", req.AutoDisableCooldownSeconds)
	}
	row := toUpstreamProvider(&req)
	if row.AutoDisableErrorCodes == nil {
		t.Fatal("store row AutoDisableErrorCodes nil from explicit []; want non-nil empty (clears via '{}')")
	}
	if len(row.AutoDisableErrorCodes) != 0 {
		t.Fatalf("store row empty codes = %#v, want length 0", row.AutoDisableErrorCodes)
	}
	if row.AutoDisableCooldownSeconds == nil || *row.AutoDisableCooldownSeconds != 0 {
		t.Fatalf("store row cooldown = %#v, want pointer to 0", row.AutoDisableCooldownSeconds)
	}
}
