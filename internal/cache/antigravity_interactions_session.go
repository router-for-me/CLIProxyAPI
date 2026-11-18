package cache

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// InteractionsContinuation contains only upstream continuation identifiers.
type InteractionsContinuation struct {
	ID          string
	Environment string
}

type interactionsContinuationEntry struct {
	state   InteractionsContinuation
	expires time.Time
}

// InteractionsSessionCache is a bounded, process-local cache. Keys must already
// isolate the caller, credential, model, conversation, and pending call IDs.
type InteractionsSessionCache struct {
	mu      sync.Mutex
	entries map[string]interactionsContinuationEntry
	now     func() time.Time
}

func NewInteractionsSessionCache() *InteractionsSessionCache {
	return &InteractionsSessionCache{entries: make(map[string]interactionsContinuationEntry), now: time.Now}
}

// InteractionsCallKey rejects ambiguous or incomplete call sets.
func InteractionsCallKey(ids []string) string {
	ids = append([]string(nil), ids...)
	sort.Strings(ids)
	for i, id := range ids {
		if id == "" || strings.ContainsRune(id, '\x00') || (i > 0 && ids[i-1] == id) {
			return ""
		}
	}
	return strings.Join(ids, "\x00")
}

func (c *InteractionsSessionCache) Get(key string) (InteractionsContinuation, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[key]
	if !ok || !c.now().Before(entry.expires) {
		delete(c.entries, key)
		return InteractionsContinuation{}, false
	}
	return entry.state, true
}

func (c *InteractionsSessionCache) Put(key string, state InteractionsContinuation) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now()
	for k, entry := range c.entries {
		if !now.Before(entry.expires) {
			delete(c.entries, k)
		}
	}
	if _, exists := c.entries[key]; !exists && len(c.entries) >= 1024 {
		var oldestKey string
		var oldest time.Time
		for k, entry := range c.entries {
			if oldest.IsZero() || entry.expires.Before(oldest) {
				oldestKey, oldest = k, entry.expires
			}
		}
		delete(c.entries, oldestKey)
	}
	c.entries[key] = interactionsContinuationEntry{state: state, expires: now.Add(30 * time.Minute)}
}
