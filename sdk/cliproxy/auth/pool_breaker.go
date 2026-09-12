package auth

import (
	"sort"
	"strings"
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

// poolBreakerContributionKey returns the breaker key an auth CONTRIBUTES
// failures/successes under. Only auths that opted in via the
// pool_circuit_breaker attribute participate (design G3: the opt-in gates
// contribution), keyed by the auth's compound provider_key.
func poolBreakerContributionKey(auth *Auth) string {
	if auth == nil || auth.Attributes == nil {
		return ""
	}
	if auth.Attributes[AttributePoolCircuitBreaker] != "true" {
		return ""
	}
	return strings.TrimSpace(auth.Attributes["provider_key"])
}

// poolBreakerSelectionKey returns the breaker key an auth is BLOCKED by during
// selection. The breaker is pool-keyed (design G3): every auth carrying the
// pool's provider_key is subject to the pool's breaker state, whether or not
// it itself opted in — the opt-in only gates who feeds the breaker.
func poolBreakerSelectionKey(auth *Auth) string {
	if auth == nil || auth.Attributes == nil {
		return ""
	}
	return strings.TrimSpace(auth.Attributes["provider_key"])
}

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

// blockDeadline reports whether the pool currently blocks selection reads and
// when it lifts. It is a READ: it never takes the probe slot (admission moved
// to admitProbe at execution commit), so repeated reads are idempotent — the
// only writes are the time-based relabels (an expired OPEN becomes HALF_OPEN;
// an expired probe window re-arms probeUsed). OPEN blocks until
// openedAt+resetPeriod; HALF_OPEN with a probe in flight blocks until the
// probe window anchored at admission time; HALF_OPEN without a probe does not
// block — commit enforces the single probe. CLOSED/missing never block.
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
		// Lazy relabel: the reset period elapsed, so a probe may be taken via
		// admitProbe. Selection reads stay admission-free on purpose — an
		// admission here was consumed by read paths (model filtering) that
		// never dispatch, leaving the pool unable to ever recover.
		entry.state = breakerHalfOpen
		entry.probeUsed = false
		return time.Time{}, false
	case breakerHalfOpen:
		if entry.probeUsed {
			// A probe is in flight; block until its anchored window elapses.
			probeDeadline := entry.openedAt.Add(entry.resetPeriod)
			if now.Before(probeDeadline) {
				return probeDeadline, true
			}
			// The probe window expired without a verdict; re-arm so the next
			// admitProbe can take a fresh probe.
			entry.probeUsed = false
		}
		// No probe in flight: selection may proceed; the single-probe
		// guarantee is enforced at commit.
		return time.Time{}, false
	default:
		return time.Time{}, false
	}
}

// admitProbe reports whether the caller may dispatch the pool's probe
// request, taking the probe slot when it does (the only place probeUsed goes
// false→true). Called at execution commit — after the auth is chosen and
// prepared, right before dispatch — so an admitted probe always corresponds
// to a real dispatch. Missing/CLOSED entries admit freely; OPEN never admits;
// HALF_OPEN admits only when no live probe is in flight (an expired probe
// window re-arms first) and re-anchors the window at admission time.
func (b *poolBreaker) admitProbe(key string, now time.Time) bool {
	if key == "" {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	entry, ok := b.pools[key]
	if !ok {
		return true
	}
	switch entry.state {
	case breakerOpen:
		return false
	case breakerHalfOpen:
		if entry.probeUsed {
			if now.Before(entry.openedAt.Add(entry.resetPeriod)) {
				return false
			}
			// The previous probe's window expired without a verdict: re-arm.
			entry.probeUsed = false
		}
		entry.probeUsed = true
		entry.openedAt = now // anchor the probe window at admission time
		return true
	default:
		return true
	}
}

// PoolBreakerRecord is one pool's live breaker state for observability.
type PoolBreakerRecord struct {
	PoolKey   string    `json:"pool_key"`
	State     string    `json:"state"` // closed|open|half_open
	Failures  int       `json:"failures"`
	OpenedAt  time.Time `json:"opened_at,omitempty"`
	OpenUntil time.Time `json:"open_until,omitempty"`
}

// PoolBreakerSnapshot returns the live breaker states (in-memory; not
// persisted). OpenUntil carries openedAt+resetPeriod while the pool blocks
// selection (OPEN, or HALF_OPEN with an unresolved probe) and is zero
// otherwise. Safe to call concurrently with the scheduler/selector.
func PoolBreakerSnapshot() []PoolBreakerRecord {
	globalPoolBreaker.mu.Lock()
	defer globalPoolBreaker.mu.Unlock()
	records := make([]PoolBreakerRecord, 0, len(globalPoolBreaker.pools))
	for key, entry := range globalPoolBreaker.pools {
		record := PoolBreakerRecord{
			PoolKey:  key,
			Failures: entry.failures,
		}
		switch entry.state {
		case breakerOpen:
			record.State = "open"
			record.OpenedAt = entry.openedAt
			record.OpenUntil = entry.openedAt.Add(entry.resetPeriod)
		case breakerHalfOpen:
			record.State = "half_open"
			record.OpenedAt = entry.openedAt
			// OpenUntil only reflects a real block: a relabeled HALF_OPEN
			// without a live probe does not block selection (commit admits
			// the next probe), so it reports no open deadline.
			if entry.probeUsed {
				record.OpenUntil = entry.openedAt.Add(entry.resetPeriod)
			}
		default:
			record.State = "closed"
		}
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool {
		return records[i].PoolKey < records[j].PoolKey
	})
	return records
}
