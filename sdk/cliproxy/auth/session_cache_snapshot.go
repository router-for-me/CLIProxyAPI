package auth

import (
	"container/list"
	"time"
)

// SessionCacheSnapshotEntry is a durable alias group for one session binding.
type SessionCacheSnapshotEntry struct {
	AuthID    string    `json:"auth_id"`
	Aliases   []string  `json:"aliases"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Snapshot returns non-expired alias groups in eviction order (least recently
// used first), without exposing internal maps. Restore rebuilds eviction order
// in input order, so LRU recency survives a restart.
func (c *SessionCache) Snapshot() []SessionCacheSnapshotEntry {
	if c == nil {
		return nil
	}
	now := time.Now()
	c.mu.RLock()
	defer c.mu.RUnlock()
	entries := make([]SessionCacheSnapshotEntry, 0, len(c.groups))
	if c.evictionOrder == nil {
		return entries
	}
	for element := c.evictionOrder.Front(); element != nil; element = element.Next() {
		key, _ := element.Value.(string)
		entry, ok := c.groups[key]
		if !ok || entry.authID == "" || len(entry.aliases) == 0 || !now.Before(entry.expiresAt) {
			continue
		}
		entries = append(entries, SessionCacheSnapshotEntry{
			AuthID:    entry.authID,
			Aliases:   append([]string(nil), entry.aliases...),
			ExpiresAt: entry.expiresAt,
		})
	}
	return entries
}

// Restore replaces the cache with non-expired snapshot entries, capping each
// expiry at the cache's current TTL.
func (c *SessionCache) Restore(entries []SessionCacheSnapshotEntry) int {
	if c == nil {
		return 0
	}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.ensureInitializedLocked()
	c.entries = make(map[string]sessionEntry)
	c.groups = make(map[string]sessionEntry)
	c.evictionOrder.Init()
	c.evictionElements = make(map[string]*list.Element)
	restored := 0
	for _, item := range entries {
		aliases := compactSessionAliases(item.Aliases)
		if item.AuthID == "" || len(aliases) == 0 || !now.Before(item.ExpiresAt) {
			continue
		}
		// Never restore an expiry beyond the current TTL (it may have been lowered
		// since the snapshot was written).
		expiresAt := item.ExpiresAt
		if limit := now.Add(c.ttl); c.ttl > 0 && expiresAt.After(limit) {
			expiresAt = limit
		}
		c.replaceAliasGroupsLocked(item.AuthID, expiresAt, aliases)
		restored++
	}
	return restored
}
