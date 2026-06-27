package helps

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	homekv "github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/home"
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

type claudeIDKVClient interface {
	KVGet(ctx context.Context, key string) ([]byte, bool, error)
	KVSetNX(ctx context.Context, key string, value []byte, ttl time.Duration) (bool, error)
	KVExpire(ctx context.Context, key string, ttl time.Duration) (bool, error)
}

var currentClaudeIDKVClient = func() (claudeIDKVClient, bool, error) {
	return homekv.CurrentKVClient()
}

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

func sessionIDCacheKey(apiKey string) string {
	sum := sha256.Sum256([]byte(apiKey))
	return hex.EncodeToString(sum[:])
}

// CachedSessionID returns a stable session UUID per apiKey, refreshing the TTL on each access.
func CachedSessionID(apiKey string) string {
	value, errValue := CachedSessionIDRequired(context.Background(), apiKey)
	if errValue == nil && value != "" {
		return value
	}
	return uuid.New().String()
}

// CachedSessionIDRequired returns a stable session UUID per apiKey for request-time paths.
func CachedSessionIDRequired(ctx context.Context, apiKey string) (string, error) {
	if apiKey == "" {
		return uuid.New().String(), nil
	}
	client, homeMode, errClient := currentClaudeIDKVClient()
	if homeMode {
		if errClient != nil {
			return "", errClient
		}
		key := claudeSessionIDKVKey(apiKey)
		raw, found, errGet := client.KVGet(ctx, key)
		if errGet != nil {
			return "", errGet
		}
		if found && strings.TrimSpace(string(raw)) != "" {
			if _, errExpire := client.KVExpire(ctx, key, sessionIDTTL); errExpire != nil {
				return "", errExpire
			}
			return strings.TrimSpace(string(raw)), nil
		}
		newID := uuid.New().String()
		if _, errSet := client.KVSetNX(ctx, key, []byte(newID), sessionIDTTL); errSet != nil {
			return "", errSet
		}
		raw, found, errGet = client.KVGet(ctx, key)
		if errGet != nil {
			return "", errGet
		}
		if found && strings.TrimSpace(string(raw)) != "" {
			return strings.TrimSpace(string(raw)), nil
		}
		return "", fmt.Errorf("home kv session id missing after set")
	}

	sessionIDCacheCleanupOnce.Do(startSessionIDCacheCleanup)

	key := sessionIDCacheKey(apiKey)
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
			return entry.value, nil
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
	return entry.value, nil
}

func claudeSessionIDKVKey(apiKey string) string {
	return "cpa:claude:session-id:" + homekv.HashKeyPart(apiKey)
}
