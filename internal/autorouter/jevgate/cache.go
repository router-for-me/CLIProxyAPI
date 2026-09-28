package jevgate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sync"
)

// defaultCacheCapacity bounds the process-local verdict cache. Entries are a
// few hundred bytes, so memory stays well under a megabyte.
const defaultCacheCapacity = 2048

// Verdict is the outcome of a classification: what the classifier chose, how
// confident it was, and how the decision was reached. It is cached and
// persisted verbatim so operators can tune thresholds against the recorded
// distribution rather than guessing.
type Verdict struct {
	Choice        string             `json:"choice,omitempty"`
	Confidence    float64            `json:"confidence"`
	Probabilities map[string]float64 `json:"probabilities,omitempty"`
	Model         string             `json:"model,omitempty"`
	LatencyMs     int64              `json:"latency_ms"`
	InputTokens   int                `json:"input_tokens"`
	// Cache is "hit" or "miss" — whether this verdict came from the cache.
	Cache string `json:"cache,omitempty"`
	// Verdict is how the decision was reached: VerdictAccepted,
	// VerdictLowConfidence, VerdictError, or VerdictBreakerOpen.
	Verdict string `json:"verdict,omitempty"`
}

// Verdict outcome values (Verdict.Verdict).
const (
	VerdictAccepted      = "accepted"
	VerdictLowConfidence = "rejected_low_confidence"
	VerdictError         = "error"
	VerdictBreakerOpen   = "breaker_open"
)

// Cache marker values (Verdict.Cache).
const (
	CacheHit  = "hit"
	CacheMiss = "miss"
)

// Cache is a mutex-guarded FIFO cache mapping a state fingerprint to a Verdict.
// It is best-effort by design: a miss simply means the classifier is called, so
// the cache can never become a failure point. Keys are SHA-256 digests — no
// prompt text is stored.
//
// Eviction is FIFO via a ring of keys (same reasoning as the scorer's cache:
// no LRU bookkeeping, repeated identical requests hit regardless of order, and
// memory is bounded deterministically).
type Cache struct {
	mu    sync.Mutex
	items map[string]Verdict
	ring  []string
	next  int
}

// NewCache builds a cache with the given capacity (<= 0 uses the default).
func NewCache(capacity int) *Cache {
	if capacity <= 0 {
		capacity = defaultCacheCapacity
	}
	return &Cache{
		items: make(map[string]Verdict, capacity),
		ring:  make([]string, capacity),
	}
}

// Key derives the cache key for one classification input. It deliberately keys
// on the derived state rather than the raw request body: the same conversational
// turn then hits regardless of accumulated history. Every input that changes the
// outcome participates, including questionHash (so a rubric edit invalidates)
// and model (so a model change does not serve stale thresholds).
func (c *Cache) Key(format, routerID string, st State, model, questionHash string) string {
	h := sha256.New()
	// Marshal the state so nested metadata participates without manual field
	// enumeration. encoding/json sorts map keys, so this stays deterministic.
	if raw, errMarshal := json.Marshal(st); errMarshal == nil {
		h.Write(raw)
	}
	for _, s := range []string{format, routerID, model, questionHash} {
		h.Write([]byte{0})
		h.Write([]byte(s))
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Get returns the cached verdict for key, if present.
func (c *Cache) Get(key string) (Verdict, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	v, ok := c.items[key]
	return v, ok
}

// Put stores a verdict, evicting the oldest entry when at capacity.
func (c *Cache) Put(key string, v Verdict) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.items[key]; !exists {
		if old := c.ring[c.next]; old != "" {
			delete(c.items, old)
		}
		c.ring[c.next] = key
		c.next = (c.next + 1) % len(c.ring)
	}
	c.items[key] = v
}

// len reports the number of live entries (test helper).
func (c *Cache) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}
