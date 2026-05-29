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
	sessionIDCache            = make(map[string]sessionIDCacheEntry)
	sessionIDCacheMu          sync.RWMutex
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
	now := time.Now()
	sessionIDCacheMu.Lock()
	for key, entry := range sessionIDCache {
		if !entry.expire.After(now) {
			delete(sessionIDCache, key)
		}
	}
	sessionIDCacheMu.Unlock()
}

// sessionIDCacheKey derives a cache key from the downstream apiKey and the
// per-account proxy URL. Mixing in the proxy URL ensures that an account
// rebound to a different egress IP gets a fresh session ID, so Anthropic does
// not see the same Claude Code session ID coming from two different IPs (which
// can break session affinity or trigger anti-abuse heuristics).
func sessionIDCacheKey(apiKey, proxyURL string) string {
	h := sha256.New()
	h.Write([]byte(apiKey))
	h.Write([]byte{0}) // domain separator so apiKey + proxyURL cannot collide with another apiKey
	h.Write([]byte(proxyURL))
	return hex.EncodeToString(h.Sum(nil))
}

// CachedSessionID returns a stable session UUID per (apiKey, proxyURL),
// refreshing the TTL on each access. Pass the empty string for proxyURL when
// no per-account proxy is configured.
func CachedSessionID(apiKey, proxyURL string) string {
	if apiKey == "" {
		return uuid.New().String()
	}

	sessionIDCacheCleanupOnce.Do(startSessionIDCacheCleanup)

	key := sessionIDCacheKey(apiKey, proxyURL)
	now := time.Now()

	sessionIDCacheMu.RLock()
	entry, ok := sessionIDCache[key]
	valid := ok && entry.value != "" && entry.expire.After(now)
	sessionIDCacheMu.RUnlock()
	if valid {
		sessionIDCacheMu.Lock()
		entry = sessionIDCache[key]
		if entry.value != "" && entry.expire.After(now) {
			entry.expire = now.Add(sessionIDTTL)
			sessionIDCache[key] = entry
			sessionIDCacheMu.Unlock()
			return entry.value
		}
		sessionIDCacheMu.Unlock()
	}

	newID := uuid.New().String()

	sessionIDCacheMu.Lock()
	entry, ok = sessionIDCache[key]
	if !ok || entry.value == "" || !entry.expire.After(now) {
		entry.value = newID
	}
	entry.expire = now.Add(sessionIDTTL)
	sessionIDCache[key] = entry
	sessionIDCacheMu.Unlock()
	return entry.value
}
