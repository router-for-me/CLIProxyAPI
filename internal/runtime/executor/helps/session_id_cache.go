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
	homekv "github.com/router-for-me/CLIProxyAPI/v7/internal/home"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
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

func claudeCredentialSeed(apiKey string, auth *cliproxyauth.Auth) string {
	if seed := strings.TrimSpace(apiKey); seed != "" {
		return seed
	}
	if auth != nil {
		if id := strings.TrimSpace(auth.ID); id != "" {
			return "auth-id|" + id
		}
		if index := strings.TrimSpace(auth.Index); index != "" {
			return "auth-index|" + index
		}
		if fileName := strings.TrimSpace(auth.FileName); fileName != "" {
			return "auth-file|" + fileName
		}
		if label := strings.TrimSpace(auth.Label); label != "" {
			return "auth-label|" + label
		}
		if provider := strings.TrimSpace(auth.Provider); provider != "" {
			return "auth-provider|" + provider
		}
	}
	return "anonymous"
}

func sessionIDCacheKey(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:])
}

// CachedSessionID returns a stable session UUID per credential, refreshing the TTL on each access.
func CachedSessionID(apiKey string, auth *cliproxyauth.Auth) string {
	value, errValue := CachedSessionIDRequired(context.Background(), apiKey, auth)
	if errValue == nil && value != "" {
		return value
	}
	return deterministicSessionID(claudeCredentialSeed(apiKey, auth))
}

// deterministicSessionID returns a version-5 UUID derived from the credential
// seed so different workers and cache misses produce the same session for the
// same credential. No stable identity falls back to a stable anonymous UUID.
func deterministicSessionID(seed string) string {
	if seed == "" || seed == "anonymous" {
		return uuid.NewSHA1(uuid.NameSpaceURL, []byte("cpa:claude:session:anonymous")).String()
	}
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(seed)).String()
}

// CachedSessionIDRequired returns a stable session UUID per credential for request-time paths.
func CachedSessionIDRequired(ctx context.Context, apiKey string, auth *cliproxyauth.Auth) (string, error) {
	seed := claudeCredentialSeed(apiKey, auth)
	if seed == "anonymous" {
		return deterministicSessionID(seed), nil
	}
	client, homeMode, errClient := currentClaudeIDKVClient()
	if homeMode {
		if errClient != nil {
			return "", errClient
		}
		key := claudeSessionIDKVKey(seed)
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
		newID := deterministicSessionID(seed)
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

	key := sessionIDCacheKey(seed)
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

	newID := deterministicSessionID(seed)

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

func claudeSessionIDKVKey(seed string) string {
	return "cpa:claude:session-id:" + homekv.HashKeyPart(seed)
}
