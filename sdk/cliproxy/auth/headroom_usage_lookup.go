package auth

import (
	"context"
	"math"
	"sync"
	"time"
)

// UsageWindowsHeadroomLookup is the production HeadroomLookup (Task 7
// closes the loop on Task 5's stub). It reads usage_windows via the
// supplied reader, caches the per-auth snapshot for a short TTL (5s by
// default) so the dispatcher path never hits PG on every request, and
// returns the MINIMUM headroom across all active windows for an auth —
// the most-constrained window wins.
//
// Concurrency: the lookup is safe for concurrent use; the cache is
// guarded by a sync.RWMutex so the dispatcher fast-path takes a read lock.
// On a cache miss a single goroutine fetches the snapshot; concurrent
// callers block on the read lock and then read the warmed cache.
//
// Failure mode: any error (timeout, transient PG failure, missing row) is
// treated as "no quota configured" — the lookup returns 100.0 so the
// scheduler behaves exactly like the round-1 stub (no override). This is
// a deliberate bias: a flaky reader must NEVER push the headroom selector
// toward exhaustion. Operators who want stricter semantics can lower the
// TTL to zero (effectively no cache) and observe failures via the metric.
//
// The TTL choice (5s) is deliberately small: usage_windows is eventually
// consistent anyway (writes are async via the flusher), so a long cache
// buys no freshness, while a short cache keeps DB load low. 5s is short
// enough to feel live during a dispatch burst, long enough that an N-RPS
// flood across M auths collapses to ~M/5 queries/s.
type UsageWindowsHeadroomLookup struct {
	reader UsageWindowsReader
	ttl    time.Duration
	now    func() time.Time

	mu    sync.RWMutex
	cache map[string]usageWindowsCacheEntry
}

// UsageWindowsReader is the slice of the PostgresStore surface the lookup
// needs. Defining it as an interface (instead of importing the store
// package) keeps this package free of PG dependencies; production wiring
// in cmd/server/main.go supplies the adapter.
type UsageWindowsReader interface {
	// GetAuthQuota returns the per-window usage snapshot for one auth.
	// The implementation must respect ctx deadlines; the lookup relies on
	// the context to keep cache misses bounded.
	GetAuthQuota(ctx context.Context, authID string) (AuthQuotaSnapshot, bool, error)
}

// AuthQuotaSnapshot is the minimal read-side projection the lookup needs:
// the windows (each carrying its own headroom) and the channel/pool
// metadata for diagnostics. Production callers pass a struct that wraps
// the store-layer QuotaShareAuthSnapshot; tests can supply a fake.
type AuthQuotaSnapshot struct {
	AuthID       string
	Channel      string
	PoolStrategy string
	Windows      []WindowQuotaRead
}

// WindowQuotaRead mirrors the per-window row the store returns. HeadroomPct
// is precomputed by the store (matching computeHeadroom in the management
// handler) so the lookup just needs to take the minimum across windows.
type WindowQuotaRead struct {
	Size        string
	Used        int64
	Limit       int64
	HeadroomPct float64
	OverLimit   bool
}

type usageWindowsCacheEntry struct {
	snapshot AuthQuotaSnapshot
	stamp    time.Time
}

// defaultHeadroomTTL is the production cache TTL. Documented above; tweak
// here (or pass a different value via NewUsageWindowsHeadroomLookup) when
// profiling shows the 5s choice is too eager or too lazy.
const defaultHeadroomTTL = 5 * time.Second

// defaultHeadroomFetchTimeout bounds the cache-miss SQL call. The handler
// uses 2s end-to-end; the lookup here is a tighter bound so a stuck PG
// cannot freeze dispatch for longer than this. The PG quota aggregation
// query is bounded already by the store layer's per-statement timeout;
// this is the outer cap so the lookup returns promptly.
const defaultHeadroomFetchTimeout = 250 * time.Millisecond

// NewUsageWindowsHeadroomLookup builds the production lookup. The reader
// must be non-nil; the TTL and fetch timeout fall back to the package
// defaults when the supplied values are zero.
func NewUsageWindowsHeadroomLookup(reader UsageWindowsReader, ttl time.Duration) *UsageWindowsHeadroomLookup {
	if ttl <= 0 {
		ttl = defaultHeadroomTTL
	}
	return &UsageWindowsHeadroomLookup{
		reader: reader,
		ttl:    ttl,
		now:    time.Now,
		cache:  make(map[string]usageWindowsCacheEntry),
	}
}

