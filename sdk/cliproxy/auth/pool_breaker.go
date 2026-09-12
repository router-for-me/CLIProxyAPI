package auth

import (
	"sync"
	"time"
)

// pool_breaker.go implements the opt-in pool-level circuit breaker (G3,
// OmniRoute reference): callers must feed only 408/5xx results; 401/402/403/429
// belong to the per-auth+model cooldown. State is in-memory by design — a
// restart starts every pool clean. Failure counting is fixed-window (resets
// 60s after the window starts), a deliberate simplification of the design
// doc's "sliding window" note.
type breakerState int

const (
	breakerClosed breakerState = iota
	breakerOpen
	breakerHalfOpen
)

const (
	poolBreakerFailureThreshold = 8
	poolBreakerWindow           = 60 * time.Second
	poolBreakerResetBase        = 30 * time.Second
	poolBreakerResetMax         = 5 * time.Minute
)

type poolBreakerEntry struct {
	failures    int
	windowStart time.Time
	state       breakerState
	openedAt    time.Time
	resetPeriod time.Duration
	probeUsed   bool
}

type poolBreaker struct {
	mu    sync.Mutex
	pools map[string]*poolBreakerEntry
}

var globalPoolBreaker = &poolBreaker{pools: make(map[string]*poolBreakerEntry)}

// entry returns the entry for key, creating a fresh CLOSED entry if absent.
func (b *poolBreaker) entry(key string) *poolBreakerEntry {
	entry, ok := b.pools[key]
	if !ok {
		entry = &poolBreakerEntry{state: breakerClosed, resetPeriod: poolBreakerResetBase}
		b.pools[key] = entry
	}
	return entry
}

// recordFailure feeds one 408/5xx result into the breaker. Fixed-window
// counting: failures inside one 60s window accumulate; hitting the threshold
// opens the breaker. A failure while HALF_OPEN re-opens with the reset period
// doubled (capped); failures while OPEN are ignored (the pool is already
// blocked).
func (b *poolBreaker) recordFailure(key string, now time.Time) {
	if key == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	entry := b.entry(key)
	if entry.state == breakerHalfOpen {
		entry.state = breakerOpen
		entry.openedAt = now
		entry.resetPeriod *= 2
		if entry.resetPeriod > poolBreakerResetMax {
			entry.resetPeriod = poolBreakerResetMax
		}
		return
	}
	if entry.state == breakerOpen {
		return
	}
	if entry.windowStart.IsZero() || now.Sub(entry.windowStart) > poolBreakerWindow {
		entry.windowStart = now
		entry.failures = 0
	}
	entry.failures++
	if entry.failures >= poolBreakerFailureThreshold {
		entry.state = breakerOpen
		entry.openedAt = now
	}
}

// recordSuccess closes an open/half-open breaker and clears the window. The
// now parameter is unused today (deletion needs no time) but kept for a
// uniform signature with the other methods.
func (b *poolBreaker) recordSuccess(key string, now time.Time) {
	if key == "" {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.pools[key]; !ok {
		return
	}
	delete(b.pools, key)
}

// blockDeadline reports whether the pool currently blocks selection and when
// it lifts. OPEN blocks until openedAt+resetPeriod; the lazy transition to
// HALF_OPEN admits exactly one probe (the first caller after expiry). While
// the probe is unresolved, other callers are blocked until a probe window
// anchored at probe-admission time (openedAt+resetPeriod) elapses; if the
// window expires without a verdict, a fresh probe is admitted.
func (b *poolBreaker) blockDeadline(key string, now time.Time) (time.Time, bool) {
	if key == "" {
		return time.Time{}, false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	entry, ok := b.pools[key]
	if !ok {
		return time.Time{}, false
	}
	switch entry.state {
	case breakerOpen:
		deadline := entry.openedAt.Add(entry.resetPeriod)
		if now.Before(deadline) {
			return deadline, true
		}
		entry.state = breakerHalfOpen
		entry.probeUsed = false
		fallthrough
	case breakerHalfOpen:
		if entry.probeUsed {
			// A probe is in flight; block until its anchored window elapses.
			probeDeadline := entry.openedAt.Add(entry.resetPeriod)
			if now.Before(probeDeadline) {
				return probeDeadline, true
			}
			// The probe window expired without a verdict; admit a fresh probe.
			entry.probeUsed = false
		}
		entry.probeUsed = true
		entry.openedAt = now      // anchor the probe window at admission time
		return time.Time{}, false // probe admitted
	default:
		return time.Time{}, false
	}
}
