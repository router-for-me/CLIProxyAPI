package cache

import (
	"bytes"
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"
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

func claudeWebSearchResultKey(sessionKey, toolUseID string) (string, bool) {
	sessionKey = strings.TrimSpace(sessionKey)
	toolUseID = strings.TrimSpace(toolUseID)
	if sessionKey == "" || toolUseID == "" {
		return "", false
	}
	sum := sha256.Sum256([]byte(sessionKey + "\x00" + toolUseID))
	return hex.EncodeToString(sum[:]), true
}

// CacheClaudeWebSearchResult stores the raw content of one web_search_tool_result
// block for a session. Memory is bounded by the maximum entry count (limit);
// least recently used entries are evicted when the entry limit is exceeded.
func CacheClaudeWebSearchResult(sessionKey, toolUseID string, content []byte, ttl time.Duration, limit int) {
	key, ok := claudeWebSearchResultKey(sessionKey, toolUseID)
	if !ok || len(content) == 0 {
		return
	}
	if ttl <= 0 {
		ttl = defaultClaudeWebSearchResultTTL
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
}

// GetClaudeWebSearchResult returns a copy of the stored content and extends its
// lifetime. Expired entries are removed and reported as missing.
func GetClaudeWebSearchResult(sessionKey, toolUseID string, ttl ...time.Duration) ([]byte, bool) {
	key, ok := claudeWebSearchResultKey(sessionKey, toolUseID)
	if !ok {
		return nil, false
	}
	claudeWebSearchResultMu.Lock()
	defer claudeWebSearchResultMu.Unlock()

	element, exists := claudeWebSearchResultIndex[key]
	if !exists {
		return nil, false
	}
	entry := element.Value.(*claudeWebSearchResultEntry)
	now := claudeWebSearchResultNow()
	if !now.Before(entry.expiresAt) {
		removeClaudeWebSearchResultLocked(element)
		return nil, false
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
	return bytes.Clone(entry.content), true
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
