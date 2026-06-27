package helps

import (
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	homekv "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/home"
)

type userIDCacheEntry struct {
	value  string
	expire time.Time
}

var (
	userIDCache            = make(map[string]userIDCacheEntry)
	userIDCacheMu          sync.RWMutex
	userIDCacheCleanupOnce sync.Once
)

const (
	userIDTTL                = time.Hour
	userIDCacheCleanupPeriod = 15 * time.Minute
	userIDCacheHashKey       = "executor-user-id-cache:v1"
)

func startUserIDCacheCleanup() {
	go func() {
		ticker := time.NewTicker(userIDCacheCleanupPeriod)
		defer ticker.Stop()
		for range ticker.C {
			purgeExpiredUserIDs()
		}
	}()
}

func purgeExpiredUserIDs() {
	now := time.Now()
	userIDCacheMu.Lock()
	for key, entry := range userIDCache {
		if !entry.expire.After(now) {
			delete(userIDCache, key)
		}
	}
	userIDCacheMu.Unlock()
}

func userIDCacheKey(apiKey string) string {
	hasher := hmac.New(sha512.New, []byte(userIDCacheHashKey))
	hasher.Write([]byte(apiKey))
	return hex.EncodeToString(hasher.Sum(nil))
}

func CachedUserID(apiKey string) string {
	value, errValue := CachedUserIDRequired(context.Background(), apiKey)
	if errValue == nil && value != "" {
		return value
	}
	return generateFakeUserID()
}

// CachedUserIDRequired returns a stable fake user ID per apiKey for request-time paths.
func CachedUserIDRequired(ctx context.Context, apiKey string) (string, error) {
	if apiKey == "" {
		return generateFakeUserID(), nil
	}
	client, homeMode, errClient := currentClaudeIDKVClient()
	if homeMode {
		if errClient != nil {
			return "", errClient
		}
		key := claudeUserIDKVKey(apiKey)
		raw, found, errGet := client.KVGet(ctx, key)
		if errGet != nil {
			return "", errGet
		}
		if found && isValidUserID(strings.TrimSpace(string(raw))) {
			if _, errExpire := client.KVExpire(ctx, key, userIDTTL); errExpire != nil {
				return "", errExpire
			}
			return strings.TrimSpace(string(raw)), nil
		}
		newID := generateFakeUserID()
		if _, errSet := client.KVSetNX(ctx, key, []byte(newID), userIDTTL); errSet != nil {
			return "", errSet
		}
		raw, found, errGet = client.KVGet(ctx, key)
		if errGet != nil {
			return "", errGet
		}
		if found && isValidUserID(strings.TrimSpace(string(raw))) {
			return strings.TrimSpace(string(raw)), nil
		}
		return "", fmt.Errorf("home kv user id missing after set")
	}

	userIDCacheCleanupOnce.Do(startUserIDCacheCleanup)

	key := userIDCacheKey(apiKey)
	now := time.Now()

	userIDCacheMu.RLock()
	entry, ok := userIDCache[key]
	valid := ok && entry.value != "" && entry.expire.After(now) && isValidUserID(entry.value)
	userIDCacheMu.RUnlock()
	if valid {
		userIDCacheMu.Lock()
		entry = userIDCache[key]
		if entry.value != "" && entry.expire.After(now) && isValidUserID(entry.value) {
			entry.expire = now.Add(userIDTTL)
			userIDCache[key] = entry
			userIDCacheMu.Unlock()
			return entry.value, nil
		}
		userIDCacheMu.Unlock()
	}

	newID := generateFakeUserID()

	userIDCacheMu.Lock()
	entry, ok = userIDCache[key]
	if !ok || entry.value == "" || !entry.expire.After(now) || !isValidUserID(entry.value) {
		entry.value = newID
	}
	entry.expire = now.Add(userIDTTL)
	userIDCache[key] = entry
	userIDCacheMu.Unlock()
	return entry.value, nil
}

func claudeUserIDKVKey(apiKey string) string {
	return "cpa:claude:user-id:" + homekv.HashKeyPart(apiKey)
}

func ResetUserIDCacheForTest() {
	userIDCacheMu.Lock()
	userIDCache = make(map[string]userIDCacheEntry)
	userIDCacheMu.Unlock()
}

func SetUserIDCacheEntryForTest(apiKey, value string, expire time.Time) {
	userIDCacheMu.Lock()
	userIDCache[userIDCacheKey(apiKey)] = userIDCacheEntry{value: value, expire: expire}
	userIDCacheMu.Unlock()
}

func UserIDCacheEntryExpireForTest(apiKey string) (time.Time, bool) {
	userIDCacheMu.RLock()
	entry, ok := userIDCache[userIDCacheKey(apiKey)]
	userIDCacheMu.RUnlock()
	return entry.expire, ok
}

func UserIDCacheKeyForTest(apiKey string) string {
	return userIDCacheKey(apiKey)
}
