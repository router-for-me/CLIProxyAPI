package helps

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"

	"github.com/google/uuid"
)

type sessionIDCacheEntry struct {
	value  string
	expire time.Time
}

var (
	sessionIDCacheStore       = newShardedStringMap[sessionIDCacheEntry]()
	sessionIDCacheCleanupOnce sync.Once
)

const (
	sessionIDTTL                = time.Hour
	sessionIDCacheCleanupPeriod = 15 * time.Minute
)

func startSessionIDCacheCleanup() {
	go func() {
		ticker := time.NewTicker(sessionIDCacheCleanupPeriod)
		defer ticker.Stop()
		for range ticker.C {
			purgeExpiredSessionIDs()
		}
	}()
}

func purgeExpiredSessionIDs() {
	sessionIDCacheStore.cleanupNextShard(time.Now(), func(entry sessionIDCacheEntry, now time.Time) bool {
		return !entry.expire.After(now)
	})
}

func sessionIDCacheKey(apiKey string) string {
	sum := sha256.Sum256([]byte(apiKey))
	return hex.EncodeToString(sum[:])
}

// CachedSessionID returns a stable session UUID per apiKey, refreshing the TTL on each access.
func CachedSessionID(apiKey string) string {
	if apiKey == "" {
		return uuid.New().String()
	}

	sessionIDCacheCleanupOnce.Do(startSessionIDCacheCleanup)

	key := sessionIDCacheKey(apiKey)
	now := time.Now()
	shard := sessionIDCacheStore.shardForKey(key)

	shard.mu.RLock()
	entry, ok := shard.entries[key]
	valid := ok && entry.value != "" && entry.expire.After(now)
	shard.mu.RUnlock()
	if valid {
		shard.mu.Lock()
		entry = shard.entries[key]
		if entry.value != "" && entry.expire.After(now) {
			entry.expire = now.Add(sessionIDTTL)
			shard.entries[key] = entry
			shard.mu.Unlock()
			return entry.value
		}
		shard.mu.Unlock()
	}

	newID := uuid.New().String()

	shard.mu.Lock()
	entry, ok = shard.entries[key]
	if !ok || entry.value == "" || !entry.expire.After(now) {
		entry.value = newID
	}
	entry.expire = now.Add(sessionIDTTL)
	shard.entries[key] = entry
	shard.mu.Unlock()
	return entry.value
}
