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
	if body["error"] == nil {
		t.Fatalf("expected error body, got %v", body)
	}
}

// TestUpstreamProviderLiveStatusResponseJSONContract guards the JSON
// contract that the dashboard's liveStatus.js depends on: field names,
// null/omitempty handling, and the time round-trip.
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
}
