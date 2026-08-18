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
// unbounded growth. When exceeded a single (non-current) entry is evicted per
// map (rows and totals are bounded independently).
const usageCacheMaxEntries = 512

// cacheValue is the single cached shape for both aggregate reads: a grouped
// row set (values) or a scalar roll-up (total). isTotal disambiguates which
// field is meaningful.
type cacheValue struct {
	values   []UsageAggregate // row set when !isTotal
	total    UsageAggregate   // scalar when isTotal
	isTotal  bool
	storedAt time.Time
}

// call tracks a single in-flight loader invocation for a key. Waiters block on
// done; the sole goroutine that created the call fills val/err and closes it
// exactly once (under the cache mutex), so the close is never double-fired.
type call struct {
	done chan struct{}
	val  cacheValue
	err  error
}

// usageCache is a small, TTL-aware, read-through cache for aggregate usage
// reads. It is safe for concurrent use. Single-flight: concurrent callers for
// the same key share one loader invocation; the loader always runs WITHOUT the
// cache mutex held. Fail-open: loader errors are never cached. Rows and totals
// live in separate maps, so the two shapes for the same logical filter are
// cached independently.
type usageCache struct {
	now      func() time.Time // clock source; overridable in tests.
	mu       sync.Mutex
	rows     map[string]cacheValue
	totals   map[string]cacheValue
	inflight map[string]*call
}

func newUsageCache() *usageCache {
	return &usageCache{
		now:      time.Now,
		rows:     make(map[string]cacheValue),
		totals:   make(map[string]cacheValue),
		inflight: make(map[string]*call),
	}
}

// newUsageCacheWithClock builds a cache with an injected clock, used by tests
// to advance time past the TTL without real sleeps.
func newUsageCacheWithClock(now func() time.Time) *usageCache {
	c := newUsageCache()
	c.now = now
	return c
}

// inflightKey routes rows vs totals loads into distinct single-flight slots so
// the two shapes for a filter never share a loader.
func (c *usageCache) inflightKey(isTotal bool, filterKey string) string {
	if isTotal {
		return "t\x1f" + filterKey
	}
	return "r\x1f" + filterKey
}

// get is the shared read-through + single-flight core. It returns a cached
// value when fresh, otherwise exactly one caller runs load and the rest wait on
// its result. Errors are returned to callers but never cached.
func (c *usageCache) get(isTotal bool, filterKey string, load func() (cacheValue, error)) (cacheValue, error) {
	ik := c.inflightKey(isTotal, filterKey)
	now := c.now()

	c.mu.Lock()
	// Look up the value in the appropriate map.
	var cached cacheValue
	ok := false
	if isTotal {
		cached, ok = c.totals[filterKey]
	} else {
		cached, ok = c.rows[filterKey]
	}
	// Fresh cache hit: serve from memory.
	if ok && now.Sub(cached.storedAt) < usageCacheTTL {
		c.mu.Unlock()
		return cached, nil
	}
	// Another caller is already loading this key: wait for its result.
	if cl, ok := c.inflight[ik]; ok {
		c.mu.Unlock()
		<-cl.done
		return cl.val, cl.err
	}
	// Miss: become the leader for this key.
	cl := &call{done: make(chan struct{})}
	c.inflight[ik] = cl
	c.mu.Unlock()

	// Run the loader outside the cache mutex so concurrent DB I/O is not
	// serialized behind the lock.
	val, err := load()

	c.mu.Lock()
	delete(c.inflight, ik)
	if err == nil {
		val.storedAt = c.now()
		if isTotal {
			c.totals[filterKey] = val
			if len(c.totals) > usageCacheMaxEntries {
				for k := range c.totals {
					if k != filterKey {
						delete(c.totals, k)
						break
					}
				}
			}
		} else {
			c.rows[filterKey] = val
			if len(c.rows) > usageCacheMaxEntries {
				for k := range c.rows {
					if k != filterKey {
						delete(c.rows, k)
						break
					}
				}
			}
		}
	}
	cl.val = val
	cl.err = err
	close(cl.done)
	c.mu.Unlock()
	return val, err
}

// getAggregate serves a grouped aggregate row set through the cache.
func (c *usageCache) getAggregate(ctx context.Context, filter UsageFilter, load func() ([]UsageAggregate, error)) ([]UsageAggregate, error) {
	v, err := c.get(false, usageFilterKey(filter), func() (cacheValue, error) {
		rows, lerr := load()
		return cacheValue{values: rows}, lerr
	})
	return v.values, err
}

// getTotals serves a single roll-up aggregate through the cache.
func (c *usageCache) getTotals(ctx context.Context, filter UsageFilter, load func() (UsageAggregate, error)) (UsageAggregate, error) {
	v, err := c.get(true, usageFilterKey(filter), func() (cacheValue, error) {
		tot, lerr := load()
		return cacheValue{total: tot, isTotal: true}, lerr
	})
	return v.total, err
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
		f.RouterID,
		f.UserID,
		f.From.Format(time.RFC3339Nano),
		f.To.Format(time.RFC3339Nano),
		f.GroupBy,
		itoa(f.Limit),
	}, "\x1f")
}
