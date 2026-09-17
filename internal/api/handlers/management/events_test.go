package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/events"
)

// eventsHandlersTest wires a Handler with the supplied ring attached via
// the SetEventsRing seam so the events endpoints have something to read.
// Tests then opt in/out of the PG gate via setEventsEnabledForTest (true
// mimics SetPGControl having wired the ring; false mimics a file-only
// deployment where the events routes return 503 — the round-2 gate that
// matches the rest of the management-route contract).
func eventsHandlersTest(t *testing.T, ring *events.Ring) *Handler {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := NewHandlerWithoutConfigFilePath(nil, nil)
	h.SetEventsRing(ring)
	h.setEventsEnabledForTest(true)
	return h
}

// runEvents dispatches a single GET to the supplied path on a minimal Gin
// router that wires only the events endpoints we care about. Mirrors the
// dispatch helper used by quota_share_test.go: small, deterministic, no
// middleware noise.
func runEvents(t *testing.T, h *Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/v0/management/events", h.GetEvents)
	r.GET("/v0/management/events/stats", h.GetEventsStats)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, nil)
	r.ServeHTTP(w, req)
	return w
}

// TestEventsEndpointFiltersByType pins the type=... query-param filter:
// only events whose Type matches are returned.
func TestEventsEndpointFiltersByType(t *testing.T) {
	ring := events.NewRing(100)
	now := time.Now()
	ring.Record(events.Event{Type: "routing.decision", Ts: now})
	ring.Record(events.Event{Type: "breaker.tripped", Ts: now})
	ring.Record(events.Event{Type: "routing.cooldown_wait", Ts: now})

	h := eventsHandlersTest(t, ring)
	w := runEvents(t, h, http.MethodGet, "/v0/management/events?type=routing.decision")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp EventsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Events) != 1 || resp.Events[0].Type != "routing.decision" {
		t.Errorf("events = %+v, want one routing.decision row", resp.Events)
	}
}

// TestEventsEndpointFiltersByAuth pins the auth=... query-param filter.
// Two events with distinct AuthID are seeded; only the matching one
// surfaces.
func TestEventsEndpointFiltersByAuth(t *testing.T) {
	ring := events.NewRing(100)
	now := time.Now()
	ring.Record(events.Event{Type: "routing.decision", Ts: now, AuthID: "auth-1"})
	ring.Record(events.Event{Type: "routing.decision", Ts: now, AuthID: "auth-2"})

	h := eventsHandlersTest(t, ring)
	w := runEvents(t, h, http.MethodGet, "/v0/management/events?auth=auth-1")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp EventsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Events) != 1 || resp.Events[0].AuthID != "auth-1" {
		t.Errorf("events = %+v, want one auth-1 row", resp.Events)
	}
}

// TestEventsEndpointFiltersBySince pins the since=<rfc3339> filter: an
// event older than the cutoff is excluded.
func TestEventsEndpointFiltersBySince(t *testing.T) {
	ring := events.NewRing(100)
	old := time.Now().Add(-1 * time.Hour)
	fresh := time.Now()
	ring.Record(events.Event{Type: "routing.decision", Ts: old})
	ring.Record(events.Event{Type: "routing.decision", Ts: fresh})

	h := eventsHandlersTest(t, ring)
	since := time.Now().Add(-30 * time.Minute).UTC().Format(time.RFC3339)
	w := runEvents(t, h, http.MethodGet, "/v0/management/events?since="+since)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp EventsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Events) != 1 {
		t.Errorf("len(events) = %d, want 1 (only the fresh event passes the since filter)", len(resp.Events))
	}
}

// TestEventsEndpointRespectsLimit pins the limit=<int> cap. The round-2
// design calls for default=100, max=500; this test uses 10 to confirm the
// cap clamps correctly without coupling to the default.
func TestEventsEndpointRespectsLimit(t *testing.T) {
	ring := events.NewRing(1000)
	now := time.Now()
	for i := 0; i < 100; i++ {
		ring.Record(events.Event{Type: "test", Ts: now})
	}

	h := eventsHandlersTest(t, ring)
	w := runEvents(t, h, http.MethodGet, "/v0/management/events?limit=10")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp EventsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Events) != 10 {
		t.Errorf("len(events) = %d, want 10 (limit=10)", len(resp.Events))
	}
}

// TestEventsEndpointReturns503WithoutPG pins the management-route contract:
// when the PG gate is off (file-only deployment), the events endpoints
// return 503 immediately so callers can detect the absence.
func TestEventsEndpointReturns503WithoutPG(t *testing.T) {
	h := eventsHandlersTest(t, events.NewRing(100))
	h.setEventsEnabledForTest(false)
	w := runEvents(t, h, http.MethodGet, "/v0/management/events")
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%s", w.Code, w.Body.String())
	}
}

// TestEventsStatsEndpoint pins the /events/stats shape: capacity matches
// the ring's Capacity(), and the dropped counter is exposed as a non-
// negative integer. Contention-induced drops are exercised in
// internal/events/ring_test.go (same package as the mutex); from here we
// only verify the handler wires the counter through to JSON.
func TestEventsStatsEndpoint(t *testing.T) {
	ring := events.NewRing(500)
	// Saturate the ring so the snapshot rotates. Dropped() stays at 0
	// for serial single-goroutine writes; the contention path is pinned
	// by ring_test.go.
	for i := 0; i < 600; i++ {
		ring.Record(events.Event{Type: "test", Ts: time.Now()})
	}

	h := eventsHandlersTest(t, ring)
	w := runEvents(t, h, http.MethodGet, "/v0/management/events/stats")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	var resp EventsStatsResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Capacity != 500 {
		t.Errorf("capacity = %d, want 500", resp.Capacity)
	}
	if resp.Dropped < 0 {
		t.Errorf("dropped = %d, want >= 0", resp.Dropped)
	}
}
