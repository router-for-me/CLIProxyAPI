package auth

import (
	"sync"
	"time"
)

// CredentialCache caches the last successfully used Auth for a provider key
// so subsequent requests to the same provider can skip the round-robin
// credential selection. Entries expire after a configurable TTL to allow
// credential rotation/refresh to propagate.
//
// The cache is keyed by the provider string (e.g. "anthropic", "gemini").
// A single Auth pointer is stored; when the conductor successfully uses a
// credential it calls MarkSuccess to update the cache. When a credential
// fails, the caller calls Evict to remove the entry so the next request picks
// a fresh credential (or the next in the round-robin).
type CredentialCache struct {
	mu      sync.Mutex
	entries map[string]*credentialEntry
	ttl     time.Duration
}

type credentialEntry struct {
	auth    *Auth
	addedAt time.Time
}

// NewCredentialCache creates a cache with the given TTL. A TTL of zero
// disables caching implicitly (every lookup will find an expired entry).
func NewCredentialCache(ttl time.Duration) *CredentialCache {
	if ttl <= 0 {
		ttl = 30 * time.Second // default: cache for 30s
	}
	return &CredentialCache{
		entries: make(map[string]*credentialEntry),
		ttl:     ttl,
	}
}

// Get returns a cached Auth for provider, or nil on miss/expiry.
func (c *CredentialCache) Get(provider string) *Auth {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[provider]
	if !ok {
		return nil
	}
	if time.Since(e.addedAt) > c.ttl {
		delete(c.entries, provider)
		return nil
	}
	return e.auth
}

// MarkSuccess stores the successful auth for provider into the cache.
func (c *CredentialCache) MarkSuccess(provider string, auth *Auth) {
	if c == nil || provider == "" || auth == nil {
		return
	}
	c.mu.Lock()
	c.entries[provider] = &credentialEntry{auth: auth, addedAt: time.Now()}
	c.mu.Unlock()
}

// Evict removes the cached entry for provider so the next request picks
// a fresh credential. Called when a cached credential fails.
func (c *CredentialCache) Evict(provider string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	delete(c.entries, provider)
	c.mu.Unlock()
}

// Clear removes all cached entries.
func (c *CredentialCache) Clear() {
	if c == nil {
		return
	}
	c.mu.Lock()
	c.entries = make(map[string]*credentialEntry)
	c.mu.Unlock()
}
