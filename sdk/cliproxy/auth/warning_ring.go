package auth

import (
	"strings"
	"time"
)

// warningRingCapacity bounds how many distinct recent warnings are retained per
// auth. Older distinct warnings are evicted in newest-wins order once the ring
// is full. Consecutive identical warnings are coalesced (see warningRing.record)
// so a credential failing the same way repeatedly occupies a single slot.
const warningRingCapacity = 50

// WarningEvent is an operator-facing record of a warning that affected an auth:
// a failed request, a quota/rate-limit cooldown, a provider usage-window park,
// etc. It is exposed via the management API so operators can answer "what
// happened, when, and how many times" without grepping ephemeral logs.
type WarningEvent struct {
	// Kind classifies the warning (e.g. "rate_limit", "unauthorized",
	// "usage_limit", "server_error").
	Kind string `json:"kind"`
	// Code is the provider/machine-readable error code, when available.
	Code string `json:"code,omitempty"`
	// Message is the human readable description.
	Message string `json:"message"`
	// HTTPStatus records the HTTP-like status code, when available.
	HTTPStatus int `json:"http_status,omitempty"`
	// Model identifies the model involved, when the warning is model-scoped.
	Model string `json:"model,omitempty"`
	// Count is how many consecutive times this same warning was observed.
	Count int64 `json:"count"`
	// FirstAt is when this warning was first observed (UTC).
	FirstAt time.Time `json:"first_at"`
	// LastAt is when this warning was most recently observed (UTC).
	LastAt time.Time `json:"last_at"`
}

// warningRing is a fixed-capacity, newest-wins ring of WarningEvent plus a
// monotonic total counter.
//
// Like recentRequestRing and rateWindow, warningRing is intentionally NOT
// internally synchronized: every access must happen while holding the owning
// Manager's mutex. This keeps Auth.Clone() (a by-value copy) free of lock-copy
// hazards.
type warningRing struct {
	events [warningRingCapacity]WarningEvent
	// next is the index of the next write slot.
	next int
	// count is the number of valid entries currently stored (<= capacity).
	count int
	// total is the monotonic lifetime count of warnings recorded; it never
	// decreases even when older distinct warnings are evicted.
	total int64
}

// warningEventsEqual reports whether two warnings are the "same" for coalescing
// purposes (identical classification, status, message and model).
func warningEventsEqual(a *WarningEvent, kind, code, message string, httpStatus int, model string) bool {
	return a.Kind == kind &&
		a.Code == code &&
		a.Message == message &&
		a.HTTPStatus == httpStatus &&
		a.Model == model
}

// record appends a warning, coalescing it with the most recent entry when it is
// identical (bumping that entry's Count and LastAt instead of consuming a new
// slot). total is always incremented.
func (w *warningRing) record(at time.Time, kind, code, message string, httpStatus int, model string) {
	if at.IsZero() {
		at = time.Now()
	}
	at = at.UTC()
	kind = strings.TrimSpace(kind)
	if kind == "" {
		kind = "error"
	}
	message = strings.TrimSpace(message)

	w.total++

	if w.count > 0 {
		lastIdx := (w.next - 1 + warningRingCapacity) % warningRingCapacity
		last := &w.events[lastIdx]
		if warningEventsEqual(last, kind, code, message, httpStatus, model) {
			last.Count++
			last.LastAt = at
			return
		}
	}

	w.events[w.next] = WarningEvent{
		Kind:       kind,
		Code:       code,
		Message:    message,
		HTTPStatus: httpStatus,
		Model:      model,
		Count:      1,
		FirstAt:    at,
		LastAt:     at,
	}
	w.next = (w.next + 1) % warningRingCapacity
	if w.count < warningRingCapacity {
		w.count++
	}
}

// snapshot returns the retained warnings newest-first, along with the monotonic
// lifetime total.
func (w *warningRing) snapshot() ([]WarningEvent, int64) {
	out := make([]WarningEvent, 0, w.count)
	for i := 0; i < w.count; i++ {
		idx := (w.next - 1 - i + warningRingCapacity*2) % warningRingCapacity
		out = append(out, w.events[idx])
	}
	return out, w.total
}

// recordWarning records an operator-facing warning for this auth. Callers must
// hold the owning Manager's mutex (same discipline as recordRecentRequest).
func (a *Auth) recordWarning(at time.Time, kind, code, message string, httpStatus int, model string) {
	if a == nil {
		return
	}
	a.warnings.record(at, kind, code, message, httpStatus, model)
}

// WarningsSnapshot returns the retained recent warnings (newest first) and the
// monotonic lifetime total observed for this auth. It is read from a cloned Auth
// in the management API, so it is race-free without additional locking.
func (a *Auth) WarningsSnapshot() ([]WarningEvent, int64) {
	if a == nil {
		return nil, 0
	}
	return a.warnings.snapshot()
}

// UsageLimitUntil returns the time until which this auth is parked due to a
// provider usage window (e.g. Claude's rolling 5h/7d limit), or the zero time
// when no park is active. Read from a cloned Auth, it is race-free.
func (a *Auth) UsageLimitUntil() time.Time {
	if a == nil {
		return time.Time{}
	}
	return a.rate.usageLimitUntil
}
