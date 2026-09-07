package autorouter

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
)

// scoreCacheCapacity bounds the process-local score cache. Sized for the
// realistic concurrency of one proxy process; entries are small (score
// results, never request bodies), so memory stays in the low single-digit MB.
const scoreCacheCapacity = 2048

// scoreCache is a mutex-guarded FIFO cache mapping a request fingerprint to a
// computed ScoreResult. It is best-effort by design: callers fall back to
// direct computation on any miss, so the cache can never become a failure
// point. Keys are SHA-256 digests — raw bodies are never stored or exposed.
//
// Eviction is FIFO via a ring buffer of keys (KISS: no LRU bookkeeping per
// access; repeated identical requests hit the map regardless of order, and a
// fixed ring bounds memory deterministically).
type scoreCache struct {
	mu    sync.Mutex
	items map[string]ScoreResult
	ring  []string
	next  int
}

// NewScoreCache builds a cache with the given capacity (ring-buffer size;
// <= 0 uses scoreCacheCapacity).
func NewScoreCache(capacity int) *scoreCache {
	if capacity <= 0 {
		capacity = scoreCacheCapacity
	}
	return &scoreCache{
		items: make(map[string]ScoreResult, capacity),
		ring:  make([]string, capacity),
	}
}

// Key derives the cache key for one scoring input: the body bytes plus every
// input that changes the outcome (entry protocol, owning router, and the
// profile hash — a profile upsert changes the hash, so it invalidates
// naturally without an explicit purge).
func (c *scoreCache) Key(rawJSON []byte, format, routerID, profileHash string) string {
	h := sha256.New()
	h.Write(rawJSON)
	for _, s := range []string{format, routerID, profileHash} {
		h.Write([]byte{0})
		h.Write([]byte(s))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Get returns the cached result for key, if present.
func (c *scoreCache) Get(key string) (ScoreResult, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	res, ok := c.items[key]
	return res, ok
}

// Put stores a result, evicting the oldest entry when at capacity.
func (c *scoreCache) Put(key string, res ScoreResult) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.items[key]; !exists {
		// Evict whatever the ring slot points at before claiming it.
		if old := c.ring[c.next]; old != "" {
			delete(c.items, old)
		}
		c.ring[c.next] = key
		c.next = (c.next + 1) % len(c.ring)
	}
	c.items[key] = res
}

// len reports the number of live entries (test/observability helper).
func (c *scoreCache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}
