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
