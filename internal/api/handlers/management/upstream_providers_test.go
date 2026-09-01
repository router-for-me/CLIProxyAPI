package management

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
