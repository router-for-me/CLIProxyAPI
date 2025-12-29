package quota

import (
	"sync"
	"time"
)

// DefaultCacheTTL is the default time-to-live for cached quota data.
const DefaultCacheTTL = 60 * time.Second

// CacheEntry represents a cached quota entry with expiration.
type CacheEntry struct {
	Data      *ProviderQuotaData
	ExpiresAt time.Time
}

// IsExpired returns true if the cache entry has expired.
func (e *CacheEntry) IsExpired() bool {
	return time.Now().After(e.ExpiresAt)
}

// QuotaCache provides TTL-based caching for quota data.
type QuotaCache struct {
	mu      sync.RWMutex
	entries map[string]*CacheEntry
	ttl     time.Duration
}

// NewQuotaCache creates a new quota cache with the given TTL.
func NewQuotaCache(ttl time.Duration) *QuotaCache {
	if ttl <= 0 {
		ttl = DefaultCacheTTL
	}
	return &QuotaCache{
		entries: make(map[string]*CacheEntry),
		ttl:     ttl,
	}
}

// cacheKey generates a cache key from provider and account ID.
func cacheKey(provider, accountID string) string {
	return provider + ":" + accountID
}

// Get retrieves quota data from cache if available and not expired.
func (c *QuotaCache) Get(provider, accountID string) (*ProviderQuotaData, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	key := cacheKey(provider, accountID)
	entry, exists := c.entries[key]
	if !exists || entry.IsExpired() {
		return nil, false
	}
	return entry.Data, true
}

// Set stores quota data in cache with the configured TTL.
func (c *QuotaCache) Set(provider, accountID string, data *ProviderQuotaData) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := cacheKey(provider, accountID)
	c.entries[key] = &CacheEntry{
		Data:      data,
		ExpiresAt: time.Now().Add(c.ttl),
	}
}

// Invalidate removes a specific entry from cache.
func (c *QuotaCache) Invalidate(provider, accountID string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := cacheKey(provider, accountID)
	delete(c.entries, key)
}

// InvalidateProvider removes all entries for a specific provider.
func (c *QuotaCache) InvalidateProvider(provider string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	prefix := provider + ":"
	for key := range c.entries {
		if len(key) > len(prefix) && key[:len(prefix)] == prefix {
			delete(c.entries, key)
		}
	}
}

// InvalidateAll clears the entire cache.
func (c *QuotaCache) InvalidateAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = make(map[string]*CacheEntry)
}

// Cleanup removes expired entries from the cache.
func (c *QuotaCache) Cleanup() {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := time.Now()
	for key, entry := range c.entries {
		if now.After(entry.ExpiresAt) {
			delete(c.entries, key)
		}
	}
}

// SetTTL updates the TTL for new cache entries.
func (c *QuotaCache) SetTTL(ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if ttl > 0 {
		c.ttl = ttl
	}
}