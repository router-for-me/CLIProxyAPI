package helps

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	homekv "github.com/router-for-me/CLIProxyAPI/v7/internal/home"
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

func userIDCacheKey(apiKey, credential string) string {
	sum := sha256.Sum256([]byte(sessionIDScope(apiKey, credential)))
	return hex.EncodeToString(sum[:])
}

// CachedUserID returns a stable fake user ID per apiKey, refreshing the TTL on each access.
// An optional credential scopes anonymous (empty apiKey) user IDs.
func CachedUserID(apiKey string, credential ...string) string {
	cred := firstCredential(credential)
	value, errValue := CachedUserIDRequired(context.Background(), apiKey, cred)
	if errValue == nil && value != "" {
		return value
	}
	return generateFakeUserID()
}

// CachedUserIDRequired returns a stable fake user ID per apiKey for request-time paths.
// An optional credential scopes anonymous user IDs.
func CachedUserIDRequired(ctx context.Context, apiKey string, credential ...string) (string, error) {
	cred := firstCredential(credential)
	newUserID := func() (string, error) {
		sessionID, errSessionID := CachedSessionIDRequired(ctx, apiKey, cred)
		if errSessionID != nil {
			return "", errSessionID
		}
		return generateDeterministicFakeUserID(apiKey, sessionID), nil
	}

	if apiKey == "" {
		return newUserID()
	}
	client, homeMode, errClient := currentClaudeIDKVClient()
	if homeMode {
		if errClient != nil {
			return "", errClient
		}
		key := claudeUserIDKVKey(apiKey, cred)
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
		newID, errNewID := newUserID()
		if errNewID != nil {
			return "", errNewID
		}
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

	key := userIDCacheKey(apiKey, cred)
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

	newID, errNewID := newUserID()
	if errNewID != nil {
		return "", errNewID
	}

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

func claudeUserIDKVKey(apiKey, credential string) string {
	return "cpa:claude:user-id:" + homekv.HashKeyPart(sessionIDScope(apiKey, credential))
}
