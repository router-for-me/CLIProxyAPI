package management

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/events"
	log "github.com/sirupsen/logrus"
)

// EventsResponse is the JSON shape returned by GET /v0/management/events.
// Events is always non-nil so clients can iterate without nil-checks;
// empty result sets surface as []. Clients compute the length from the
// slice — no separate Count field is needed.
type EventsResponse struct {
	Events []events.Event `json:"events"`
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
		// Unreachable in production — cmd/server/main.go calls
		// mgmt.SetEventsRing before the server binds, so the nil-ring
		// path only fires if a future wiring change forgets the ring.
		// 500 (not 503) signals "this is a deployment bug, not a
		// missing-feature condition" so the dashboard can surface a
		// remediation hint instead of a misleading "PG not enabled" .
		c.JSON(http.StatusInternalServerError, gin.H{"error": "events ring not wired"})
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
			// Newer events first; once we cross an event older than since,
			// every remaining entry is also older so we can stop scanning.
			// This collapses the per-request work from O(ring-capacity) to
			// O(matching-events) when a since= filter is supplied.
			break
		}
		out = append(out, e)
		if len(out) >= limit {
			break
		}
	}
	c.JSON(http.StatusOK, EventsResponse{Events: out})
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
		// See the comment on GetEvents's nil-ring branch — same rationale
		// applies here. Logged separately so an operator inspecting
		// /events/stats sees the warning.
		log.Warn("events stats handler called but events ring is not wired; check cmd/server/main.go initialization")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "events ring not wired"})
		return
	}
	c.JSON(http.StatusOK, EventsStatsResponse{
		Capacity: ring.Capacity(),
		Dropped:  ring.Dropped(),
	})
}

// eventsStreamKeepAlive is the SSE comment-ping interval. SSE clients
// (EventSource browsers, the dashboard live tab) treat the connection
// as dead when no frames arrive within ~30-60s, so we emit a ":keep-alive"
// comment every 15s to keep proxies and the browser happy without
// burning bandwidth.
const eventsStreamKeepAlive = 15 * time.Second

// StreamEvents handles GET /v0/management/events/stream — the Server-Sent
// Events live feed. Each subscribed client gets its own per-subscriber
// channel (see events.Ring.Subscribe); the handler pumps frames until
// the client disconnects (c.Request.Context() Done) or the subscriber
// is closed. Returns 503 without PG (matches the rest of the
// management-route gate contract, even though no PG queries are
// involved), so the dashboard can detect the absence cleanly.
//
// SSE framing: one event per frame as `event: <type>\ndata: <json>\n\n`
// plus a 15s `:keep-alive\n\n` comment to defeat proxy/browser idle
// timeouts. The X-Accel-Buffering=no header disables nginx buffering so
// frames are flushed immediately; Cache-Control: no-cache prevents
// intermediate caches from holding frames. Connection: keep-alive
// keeps the underlying TCP socket warm.
//
// Client disconnect drops the goroutine within 5s of context
// cancellation — the request context cancels the moment the underlying
// TCP read on the request body fails, and the select on ctx.Done()
// returns immediately. No goroutine leaks on dashboard tab close.
func (h *Handler) StreamEvents(c *gin.Context) {
	if !h.pgEnabledForEvents() {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "PG storage not enabled"})
		return
	}
	ring := h.eventsRing()
	if ring == nil {
		log.Warn("events stream handler called but events ring is not wired; check cmd/server/main.go initialization")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "events ring not wired"})
		return
	}

	hdr := c.Writer.Header()
	hdr.Set("Content-Type", "text/event-stream")
	hdr.Set("Cache-Control", "no-cache")
	hdr.Set("Connection", "keep-alive")
	hdr.Set("X-Accel-Buffering", "no")
	c.Writer.WriteHeader(http.StatusOK)
	c.Writer.Flush()

	sub := ring.Subscribe()
	defer ring.Unsubscribe(sub)

	ctx := c.Request.Context()
	pingTicker := time.NewTicker(eventsStreamKeepAlive)
	defer pingTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case e, ok := <-sub.C:
			if !ok {
				return
			}
			payload, err := json.Marshal(e)
			if err != nil {
				// Should never happen — the only error json.Marshal can
				// return on Event is for unsupported types, and Event
				// is all primitives + RawMessage. Log and skip rather
				// than close the stream on a single bad event.
				log.WithError(err).WithField("event_type", e.Type).Warn("events stream: marshal event failed; skipping frame")
				continue
			}
			if _, err := fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", e.Type, payload); err != nil {
				// Write failure usually means the client disconnected;
				// let ctx.Done() close the loop on the next iteration.
				return
			}
			c.Writer.Flush()
		case <-pingTicker.C:
			if _, err := fmt.Fprint(c.Writer, ": keep-alive\n\n"); err != nil {
				return
			}
			c.Writer.Flush()
		}
	}
}

// parseEventsSince parses an RFC3339 timestamp from the since= query
// parameter. Returns the zero time.Time on parse failure or empty input,
// which the handler treats as "no since filter" — the same shape as
// omitting the parameter. We deliberately do NOT 400 on a malformed since:
// the rest of the filters still apply, so a typo doesn't black out the
// whole page for the operator. The parse failure is logged at WARN so
// operators can spot a recurring client bug or copy/paste mistake
// without the operator's dashboard silently dropping events.
func parseEventsSince(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		log.Warnf("events endpoint: invalid since=%q (RFC3339 required): %v", s, err)
		return time.Time{}
	}
	return t
}

// parseEventsLimit parses the limit= query parameter. Empty / non-numeric
// / non-positive values fall back to defaultLimit; values above maxLimit
// clamp down to maxLimit. The 500-page cap matches the round-2 design.
//
// We treat zero/negative as missing because limit=0 is rarely an
// intentional request and we'd rather give a default page than an empty
// response — callers who genuinely want to test the empty-result path
// can pass an impossibly-narrow since= or use a filter that matches no
// rows.
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
