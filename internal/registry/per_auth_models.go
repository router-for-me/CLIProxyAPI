package registry

import (
	"context"
	"sync"
	"time"
)

// PerAuthModelFetcher fetches the model list available to a single authenticated
// account from the upstream provider. It receives the auth's bearer-token-aware
// HTTP client and returns the parsed model list. Implementations are expected
// to refresh the access token BEFORE calling out to the provider's /models
// endpoint when expiry is within the provider's refresh skew.
type PerAuthModelFetcher func(ctx context.Context) ([]*ModelInfo, error)

// PerAuthModelCache caches per-account model lists keyed by auth.ID with a
// fixed TTL. Lazy-on-first-request — entries are NOT prefetched at config
// load time. Fetch failures fall back to the static catalog without caching
// the failure (so the next call retries).
type PerAuthModelCache struct {
	mu       sync.RWMutex
	entries  map[string]*perAuthCacheEntry
	ttl      time.Duration
	fallback func() []*ModelInfo
	now      func() time.Time
}

type perAuthCacheEntry struct {
	models  []*ModelInfo
	expires time.Time
}

// PerAuthModelCacheOption configures a PerAuthModelCache.
type PerAuthModelCacheOption func(*PerAuthModelCache)

// WithClock injects a custom clock into the cache. Intended for testing only.
func WithClock(fn func() time.Time) PerAuthModelCacheOption {
	return func(c *PerAuthModelCache) {
		c.now = fn
	}
}

// NewPerAuthModelCache constructs a cache with the supplied TTL and a fallback
// that returns the static list when a per-account fetch fails or when
// --local-model gates the remote fetch.
func NewPerAuthModelCache(ttl time.Duration, fallback func() []*ModelInfo, opts ...PerAuthModelCacheOption) *PerAuthModelCache {
	if ttl <= 0 {
		ttl = time.Hour
	}
	c := &PerAuthModelCache{
		entries:  make(map[string]*perAuthCacheEntry),
		ttl:      ttl,
		fallback: fallback,
		now:      time.Now,
	}
	for _, opt := range opts {
		opt(c)
	}
	return c
}

// GetOrFetch returns the cached model list for authID if still within TTL.
// Otherwise it invokes fetcher and caches the result. When fetcher returns
// an error, GetOrFetch returns the static fallback WITHOUT caching the
// failure — the next call retries.
//
// When localModelOnly is true, the fetcher is skipped entirely and the
// fallback is returned without caching (so a config toggle takes effect
// immediately).
func (c *PerAuthModelCache) GetOrFetch(ctx context.Context, authID string, localModelOnly bool, fetcher PerAuthModelFetcher) ([]*ModelInfo, error) {
	if localModelOnly || authID == "" {
		return c.staticFallback(), nil
	}

	now := c.now()

	c.mu.RLock()
	entry, ok := c.entries[authID]
	if ok && now.Before(entry.expires) {
		out := append([]*ModelInfo(nil), entry.models...)
		c.mu.RUnlock()
		return out, nil
	}
	c.mu.RUnlock()

	if fetcher == nil {
		return c.staticFallback(), nil
	}
	models, err := fetcher(ctx)
	if err != nil {
		return c.staticFallback(), err
	}
	if len(models) == 0 {
		models = c.staticFallback()
	}

	c.mu.Lock()
	c.entries[authID] = &perAuthCacheEntry{
		models:  append([]*ModelInfo(nil), models...),
		expires: c.now().Add(c.ttl),
	}
	c.mu.Unlock()

	return models, nil
}

// Invalidate drops the cached entry for authID. Subsequent GetOrFetch calls
// will run the fetcher again. Safe to call when the entry does not exist.
func (c *PerAuthModelCache) Invalidate(authID string) {
	if authID == "" {
		return
	}
	c.mu.Lock()
	delete(c.entries, authID)
	c.mu.Unlock()
}

func (c *PerAuthModelCache) staticFallback() []*ModelInfo {
	if c.fallback == nil {
		return nil
	}
	return c.fallback()
}

// Global per-auth model cache instance.
var (
	globalPerAuthModelCache     *PerAuthModelCache
	globalPerAuthModelCacheOnce sync.Once
)

// GlobalPerAuthModelCache returns the process-wide PerAuthModelCache singleton.
// The cache is initialised with a 1-hour TTL and GetGrokModels() as its static
// fallback. Callers that need a different TTL or fallback should construct their
// own instance via NewPerAuthModelCache.
func GlobalPerAuthModelCache() *PerAuthModelCache {
	globalPerAuthModelCacheOnce.Do(func() {
		globalPerAuthModelCache = NewPerAuthModelCache(time.Hour, GetGrokModels)
	})
	return globalPerAuthModelCache
}

// UnregisterAuth removes an auth's entries from both the global model registry
// and the per-auth model cache. Call this from every site that previously called
// GlobalModelRegistry().UnregisterClient(authID) directly so that stale
// per-account model lists are evicted at the same time.
func UnregisterAuth(authID string) {
	GetGlobalRegistry().UnregisterClient(authID)
	GlobalPerAuthModelCache().Invalidate(authID)
}
