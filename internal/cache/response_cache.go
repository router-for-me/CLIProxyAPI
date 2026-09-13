package cache

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ResponseCacheStats reports cumulative counters for the upstream response cache.
type ResponseCacheStats struct {
	Hits      uint64
	Misses    uint64
	Stores    uint64
	Evictions uint64
	Entries   int
}

// ResponseCacheEntry holds a cached upstream payload.
//
// For non-streaming responses Payload carries the raw upstream body. For
// streaming responses Payload carries the concatenated raw SSE frames exactly as
// they were received, so a replay can be fed back through the same translator
// pipeline that produced the original downstream output.
type ResponseCacheEntry struct {
	Payload   []byte
	Headers   http.Header
	Stream    bool
	StoredAt  time.Time
	expiresAt time.Time
}

// ResponseCache stores upstream responses keyed by a deterministic request
// signature. It exists because some upstream aggregators bill every request at
// full price and provide no prompt caching of their own: an identical retry,
// duplicated subagent probe, or replayed deterministic request would otherwise
// be paid for twice.
//
// The cache is intentionally conservative. Only exact request matches hit, and
// entries expire quickly so agents keep observing fresh upstream behavior.
type ResponseCache struct {
	mu         sync.Mutex
	entries    map[string]*responseCacheNode
	order      []string
	maxEntries int
	maxBytes   int
	ttl        time.Duration

	hits      atomic.Uint64
	misses    atomic.Uint64
	stores    atomic.Uint64
	evictions atomic.Uint64
}

type responseCacheNode struct {
	entry ResponseCacheEntry
}

const (
	// DefaultResponseCacheTTL is the fallback lifetime for cached upstream responses.
	DefaultResponseCacheTTL = 5 * time.Minute

	// DefaultResponseCacheMaxEntries bounds the number of retained responses.
	DefaultResponseCacheMaxEntries = 256

	// DefaultResponseCacheMaxEntryBytes bounds the size of a single cached response.
	DefaultResponseCacheMaxEntryBytes = 2 << 20
)

// NewResponseCache builds a bounded response cache. Non-positive arguments fall
// back to the package defaults.
func NewResponseCache(ttl time.Duration, maxEntries, maxEntryBytes int) *ResponseCache {
	if ttl <= 0 {
		ttl = DefaultResponseCacheTTL
	}
	if maxEntries <= 0 {
		maxEntries = DefaultResponseCacheMaxEntries
	}
	if maxEntryBytes <= 0 {
		maxEntryBytes = DefaultResponseCacheMaxEntryBytes
	}
	return &ResponseCache{
		entries:    make(map[string]*responseCacheNode, maxEntries),
		order:      make([]string, 0, maxEntries),
		maxEntries: maxEntries,
		maxBytes:   maxEntryBytes,
		ttl:        ttl,
	}
}

// ResponseCacheKey builds a stable cache key from the request coordinates, the
// effective upstream headers, and the exact upstream payload. Header names and
// values are sorted so equivalent maps produce the same key regardless of
// iteration order. Any difference produces a separate entry.
func ResponseCacheKey(provider, url, model string, stream bool, headers http.Header, payload []byte) string {
	hasher := sha256.New()
	hasher.Write([]byte(provider))
	hasher.Write([]byte{0})
	hasher.Write([]byte(url))
	hasher.Write([]byte{0})
	hasher.Write([]byte(model))
	hasher.Write([]byte{0})
	if stream {
		hasher.Write([]byte{1})
	} else {
		hasher.Write([]byte{0})
	}
	hasher.Write([]byte{0})

	normalizedHeaders := make(map[string][]string, len(headers))
	for name, values := range headers {
		lowerName := strings.ToLower(name)
		normalizedHeaders[lowerName] = append(normalizedHeaders[lowerName], values...)
	}
	headerNames := make([]string, 0, len(normalizedHeaders))
	for name := range normalizedHeaders {
		headerNames = append(headerNames, name)
	}
	sort.Strings(headerNames)
	for _, name := range headerNames {
		values := append([]string(nil), normalizedHeaders[name]...)
		sort.Strings(values)
		hasher.Write([]byte(name))
		hasher.Write([]byte{0})
		for _, value := range values {
			hasher.Write([]byte(value))
			hasher.Write([]byte{0})
		}
	}
	hasher.Write([]byte{0})
	hasher.Write(payload)
	return hex.EncodeToString(hasher.Sum(nil))
}

