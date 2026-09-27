package precompact

import (
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/cache"
)

// Entry is a cached summary for one session.
type Entry struct {
	// Covered is how many middle messages the summary covers.
	Covered int
	// PrefixHash hashes those covered messages.
	PrefixHash string
	Summary    string
	ExpiresAt  time.Time
}

// Cache is a bounded, TTL-aware per-session summary cache.
type Cache struct {
	mu  sync.Mutex
	lru *cache.BoundedLRU[string, *Entry]
	ttl time.Duration
}

// NewCache builds a cache holding at most maxSessions entries alive for ttl.
func NewCache(maxSessions int, ttl time.Duration) *Cache {
	if ttl <= 0 {
		ttl = 2 * time.Hour
	}
	return &Cache{lru: cache.NewBoundedLRU[string, *Entry](maxSessions, nil), ttl: ttl}
}

// Get returns a live entry for session.
func (c *Cache) Get(session string) (Entry, bool) {
	if c == nil || session == "" {
		return Entry{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.lru.Get(session)
	if !ok || e == nil || time.Now().After(e.ExpiresAt) {
		return Entry{}, false
	}
	return *e, true
}

// Put stores or replaces the entry for session.
func (c *Cache) Put(session string, e Entry) {
	if c == nil || session == "" {
		return
	}
	e.ExpiresAt = time.Now().Add(c.ttl)
	c.mu.Lock()
	defer c.mu.Unlock()
	slot := c.lru.GetOrAdd(session, func() *Entry { return &Entry{} })
	*slot = e
}
