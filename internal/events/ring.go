package events

import (
	"sync"
	"sync/atomic"
	"time"
)

const (
	minRingCapacity     = 100
	maxRingCapacity     = 50000
	defaultRingCapacity = 5000
)

// DefaultRingCapacity is the capacity the ring picks when callers pass
// 0 (and the value cmd/server falls back to when config.routing.events.
// ring-capacity is unset). It matches the value the round-2 design doc
// pins as the in-process default.
const DefaultRingCapacity = defaultRingCapacity

// Ring is a bounded, lock-protected event buffer. Capacity is enforced
// by count (not bytes). Records that exceed the contention threshold
// are dropped and counted, never blocking the caller for long.
type Ring struct {
	mu      sync.Mutex
	buf     []Event
	cap     int
	cursor  int
	full    bool
	dropped atomic.Int64
	now     func() time.Time
	// contentionThreshold is reserved for future use. The current
	// Record implementation only drops on TryLock failure; the field
	// is kept for callers that want to introspect or extend.
	contentionThreshold time.Duration
}

// NewRing returns a Ring with the given capacity. Capacity is clamped
// to [100, 50000]. The default contention threshold is 1ms.
func NewRing(capacity int) *Ring {
	if capacity < minRingCapacity {
		capacity = minRingCapacity
	}
	if capacity > maxRingCapacity {
		capacity = maxRingCapacity
	}
	return &Ring{
		buf:                 make([]Event, capacity),
		cap:                 capacity,
		now:                 time.Now,
		contentionThreshold: time.Millisecond,
	}
}

// Capacity returns the ring's actual capacity (post-clamp).
func (r *Ring) Capacity() int { return r.cap }

// Record adds an event to the ring. If e.Ts is the zero time.Time,
// the ring stamps it with its clock; otherwise the caller's Ts is
// preserved. Zero-value Ring (cap==0) is a no-op.
func (r *Ring) Record(e Event) {
	// Short-circuit on zero-value Ring (returned by events.Global()
	// before SetGlobal has been called). Without this, the zero-value
	// ring has cap=0 and panics on the modulo below.
	if r.cap == 0 {
		return
	}
	if e.Ts.IsZero() {
		e.Ts = r.now()
	}
	if !r.mu.TryLock() {
		r.dropped.Add(1)
		return
	}
	defer r.mu.Unlock()
	r.buf[r.cursor] = e
	r.cursor = (r.cursor + 1) % r.cap
	if r.cursor == 0 {
		r.full = true
	}
}

// Snapshot returns the events in newest-first order. The returned slice
// is a fresh copy; callers may mutate it freely.
func (r *Ring) Snapshot() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []Event
	if r.full {
		out = make([]Event, r.cap)
		// Oldest first: cursor..end, then 0..cursor
		copy(out, r.buf[r.cursor:])
		copy(out[r.cap-r.cursor:], r.buf[:r.cursor])
	} else {
		out = make([]Event, r.cursor)
		copy(out, r.buf[:r.cursor])
	}
	// Reverse for newest-first
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// Dropped returns the cumulative count of events dropped due to contention.
func (r *Ring) Dropped() int64 { return r.dropped.Load() }
