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

	// subsMu / subs back the SSE fan-out (Ring.Subscribe / Unsubscribe).
	// subsMu is RLocked during fanout (so a slow subscriber never blocks
	// Record's caller goroutine — fanout runs AFTER r.mu is released in
	// Record); Locked only when the subscriber set itself changes.
	subsMu sync.RWMutex
	subs   map[*Subscriber]struct{}
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
	r.buf[r.cursor] = e
	r.cursor = (r.cursor + 1) % r.cap
	if r.cursor == 0 {
		r.full = true
	}
	// Capture the value before releasing the ring mutex so fanout runs
	// AFTER the lock is released. Fanout iterates subscribers and may
	// block briefly on a full channel (which has its own drop-on-full
	// fast path), so doing it under r.mu would let a slow subscriber
	// stall every concurrent Record caller.
	r.mu.Unlock()
	r.fanout(e)
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

// subscriberChanCap caps the per-subscriber channel buffer used by the
// SSE fan-out. The round-2 design pins this at 32 — large enough that a
// momentarily-slow dashboard client can absorb a small burst without
// dropping, small enough that a stuck client doesn't pin unbounded
// memory. On overflow, Subscriber.deliver silently drops the event for
// that subscriber only (other subscribers are unaffected).
const subscriberChanCap = 32

// Subscriber is a per-client fan-out channel handed out by
// Ring.Subscribe. The SSE handler reads from C until it closes; closure
// happens exactly once when Ring.Unsubscribe runs. Closed atomically so
// concurrent deliver + Unsubscribe never panics on a closed channel.
type Subscriber struct {
	C      chan Event
	closed atomic.Bool
}

// newSubscriber allocates a buffered channel with subscriberChanCap
// capacity. The buffer is intentionally small — see subscriberChanCap.
func newSubscriber() *Subscriber {
	return &Subscriber{C: make(chan Event, subscriberChanCap)}
}

// deliver pushes e onto the subscriber's channel, dropping it (without
// blocking) when the buffer is full. Returns true if the event was
// enqueued, false if it was dropped (buffer full, or subscriber closed).
// The closed check is the goroutine-safety guarantee: once close() has
// run (CAS-driven), no further sends occur.
func (s *Subscriber) deliver(e Event) bool {
	if s.closed.Load() {
		return false
	}
	select {
	case s.C <- e:
		return true
	default:
		return false // buffer full; drop event for this subscriber
	}
}

// close is invoked exactly once by Ring.Unsubscribe. Subsequent calls
// from other Unsubscribe paths are no-ops thanks to CompareAndSwap.
func (s *Subscriber) close() {
	if s.closed.CompareAndSwap(false, true) {
		close(s.C)
	}
}

// Subscribe returns a new subscriber attached to this ring. The caller
// MUST defer Ring.Unsubscribe(sub) so the channel is closed and the
// goroutine that reads from C can terminate cleanly.
//
// On a zero-value Ring (cap==0; the events.Global() stub before
// SetGlobal), the returned subscriber is never delivered to because
// Record short-circuits. Callers should still Unsubscribe so the
// buffered channel is closed for GC.
func (r *Ring) Subscribe() *Subscriber {
	sub := newSubscriber()
	if r.cap == 0 {
		// Zero-value ring: return a subscriber but skip registration.
		// Record will short-circuit anyway, so no fanout will run.
		return sub
	}
	r.subsMu.Lock()
	defer r.subsMu.Unlock()
	if r.subs == nil {
		r.subs = make(map[*Subscriber]struct{})
	}
	r.subs[sub] = struct{}{}
	return sub
}

// Unsubscribe removes sub from the ring's subscriber set and closes its
// channel. Safe to call on a nil sub (no-op) and idempotent on a
// double-Unsubscribe (the second call sees the subscriber absent from
// the map and returns without re-closing).
func (r *Ring) Unsubscribe(sub *Subscriber) {
	if sub == nil {
		return
	}
	r.subsMu.Lock()
	defer r.subsMu.Unlock()
	if _, ok := r.subs[sub]; ok {
		delete(r.subs, sub)
		sub.close()
	}
}

// fanout delivers a recorded event to every subscriber. The ring mutex
// is NOT held when this runs (Record releases r.mu before calling), so
// a slow subscriber can briefly delay fanout for itself without stalling
// other Record callers. The per-subscriber drop-on-full guard inside
// Subscriber.deliver makes "slow subscriber" the only cost of a slow
// client, never a global backpressure event.
func (r *Ring) fanout(e Event) {
	r.subsMu.RLock()
	defer r.subsMu.RUnlock()
	for sub := range r.subs {
		sub.deliver(e)
	}
}
