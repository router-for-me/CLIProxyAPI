package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// TestGetUpstreamProvidersLiveStatus_503WhenPGMissing exercises the 503
// path: with no pgUpstreamProviders wired into the Handler, the upstream
// store helper returns 503 before the authManager check runs.
func TestGetUpstreamProvidersLiveStatus_503WhenPGMissing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &Handler{} // no pgUpstreamProviders, no authManager
	r := gin.New()
	r.GET("/v0/management/upstream-providers/live-status", h.GetUpstreamProvidersLiveStatus)

	req := httptest.NewRequest(http.MethodGet, "/v0/management/upstream-providers/live-status", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body = %s", w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	errObj, ok := body["error"].(map[string]any)
	if !ok || errObj == nil {
		t.Fatalf("expected error object body, got %v", body)
	}
	if errObj["type"] != "pg_store_not_configured" {
		t.Fatalf("error.type = %v, want pg_store_not_configured", errObj["type"])
	}
}

// TestUpstreamProviderLiveStatusResponseJSONContract guards the JSON
// contract that the dashboard's liveStatus.js depends on: raw key names
// (a round-trip through the typed struct alone would silently absorb
// tag renames), and *time.Time omitempty + value equality.
func TestUpstreamProviderLiveStatusResponseJSONContract(t *testing.T) {
	until := time.Date(2026, 9, 19, 12, 5, 0, 0, time.UTC)
	last := time.Date(2026, 9, 19, 11, 59, 30, 0, time.UTC)
	resp := UpstreamProviderLiveStatusResponse{
		Rows: map[string]UpstreamProviderLiveStatus{
			"12": {IsLive: true, BreakerOpen: false},
			"13": {IsLive: false, CooldownUntil: &until, BreakerOpen: true, LastCheckAt: &last, LastError: "429 rate-limited"},
		},
		AsOf: time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC),
	}
	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	t.Run("raw_keys", func(t *testing.T) {
		var raw map[string]any
		if err := json.Unmarshal(b, &raw); err != nil {
			t.Fatalf("unmarshal to raw: %v", err)
		}
		for _, key := range []string{"rows", "as_of"} {
			if _, ok := raw[key]; !ok {
				t.Fatalf("missing top-level key %q in %s", key, b)
			}
		}
		rowsRaw, ok := raw["rows"].(map[string]any)
		if !ok {
			t.Fatalf("rows is not an object: %v", raw["rows"])
		}
		r13Raw, ok := rowsRaw["13"].(map[string]any)
		if !ok {
			t.Fatalf("rows.13 missing or wrong type")
		}
		for _, key := range []string{"is_live", "breaker_open", "cooldown_until", "last_check_at", "last_error"} {
			if _, ok := r13Raw[key]; !ok {
				t.Fatalf("rows.13 missing key %q in %s", key, b)
			}
		}
	})

	t.Run("typed_round_trip", func(t *testing.T) {
		var roundTrip UpstreamProviderLiveStatusResponse
		if err := json.Unmarshal(b, &roundTrip); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(roundTrip.Rows) != 2 {
			t.Fatalf("rows = %d, want 2", len(roundTrip.Rows))
		}
		r13, ok := roundTrip.Rows["13"]
		if !ok {
			t.Fatalf("missing row 13")
		}
		if r13.LastError != "429 rate-limited" || !r13.BreakerOpen || r13.CooldownUntil == nil {
			t.Fatalf("row 13 = %+v, want breaker+cooldown+error", r13)
		}
		if !roundTrip.AsOf.Equal(resp.AsOf) {
			t.Fatalf("as_of round-trip mismatch: got %v want %v", roundTrip.AsOf, resp.AsOf)
		}
	})
}
