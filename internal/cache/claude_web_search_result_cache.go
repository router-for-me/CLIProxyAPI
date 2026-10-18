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

// Claude web_search_tool_result content is kept per session so it can be restored
// when a Responses client sends a web_search_call back without its results.
// Anthropic validates the encrypted_content of every replayed search result, so
// the original response is the only usable source for that content.
//
// Keys are hashed to a fixed length, and every entry is charged its key plus a
// fixed overhead, so client-supplied session identifiers cannot grow the store
// past its byte budget.
const (
	claudeWebSearchResultTTL           = 24 * time.Hour
	claudeWebSearchResultMaxBytes      = 64 << 20
	claudeWebSearchResultMaxEntryBytes = 4 << 20
	claudeWebSearchResultMaxEntries    = 16384
	claudeWebSearchResultEntryOverhead = 256
)

type claudeWebSearchResultEntry struct {
	key       string
	content   []byte
	expiresAt time.Time
}

func (entry *claudeWebSearchResultEntry) cost() int {
	return len(entry.key) + len(entry.content) + claudeWebSearchResultEntryOverhead
}

var (
	claudeWebSearchResultMu    sync.Mutex
	claudeWebSearchResultIndex = make(map[string]*list.Element)
	claudeWebSearchResultOrder = list.New()
	claudeWebSearchResultBytes int
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
// block for a session. Entries larger than the per-entry limit are skipped; the
// least recently used entries are dropped when the byte budget or the entry
// limit is exceeded.
func CacheClaudeWebSearchResult(sessionKey, toolUseID string, content []byte) {
	key, ok := claudeWebSearchResultKey(sessionKey, toolUseID)
	if !ok || len(content) == 0 || len(content) > claudeWebSearchResultMaxEntryBytes {
		return
	}
	claudeWebSearchResultMu.Lock()
	defer claudeWebSearchResultMu.Unlock()
	if element, exists := claudeWebSearchResultIndex[key]; exists {
		removeClaudeWebSearchResultLocked(element)
	}
	entry := &claudeWebSearchResultEntry{
		key:       key,
		content:   bytes.Clone(content),
		expiresAt: claudeWebSearchResultNow().Add(claudeWebSearchResultTTL),
	}
	claudeWebSearchResultIndex[key] = claudeWebSearchResultOrder.PushFront(entry)
	claudeWebSearchResultBytes += entry.cost()
	for claudeWebSearchResultBytes > claudeWebSearchResultMaxBytes || claudeWebSearchResultOrder.Len() > claudeWebSearchResultMaxEntries {
		oldest := claudeWebSearchResultOrder.Back()
		if oldest == nil {
			break
		}
		removeClaudeWebSearchResultLocked(oldest)
	}
}

// GetClaudeWebSearchResult returns a copy of the stored content and extends its
// lifetime. Expired entries are removed and reported as missing.
func GetClaudeWebSearchResult(sessionKey, toolUseID string) ([]byte, bool) {
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
	entry.expiresAt = now.Add(claudeWebSearchResultTTL)
	claudeWebSearchResultOrder.MoveToFront(element)
	return bytes.Clone(entry.content), true
}

// ClearClaudeWebSearchResultCache removes every stored entry.
func ClearClaudeWebSearchResultCache() {
	claudeWebSearchResultMu.Lock()
	defer claudeWebSearchResultMu.Unlock()
	claudeWebSearchResultIndex = make(map[string]*list.Element)
	claudeWebSearchResultOrder.Init()
	claudeWebSearchResultBytes = 0
}

func removeClaudeWebSearchResultLocked(element *list.Element) {
	entry := element.Value.(*claudeWebSearchResultEntry)
	delete(claudeWebSearchResultIndex, entry.key)
	claudeWebSearchResultOrder.Remove(element)
	claudeWebSearchResultBytes -= entry.cost()
}
