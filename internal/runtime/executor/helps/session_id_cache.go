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

func sessionIDScope(apiKey, credential string) string {
	if apiKey != "" {
		return apiKey
	}
	if credential != "" {
		return "anonymous:" + credential
	}
	return "anonymous"
}

func sessionIDCacheKey(apiKey, credential string) string {
	sum := sha256.Sum256([]byte(sessionIDScope(apiKey, credential)))
	return hex.EncodeToString(sum[:])
}

func firstCredential(credential []string) string {
	if len(credential) > 0 {
		return credential[0]
	}
	return ""
}

// CachedSessionID returns a stable session UUID per apiKey, refreshing the TTL on each access.
// An optional credential scopes anonymous (empty apiKey) sessions so different credentials do not share a session.
func CachedSessionID(apiKey string, credential ...string) string {
	cred := firstCredential(credential)
	value, errValue := CachedSessionIDRequired(context.Background(), apiKey, cred)
	if errValue == nil && value != "" {
		return value
	}
	return deterministicSessionID(apiKey, cred)
}

// deterministicSessionID returns a version-5 UUID derived from apiKey so
// different workers and cache misses produce the same session for the same
// credential. A missing apiKey falls back to a namespace UUID scoped by the
// credential; an empty credential produces the legacy anonymous UUID.
func deterministicSessionID(apiKey string, credential ...string) string {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte(sessionIDScope(apiKey, firstCredential(credential)))).String()
}

// CachedSessionIDRequired returns a stable session UUID per apiKey for request-time paths.
// An optional credential scopes anonymous sessions.
func CachedSessionIDRequired(ctx context.Context, apiKey string, credential ...string) (string, error) {
	cred := firstCredential(credential)
	if apiKey == "" {
		return deterministicSessionID(apiKey, cred), nil
	}
	client, homeMode, errClient := currentClaudeIDKVClient()
	if homeMode {
		if errClient != nil {
			return "", errClient
		}
		key := claudeSessionIDKVKey(apiKey, cred)
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
		newID := deterministicSessionID(apiKey, cred)
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

	key := sessionIDCacheKey(apiKey, cred)
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

	newID := deterministicSessionID(apiKey, cred)

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

func claudeSessionIDKVKey(apiKey, credential string) string {
	return "cpa:claude:session-id:" + homekv.HashKeyPart(sessionIDScope(apiKey, credential))
}
