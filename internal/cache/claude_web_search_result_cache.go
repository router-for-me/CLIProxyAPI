package cache

import (
	"bytes"
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	homekv "github.com/router-for-me/CLIProxyAPI/v8/internal/home"
	log "github.com/sirupsen/logrus"
)

const (
	defaultClaudeWebSearchResultTTL   = 24 * time.Hour
	defaultClaudeWebSearchResultLimit = 1024
)

type claudeWebSearchResultEntry struct {
	key       string
	content   []byte
	ttl       time.Duration
	expiresAt time.Time
}

var (
	claudeWebSearchResultMu    sync.Mutex
	claudeWebSearchResultIndex = make(map[string]*list.Element)
	claudeWebSearchResultOrder = list.New()
	claudeWebSearchResultNow   = time.Now
)

type claudeWebSearchResultKVClient interface {
	KVGet(ctx context.Context, key string) ([]byte, bool, error)
	KVSet(ctx context.Context, key string, value []byte, opts homekv.KVSetOptions) (bool, error)
	KVExpire(ctx context.Context, key string, ttl time.Duration) (bool, error)
}

var currentClaudeWebSearchResultKVClient = func() (claudeWebSearchResultKVClient, bool, error) {
	return homekv.CurrentKVClient()
}

func claudeWebSearchResultKey(sessionKey, toolUseID string) (string, bool) {
	sessionKey = strings.TrimSpace(sessionKey)
	toolUseID = strings.TrimSpace(toolUseID)
	if sessionKey == "" || toolUseID == "" {
		return "", false
	}
	sum := sha256.Sum256([]byte(sessionKey + "\x00" + toolUseID))
	return hex.EncodeToString(sum[:]), true
}

func claudeWebSearchResultKVKey(sessionKey, toolUseID string) string {
	key, ok := claudeWebSearchResultKey(sessionKey, toolUseID)
	if !ok {
		return ""
	}
	return "cpa:claude:web-search-result:" + key
}

// CacheClaudeWebSearchResult stores the raw content of one web_search_tool_result
// block for a session. Memory is bounded by the maximum entry count (limit);
// least recently used entries are evicted when the entry limit is exceeded.
func CacheClaudeWebSearchResult(sessionKey, toolUseID string, content []byte, ttl time.Duration, limit int) {
	CacheClaudeWebSearchResultBestEffort(context.Background(), sessionKey, toolUseID, content, ttl, limit)
}

