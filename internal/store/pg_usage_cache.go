package store

import (
	"context"
	"strings"
	"sync"
	"time"
)

// usageCacheTTL bounds how stale a cached aggregate may be. The underlying
// usage_events change on the ~10s flush cadence, so 15s both absorbs polling
// bursts and stays fresh.
const usageCacheTTL = 15 * time.Second

// usageCacheMaxEntries bounds the number of distinct cached filters to prevent
// unbounded growth. When exceeded a single (non-current) entry is evicted.
const usageCacheMaxEntries = 512

// aggKind distinguishes the two value shapes the cache stores under a filter:
// a grouped row set (SelectAggregate) vs a single roll-up (SelectTotals). The
// two kinds share the same logical filter key, so the kind is folded into the
// physical map key to keep them independent.
type aggKind int

const (
	aggRows aggKind = iota
	aggTotals
)

// aggCacheEntry is one cached value. Both rows and tot are stored so the entry
// is self-describing; only the field matching kind is meaningful.
type aggCacheEntry struct {
	rows     []UsageAggregate
	tot      UsageAggregate
	kind     aggKind
	storedAt time.Time
}

// aggResult is the loader's output plus the kind it produced.
type aggResult struct {
	rows []UsageAggregate
	tot  UsageAggregate
	kind aggKind
}

// call tracks a single in-flight loader invocation for a key. Waiters block on
// done; the sole goroutine that created the call fills res/err and closes it
// exactly once (under the cache mutex), so the close is never double-fired.
type call struct {
	done chan struct{}
	res  aggResult
	err  error
}

// usageCache is a small, TTL-aware, read-through cache for aggregate usage
// reads. It is safe for concurrent use. Single-flight: concurrent callers for
// the same key share one loader invocation; the loader always runs WITHOUT the
// cache mutex held. Fail-open: loader errors are never cached.
type usageCache struct {
	mu       sync.Mutex
	entries  map[string]aggCacheEntry
	inflight map[string]*call
}

func newUsageCache() *usageCache {
	return &usageCache{
		entries:  make(map[string]aggCacheEntry),
		inflight: make(map[string]*call),
	}
}

// mapKey folds the value kind into the physical map key so aggregate-rows and
// totals for the same filter never collide.
func (c *usageCache) mapKey(kind aggKind, filterKey string) string {
	return string(rune('a'+int(kind))) + "\x1f" + filterKey
}

// get is the shared read-through + single-flight core. It returns a cached
// value when fresh, otherwise exactly one caller runs load and the rest wait on
// its result. Errors are returned to callers but never cached.
func (c *usageCache) get(filterKey string, kind aggKind, load func() (aggResult, error)) (aggResult, error) {
	key := c.mapKey(kind, filterKey)
	now := time.Now()

	c.mu.Lock()
	// Fresh cache hit: serve from memory.
	if e, ok := c.entries[key]; ok && now.Sub(e.storedAt) < usageCacheTTL {
		c.mu.Unlock()
		return aggResult{rows: e.rows, tot: e.tot, kind: e.kind}, nil
	}
	// Another caller is already loading this key: wait for its result.
	if cl, ok := c.inflight[key]; ok {
		c.mu.Unlock()
		<-cl.done
		return cl.res, cl.err
	}
	// Miss: become the leader for this key.
	cl := &call{done: make(chan struct{})}
	c.inflight[key] = cl
	c.mu.Unlock()

	// Run the loader outside the cache mutex so concurrent DB I/O is not
	// serialized behind the lock.
	res, err := load()

	c.mu.Lock()
	delete(c.inflight, key)
	if err == nil {
		c.entries[key] = aggCacheEntry{rows: res.rows, tot: res.tot, kind: res.kind, storedAt: time.Now()}
		if len(c.entries) > usageCacheMaxEntries {
			// Simple capacity eviction: drop one entry that is not the one we
			// just stored. Map iteration order is random, so this is a cheap
			// approximate-LRU stand-in.
			for k := range c.entries {
				if k != key {
					delete(c.entries, k)
					break
				}
			}
		}
	}
	cl.res = res
	cl.err = err
	close(cl.done)
	c.mu.Unlock()
	return res, err
}

// getAggregate serves a grouped aggregate row set through the cache.
func (c *usageCache) getAggregate(ctx context.Context, filter UsageFilter, load func() ([]UsageAggregate, error)) ([]UsageAggregate, error) {
	res, err := c.get(usageFilterKey(filter), aggRows, func() (aggResult, error) {
		rows, lerr := load()
		return aggResult{rows: rows, kind: aggRows}, lerr
	})
	return res.rows, err
}

// getTotals serves a single roll-up aggregate through the cache.
func (c *usageCache) getTotals(ctx context.Context, filter UsageFilter, load func() (UsageAggregate, error)) (UsageAggregate, error) {
	res, err := c.get(usageFilterKey(filter), aggTotals, func() (aggResult, error) {
		tot, lerr := load()
		return aggResult{tot: tot, kind: aggTotals}, lerr
	})
	return res.tot, err
}

// UsageFilterIncludeRequestID reports whether the filter targets a single
// request (RequestID). Request-scoped reads are inherently unique one-row
// lookups and are skipped by the aggregate cache.
func UsageFilterIncludeRequestID(f UsageFilter) bool { return f.RequestID != "" }

// usageFilterKey canonicalizes a UsageFilter into a stable cache key. It
// includes every query-shaping field (predicates, time range, group-by, limit)
// but EXCLUDES RequestID, which routes to the uncached path. Fields are joined
// with the ASCII unit separator (0x1F) — a byte that cannot appear in any of
// the string fields — so the key is unambiguous.
func usageFilterKey(f UsageFilter) string {
	return strings.Join([]string{
		f.APIKeyID,
		f.Principal,
		f.Provider,
		f.Model,
		f.UserID,
		f.From.Format(time.RFC3339Nano),
		f.To.Format(time.RFC3339Nano),
		f.GroupBy,
		itoa(f.Limit),
	}, "\x1f")
}
