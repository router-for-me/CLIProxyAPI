package helps

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"

	"github.com/google/uuid"
)

type deviceIDCacheEntry struct {
	value  string
	expire time.Time
}

var (
	deviceIDCache            = make(map[string]deviceIDCacheEntry)
	deviceIDCacheMu          sync.RWMutex
	deviceIDCacheCleanupOnce sync.Once
)

const (
	deviceIDTTL                = time.Hour
	deviceIDCacheCleanupPeriod = 15 * time.Minute
)

func startDeviceIDCacheCleanup() {
	go func() {
		ticker := time.NewTicker(deviceIDCacheCleanupPeriod)
		defer ticker.Stop()
		for range ticker.C {
			purgeExpiredDeviceIDs()
		}
	}()
}

func purgeExpiredDeviceIDs() {
	now := time.Now()
	deviceIDCacheMu.Lock()
	for key, entry := range deviceIDCache {
		if !entry.expire.After(now) {
			delete(deviceIDCache, key)
		}
	}
	deviceIDCacheMu.Unlock()
}

func deviceIDCacheKey(apiKey string) string {
	sum := sha256.Sum256([]byte(apiKey))
	return hex.EncodeToString(sum[:])
}

// CachedDeviceID returns a stable device UUID per apiKey, refreshing the TTL on each access.
func CachedDeviceID(apiKey string) string {
	if apiKey == "" {
		return uuid.New().String()
	}

	deviceIDCacheCleanupOnce.Do(startDeviceIDCacheCleanup)

	key := deviceIDCacheKey(apiKey)
	now := time.Now()

	deviceIDCacheMu.Lock()
	defer deviceIDCacheMu.Unlock()

	if entry, ok := deviceIDCache[key]; ok && entry.expire.After(now) {
		entry.expire = now.Add(deviceIDTTL)
		deviceIDCache[key] = entry
		return entry.value
	}

	newID := uuid.New().String()
	deviceIDCache[key] = deviceIDCacheEntry{
		value:  newID,
		expire: now.Add(deviceIDTTL),
	}
	return newID
}
