package helps

import (
	"sync"
	"time"
)

type CodexCache struct {
	ID     string
	Expire time.Time
}

// codexCacheStore stores prompt cache IDs keyed by model+user_id.
// Entries are sharded to reduce write contention under concurrent access.
var codexCacheStore = newShardedStringMap[CodexCache]()

// codexCacheCleanupInterval controls how often expired entries are purged.
const codexCacheCleanupInterval = 15 * time.Minute

// codexCacheCleanupOnce ensures the background cleanup goroutine starts only once.
var codexCacheCleanupOnce sync.Once

// startCodexCacheCleanup launches a background goroutine that periodically
// removes expired entries from codexCacheMap to prevent memory leaks.
func startCodexCacheCleanup() {
	go func() {
		ticker := time.NewTicker(codexCacheCleanupInterval)
		defer ticker.Stop()
		for range ticker.C {
			purgeExpiredCodexCache()
		}
	}()
}

// purgeExpiredCodexCache removes expired entries from one shard per run.
func purgeExpiredCodexCache() {
	codexCacheStore.cleanupNextShard(time.Now(), func(cache CodexCache, now time.Time) bool {
		return cache.Expire.Before(now)
	})
}

// GetCodexCache retrieves a cached entry, returning ok=false if not found or expired.
func GetCodexCache(key string) (CodexCache, bool) {
	codexCacheCleanupOnce.Do(startCodexCacheCleanup)
	cache, ok := codexCacheStore.load(key)
	if !ok {
		return CodexCache{}, false
	}
	now := time.Now()
	if cache.Expire.Before(now) {
		deleteExpiredCodexCache(key, now)
		return CodexCache{}, false
	}
	return cache, true
}

// SetCodexCache stores a cache entry.
func SetCodexCache(key string, cache CodexCache) {
	codexCacheCleanupOnce.Do(startCodexCacheCleanup)
	codexCacheStore.store(key, cache)
}

func deleteExpiredCodexCache(key string, now time.Time) {
	shard := codexCacheStore.shardForKey(key)
	shard.mu.Lock()
	entry, ok := shard.entries[key]
	if ok && entry.Expire.Before(now) {
		delete(shard.entries, key)
	}
	shard.mu.Unlock()
}