// Get returns a live cached entry for key when present.
func (c *ResponseCache) Get(key string) (ResponseCacheEntry, bool) {
	if c == nil || key == "" {
		return ResponseCacheEntry{}, false
	}
	now := time.Now()
	c.mu.Lock()
	node, ok := c.entries[key]
	if ok && now.After(node.entry.expiresAt) {
		c.removeLocked(key)
		ok = false
	}
	var entry ResponseCacheEntry
	if ok {
		entry = node.entry
		c.touchLocked(key)
	}
	c.mu.Unlock()

	if !ok {
		c.misses.Add(1)
		return ResponseCacheEntry{}, false
	}
	c.hits.Add(1)
	return entry, true
}

// Store records a payload for key along with upstream response headers.
// Oversized payloads are dropped instead of evicting useful smaller entries.
func (c *ResponseCache) Store(key string, payload []byte, headers http.Header, stream bool) {
	if c == nil || key == "" || len(payload) == 0 || len(payload) > c.maxBytes {
		return
	}
	stored := make([]byte, len(payload))
	copy(stored, payload)
	var storedHeaders http.Header
	if headers != nil {
		storedHeaders = headers.Clone()
	}
	now := time.Now()

	c.mu.Lock()
	if _, exists := c.entries[key]; exists {
		c.removeLocked(key)
	}
	c.entries[key] = &responseCacheNode{entry: ResponseCacheEntry{
		Payload:   stored,
		Headers:   storedHeaders,
		Stream:    stream,
		StoredAt:  now,
		expiresAt: now.Add(c.ttl),
	}}
	c.order = append(c.order, key)
	evicted := 0
	for len(c.order) > c.maxEntries {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.entries, oldest)
		evicted++
	}
	c.mu.Unlock()

	c.stores.Add(1)
	if evicted > 0 {
		c.evictions.Add(uint64(evicted))
	}
}

// Stats returns a snapshot of the cache counters.
func (c *ResponseCache) Stats() ResponseCacheStats {
	if c == nil {
		return ResponseCacheStats{}
	}
	c.mu.Lock()
	entries := len(c.entries)
	c.mu.Unlock()
	return ResponseCacheStats{
		Hits:      c.hits.Load(),
		Misses:    c.misses.Load(),
		Stores:    c.stores.Load(),
		Evictions: c.evictions.Load(),
		Entries:   entries,
	}
}

// Purge drops every cached entry.
func (c *ResponseCache) Purge() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.entries = make(map[string]*responseCacheNode, c.maxEntries)
	c.order = c.order[:0]
	c.mu.Unlock()
}

func (c *ResponseCache) removeLocked(key string) {
	delete(c.entries, key)
	for i := range c.order {
		if c.order[i] == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
}

func (c *ResponseCache) touchLocked(key string) {
	for i := range c.order {
		if c.order[i] == key {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
	c.order = append(c.order, key)
}

// ResponseCacheModelAllowed reports whether model is covered by an allowlist.
// An empty allowlist covers every model.
func ResponseCacheModelAllowed(allowlist []string, model string) bool {
	if len(allowlist) == 0 {
		return true
	}
	model = strings.ToLower(strings.TrimSpace(model))
	if model == "" {
		return false
	}
	for _, candidate := range allowlist {
		if strings.EqualFold(strings.TrimSpace(candidate), model) {
			return true
		}
	}
	return false
}