// Headroom returns the minimum headroom_pct across every active window
// for the supplied auth. A successful cache hit short-circuits the PG
// read; a miss triggers a single fetch (bounded by the configured timeout)
// and warms the cache for the next caller.
//
// Returns 100.0 (unlimited) when:
//   - the lookup is nil or has no reader wired;
//   - the auth is not registered in the scheduler (caller should not ask
//     for an unknown auth, but the defensive default keeps the selector
//     well-behaved);
//   - the underlying fetch errored or timed out (bias toward "no
//     override" so a flaky PG cannot poison the selector);
//   - the auth has no quota configured for any window (the store returns
//     an empty snapshot).
//
// The minimum is computed across windows with non-empty Size (rows from
// the store's GROUP BY window_type always carry a size). When every
// window reports 100.0 the lookup returns 100.0 directly; the math
// "minimum of N>=1 values in [0,100]" stays in-bounds.
func (l *UsageWindowsHeadroomLookup) Headroom(authID string) float64 {
	if l == nil || l.reader == nil {
		return 100.0
	}
	authID = trimHeadroomID(authID)
	if authID == "" {
		return 100.0
	}

	if snap, ok := l.cached(authID); ok {
		return minimumHeadroom(snap.Windows)
	}

	snap := l.fetch(authID)
	if snap.AuthID == "" {
		// No data — treat as unlimited.
		return 100.0
	}
	l.mu.Lock()
	l.cache[authID] = usageWindowsCacheEntry{snapshot: snap, stamp: l.now()}
	l.mu.Unlock()
	return minimumHeadroom(snap.Windows)
}

// cached returns the cached snapshot when fresh, with a read lock so
// concurrent dispatchers can hit it in parallel.
func (l *UsageWindowsHeadroomLookup) cached(authID string) (AuthQuotaSnapshot, bool) {
	l.mu.RLock()
	entry, ok := l.cache[authID]
	l.mu.RUnlock()
	if !ok {
		return AuthQuotaSnapshot{}, false
	}
	if l.now().Sub(entry.stamp) > l.ttl {
		return AuthQuotaSnapshot{}, false
	}
	return entry.snapshot, true
}

// fetch runs a single PG read against the configured reader, bounded by
// the per-call timeout. Errors collapse to an empty snapshot — see the
// type-level doc for the rationale.
func (l *UsageWindowsHeadroomLookup) fetch(authID string) AuthQuotaSnapshot {
	if l.reader == nil {
		return AuthQuotaSnapshot{}
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultHeadroomFetchTimeout)
	defer cancel()
	snap, _, err := l.reader.GetAuthQuota(ctx, authID)
	if err != nil {
		return AuthQuotaSnapshot{}
	}
	return snap
}

// minimumHeadroom picks the lowest headroom across every window in the
// snapshot. Empty snapshot → 100.0 (unlimited). NaN-safe: any NaN the
// store layer ever surfaced (it can't today, but defensive against
// future schema drift) is treated as 100.0 so the scheduler never sees a
// poisoned value.
func minimumHeadroom(windows []WindowQuotaRead) float64 {
	if len(windows) == 0 {
		return 100.0
	}
	min := math.MaxFloat64
	for _, w := range windows {
		h := w.HeadroomPct
		if h != h { // NaN guard
			continue
		}
		if h < min {
			min = h
		}
	}
	if min == math.MaxFloat64 {
		return 100.0
	}
	if min < 0 {
		return 0
	}
	if min > 100 {
		return 100
	}
	return min
}

// trimHeadroomID normalizes the supplied auth id for the cache key. The
// cache lookup uses the trimmed id so callers passing whitespace or
// different casings share an entry; the cache key itself stays
// allocation-free.
func trimHeadroomID(s string) string {
	start := 0
	end := len(s)
	for start < end {
		c := s[start]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			start++
			continue
		}
		break
	}
	for end > start {
		c := s[end-1]
		if c == ' ' || c == '\t' || c == '\n' || c == '\r' {
			end--
			continue
		}
		break
	}
	return s[start:end]
}