// CacheClaudeWebSearchResultBestEffort stores the raw content of one
// web_search_tool_result block for completed response paths.
func CacheClaudeWebSearchResultBestEffort(ctx context.Context, sessionKey, toolUseID string, content []byte, ttl time.Duration, limit int) bool {
	key, ok := claudeWebSearchResultKey(sessionKey, toolUseID)
	if !ok || len(content) == 0 {
		return false
	}
	if ttl <= 0 {
		ttl = defaultClaudeWebSearchResultTTL
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if client, homeMode, errClient := currentClaudeWebSearchResultKVClient(); homeMode {
		if errClient != nil {
			log.Errorf("home kv best-effort claude web search result set failed prefix=cpa:claude:*: %v", errClient)
			return false
		}
		written, errSet := client.KVSet(ctx, "cpa:claude:web-search-result:"+key, bytes.Clone(content), homekv.KVSetOptions{EX: ttl})
		if errSet != nil {
			log.Errorf("home kv best-effort claude web search result set failed prefix=cpa:claude:*: %v", errSet)
			return false
		}
		return written
	}
	if limit <= 0 {
		limit = defaultClaudeWebSearchResultLimit
	}
	cacheCleanupOnce.Do(startCacheCleanup)

	claudeWebSearchResultMu.Lock()
	defer claudeWebSearchResultMu.Unlock()

	now := claudeWebSearchResultNow()
	if element, exists := claudeWebSearchResultIndex[key]; exists {
		removeClaudeWebSearchResultLocked(element)
	}
	if claudeWebSearchResultOrder.Len() >= limit {
		purgeExpiredClaudeWebSearchResultLocked(now)
	}
	entry := &claudeWebSearchResultEntry{
		key:       key,
		content:   bytes.Clone(content),
		ttl:       ttl,
		expiresAt: now.Add(ttl),
	}
	claudeWebSearchResultIndex[key] = claudeWebSearchResultOrder.PushFront(entry)
	for claudeWebSearchResultOrder.Len() > limit {
		oldest := claudeWebSearchResultOrder.Back()
		if oldest == nil {
			break
		}
		removeClaudeWebSearchResultLocked(oldest)
	}
	return true
}

// GetClaudeWebSearchResult returns a copy of the stored content and extends its
// lifetime. Expired entries are removed and reported as missing.
func GetClaudeWebSearchResult(sessionKey, toolUseID string, ttl ...time.Duration) ([]byte, bool) {
	content, ok, errGet := GetClaudeWebSearchResultRequired(context.Background(), sessionKey, toolUseID, ttl...)
	if errGet != nil {
		return nil, false
	}
	return content, ok
}

// GetClaudeWebSearchResultRequired retrieves a copy of the stored content for
// request-time paths and extends its lifetime.
func GetClaudeWebSearchResultRequired(ctx context.Context, sessionKey, toolUseID string, ttl ...time.Duration) ([]byte, bool, error) {
	key, ok := claudeWebSearchResultKey(sessionKey, toolUseID)
	if !ok {
		return nil, false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if client, homeMode, errClient := currentClaudeWebSearchResultKVClient(); homeMode {
		if errClient != nil {
			return nil, false, errClient
		}
		kvKey := "cpa:claude:web-search-result:" + key
		raw, found, errGet := client.KVGet(ctx, kvKey)
		if errGet != nil || !found || len(raw) == 0 {
			return nil, false, errGet
		}
		effectiveTTL := defaultClaudeWebSearchResultTTL
		if len(ttl) > 0 && ttl[0] > 0 {
			effectiveTTL = ttl[0]
		}
		if _, errExpire := client.KVExpire(ctx, kvKey, effectiveTTL); errExpire != nil {
			log.Warnf("home kv claude web search result expire failed prefix=cpa:claude:*: %v", errExpire)
		}
		return bytes.Clone(raw), true, nil
	}
	claudeWebSearchResultMu.Lock()
	defer claudeWebSearchResultMu.Unlock()

	element, exists := claudeWebSearchResultIndex[key]
	if !exists {
		return nil, false, nil
	}
	entry := element.Value.(*claudeWebSearchResultEntry)
	now := claudeWebSearchResultNow()
	if !now.Before(entry.expiresAt) {
		removeClaudeWebSearchResultLocked(element)
		return nil, false, nil
	}
	effectiveTTL := entry.ttl
	if len(ttl) > 0 && ttl[0] > 0 {
		effectiveTTL = ttl[0]
		entry.ttl = effectiveTTL
	}
	if effectiveTTL <= 0 {
		effectiveTTL = defaultClaudeWebSearchResultTTL
	}
	entry.expiresAt = now.Add(effectiveTTL)
	claudeWebSearchResultOrder.MoveToFront(element)
	return bytes.Clone(entry.content), true, nil
}

// ClearClaudeWebSearchResultCache removes every stored entry.
func ClearClaudeWebSearchResultCache() {
	claudeWebSearchResultMu.Lock()
	defer claudeWebSearchResultMu.Unlock()
	claudeWebSearchResultIndex = make(map[string]*list.Element)
	claudeWebSearchResultOrder.Init()
}

func purgeExpiredClaudeWebSearchResultCache(now time.Time) {
	claudeWebSearchResultMu.Lock()
	defer claudeWebSearchResultMu.Unlock()
	purgeExpiredClaudeWebSearchResultLocked(now)
}

func purgeExpiredClaudeWebSearchResultLocked(now time.Time) {
	for element := claudeWebSearchResultOrder.Back(); element != nil; {
		prev := element.Prev()
		entry := element.Value.(*claudeWebSearchResultEntry)
		if !now.Before(entry.expiresAt) {
			removeClaudeWebSearchResultLocked(element)
		}
		element = prev
	}
}

func removeClaudeWebSearchResultLocked(element *list.Element) {
	entry := element.Value.(*claudeWebSearchResultEntry)
	delete(claudeWebSearchResultIndex, entry.key)
	claudeWebSearchResultOrder.Remove(element)
}
