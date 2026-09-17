package management

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/events"
)

// EventsResponse is the JSON shape returned by GET /v0/management/events.
// Events is always non-nil so clients can iterate without nil-checks;
// empty result sets surface as []. Count mirrors len(Events) so the
// dashboard does not have to reach into the slice to size its render.
type EventsResponse struct {
	Events []events.Event `json:"events"`
	Count  int            `json:"count"`
}

// EventsStatsResponse is the JSON shape returned by
// GET /v0/management/events/stats. Capacity is the ring's post-clamp
// configured size (matches internal/events.NewRing's [100, 50000]
// contract); Dropped is the cumulative count of events lost to writer
// contention, exposed so operators can detect backpressure.
type EventsStatsResponse struct {
	Capacity int   `json:"capacity"`
	Dropped  int64 `json:"dropped"`
}

// eventsDefaultLimit is the page size returned when the caller omits
// ?limit=. Per the round-2 design this is 100; callers can ask for less
// or more up to eventsMaxLimit.
const eventsDefaultLimit = 100

// eventsMaxLimit caps ?limit=. Per the round-2 design this is 500;
// larger requests clamp down so a misbehaving client cannot drain the
// whole ring in one shot.
const eventsMaxLimit = 500

// GetEvents handles GET /v0/management/events. Filters:
//   - type=<event-type>: matches the Event.Type field
//   - auth=<auth-id>:   matches the Event.AuthID field
//   - since=<rfc3339>:  includes only events with Ts >= since
//
// Results come back newest-first (the ring's Snapshot() order) and are
// capped at eventsMaxLimit. Returns 503 without PG, matching the
// round-2 contract for the management-route surface (the gate stays
// consistent even though no PG queries are involved).
func (h *Handler) GetEvents(c *gin.Context) {
	if !h.pgEnabledForEvents() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "PG storage not enabled"})
		return
	}
	ring := h.eventsRing()
	if ring == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "PG storage not enabled"})
		return
	}

	typ := c.Query("type")
	auth := c.Query("auth")
	since := parseEventsSince(c.Query("since"))
	limit := parseEventsLimit(c.Query("limit"), eventsDefaultLimit, eventsMaxLimit)

	snap := ring.Snapshot()
	out := make([]events.Event, 0, limit)
	for _, e := range snap {
		if typ != "" && e.Type != typ {
			continue
		}
		if auth != "" && e.AuthID != auth {
			continue
		}
		if !since.IsZero() && e.Ts.Before(since) {
			continue
		}
		out = append(out, e)
		if len(out) >= limit {
			break
		}
	}
	c.JSON(http.StatusOK, EventsResponse{Events: out, Count: len(out)})
}

// GetEventsStats handles GET /v0/management/events/stats. Exposes the
// ring's post-clamp capacity + the cumulative dropped counter so the
// dashboard's Live tab can render "backpressure detected" / "ring at
// N%" status without having to fetch the whole events page. Same 503
// gate as GetEvents.
func (h *Handler) GetEventsStats(c *gin.Context) {
	if !h.pgEnabledForEvents() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "PG storage not enabled"})
		return
	}
	ring := h.eventsRing()
	if ring == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "PG storage not enabled"})
		return
	}
	c.JSON(http.StatusOK, EventsStatsResponse{
		Capacity: ring.Capacity(),
		Dropped:  ring.Dropped(),
	})
}

// parseEventsSince parses an RFC3339 timestamp from the since= query
// parameter. Returns the zero time.Time on parse failure or empty input,
// which the handler treats as "no since filter" — the same shape as
// omitting the parameter. We deliberately do NOT 400 on a malformed since:
// the rest of the filters still apply, so a typo doesn't black out the
// whole page for the operator.
func parseEventsSince(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// parseEventsLimit parses the limit= query parameter. Empty / non-numeric
// / non-positive values fall back to defaultLimit; values above maxLimit
// clamp down to maxLimit. The 500-page cap matches the round-2 design.
func parseEventsLimit(s string, defaultLimit, maxLimit int) int {
	if s == "" {
		return defaultLimit
	}
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 {
		return defaultLimit
	}
	if n > maxLimit {
		return maxLimit
	}
	return n
}

// SetEventsRing attaches the in-memory events ring recorder so the
// /v0/management/events endpoints can serve Snapshot() / Capacity() /
// Dropped(). A non-nil ring also flips the events gate on (eventsEnabled)
// so the routes stop returning 503. nil disables both — the gate stays
// off and the handler returns 503 via the pgEnabledForEvents guard.
// Production wires this from cmd/server after events.SetGlobal so a
// single ring backs both the global recorder and the management
// handlers. Tests call it directly via this public seam (no PG wiring
// needed).
func (h *Handler) SetEventsRing(ring *events.Ring) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.eventsRingRecorder = ring
	h.eventsEnabled = ring != nil
	h.mu.Unlock()
}

// eventsRing returns the ring attached via SetEventsRing, or nil if no
// ring has been wired. Thread-safe under h.mu.
func (h *Handler) eventsRing() *events.Ring {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.eventsRingRecorder
}

// pgEnabledForEvents gates the events endpoints on the PG backend.
// The gate mirrors the rest of the PG-first management routes: even
// though no PG queries are involved, returning 503 here keeps the
// management-route contract consistent so dashboards can rely on "no
// PG → no /v0/management/* data routes". The actual flag flips when
// SetEventsRing is called (production path) or via the test-only
// setEventsEnabledForTest seam.
func (h *Handler) pgEnabledForEvents() bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.eventsEnabled
}

// setEventsEnabledForTest toggles the events gate without standing up
// the rest of the PG wiring. Production wires SetEventsRing which sets
// the flag; tests poke the field directly via this seam so the package
// stays free of PG fixtures.
func (h *Handler) setEventsEnabledForTest(enabled bool) {
	if h == nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.eventsEnabled = enabled
}
