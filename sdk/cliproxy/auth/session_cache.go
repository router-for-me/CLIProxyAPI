package auth

import (
	"container/list"
	"strings"
	"sync"
	"time"
)

const maxStableSessionAliases = 64

// defaultSessionCacheEntries bounds how many session identifiers one process keeps
// bound at once. Without it a client can retain memory for a whole TTL window by
// sending many distinct, individually valid session IDs.
const defaultSessionCacheEntries = 8192

// sessionEntry stores an auth binding, its identifier aliases, and expiration.
// group is a process-unique identifier for the alias set, so an entry read through
// one alias can be matched against the authoritative group it belongs to.
type sessionEntry struct {
	authID    string
	expiresAt time.Time
	aliases   []string
	group     uint64
}

// SessionCacheStats reports session cache occupancy for diagnostics.
type SessionCacheStats struct {
	// Entries counts bound identifiers, including aliases of the same session.
	Entries int
	// Groups counts distinct logical sessions.
	Groups int
	// Evictions counts groups dropped because the capacity bound was reached.
	Evictions uint64
}

type sessionBindingSnapshot struct {
	authIDs       []string
	groupLockKeys []string
}

// SessionCache provides TTL-based session to auth mapping with automatic cleanup
// and a bounded number of retained sessions.
type SessionCache struct {
	mu         sync.RWMutex
	entries    map[string]sessionEntry
	groups     map[uint64]sessionEntry
	order      *list.List
	elements   map[uint64]*list.Element
	nextGroup  uint64
	maxEntries int
	evictions  uint64
	ttl        time.Duration
	stopCh     chan struct{}
	stopOnce   sync.Once
}

// NewSessionCache creates a cache with the specified TTL and the default capacity bound.
// A background goroutine periodically cleans expired entries.
func NewSessionCache(ttl time.Duration) *SessionCache {
	return NewSessionCacheWithLimit(ttl, defaultSessionCacheEntries)
}

// NewSessionCacheWithLimit creates a cache with the specified TTL and capacity bound.
// A non-positive maxEntries selects the default bound.
func NewSessionCacheWithLimit(ttl time.Duration, maxEntries int) *SessionCache {
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	if maxEntries <= 0 {
		maxEntries = defaultSessionCacheEntries
	}
	c := &SessionCache{
		entries:    make(map[string]sessionEntry),
		groups:     make(map[uint64]sessionEntry),
		order:      list.New(),
		elements:   make(map[uint64]*list.Element),
		maxEntries: maxEntries,
		ttl:        ttl,
		stopCh:     make(chan struct{}),
	}
	go c.cleanupLoop()
	return c
}

// Stats reports current occupancy and the cumulative capacity eviction count.
func (c *SessionCache) Stats() SessionCacheStats {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return SessionCacheStats{
		Entries:   len(c.entries),
		Groups:    len(c.groups),
		Evictions: c.evictions,
	}
}

// bindingSnapshot returns the live auth bindings and stable lock identities for
// the alias groups reached through sessionIDs. It does not refresh their TTL.
func (c *SessionCache) bindingSnapshot(sessionIDs []string) sessionBindingSnapshot {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()

	snapshot := sessionBindingSnapshot{
		authIDs:       make([]string, 0, len(sessionIDs)),
		groupLockKeys: make([]string, 0, len(sessionIDs)),
	}
	seenAuthIDs := make(map[string]struct{}, len(sessionIDs))
	seenGroups := make(map[uint64]struct{}, len(sessionIDs))
	for _, sessionID := range sessionIDs {
		entry, ok := c.entries[sessionID]
		if !ok {
			continue
		}
		if !now.Before(entry.expiresAt) {
			c.removeAliasGroupLocked(entry)
			continue
		}
		if _, seen := seenAuthIDs[entry.authID]; !seen && entry.authID != "" {
			seenAuthIDs[entry.authID] = struct{}{}
			snapshot.authIDs = append(snapshot.authIDs, entry.authID)
		}
		if _, seen := seenGroups[entry.group]; seen {
			continue
		}
		seenGroups[entry.group] = struct{}{}
		groupLockKey := ""
		for _, alias := range entry.aliases {
			if alias != "" && (groupLockKey == "" || alias < groupLockKey) {
				groupLockKey = alias
			}
		}
		if groupLockKey != "" {
			snapshot.groupLockKeys = append(snapshot.groupLockKeys, groupLockKey)
		}
	}
	return snapshot
}

// Get retrieves the auth ID bound to a session, if still valid.
// Does NOT refresh the TTL on access.
func (c *SessionCache) Get(sessionID string) (string, bool) {
	if sessionID == "" {
		return "", false
	}
	now := time.Now()
	c.mu.RLock()
	entry, ok := c.entries[sessionID]
	if ok && now.Before(entry.expiresAt) {
		c.mu.RUnlock()
		return entry.authID, true
	}
	c.mu.RUnlock()
	if !ok {
		return "", false
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok = c.entries[sessionID]
	if !ok {
		return "", false
	}
	if time.Now().Before(entry.expiresAt) {
		return entry.authID, true
	}
	c.removeAliasGroupLocked(entry)
	return "", false
}

// GetAndRefresh retrieves the auth ID bound to a session and refreshes the TTL
// for every identifier known to represent the same logical session.
func (c *SessionCache) GetAndRefresh(sessionID string) (string, bool) {
	if sessionID == "" {
		return "", false
	}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[sessionID]
	if !ok {
		return "", false
	}
	if !now.Before(entry.expiresAt) {
		c.removeAliasGroupLocked(entry)
		return "", false
	}

	aliases := compactSessionAliases(mergeSessionAliases([]string{sessionID}, entry.aliases...))
	c.replaceAliasGroupsLocked(entry.authID, now.Add(c.ttl), aliases, entry)
	return entry.authID, true
}

// Set binds a session to an auth ID with TTL refresh. Existing aliases for the
// same logical session remain attached when the binding is refreshed or moved.
func (c *SessionCache) Set(sessionID, authID string) {
	c.SetAliases(authID, sessionID)
}

// SetAliases binds multiple identifiers for one logical session to an auth ID.
func (c *SessionCache) SetAliases(authID string, sessionIDs ...string) {
	if authID == "" {
		return
	}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()

	aliases := mergeSessionAliases(nil, sessionIDs...)
	previousGroups := make([]sessionEntry, 0, len(sessionIDs))
	for _, sessionID := range sessionIDs {
		entry, ok := c.entries[sessionID]
		if !ok {
			continue
		}
		if !now.Before(entry.expiresAt) {
			c.removeAliasGroupLocked(entry)
			continue
		}
		previousGroups = append(previousGroups, entry)
		aliases = mergeSessionAliases(aliases, entry.aliases...)
	}
	aliases = compactSessionAliases(aliases)
	if len(aliases) == 0 {
		return
	}
	c.replaceAliasGroupsLocked(authID, now.Add(c.ttl), aliases, previousGroups...)
}

func (c *SessionCache) replaceAliasGroupsLocked(authID string, expiresAt time.Time, aliases []string, previousGroups ...sessionEntry) {
	for _, previous := range previousGroups {
		c.removeAliasGroupLocked(previous)
	}
	c.nextGroup++
	entry := sessionEntry{authID: authID, expiresAt: expiresAt, aliases: aliases, group: c.nextGroup}
	for _, alias := range aliases {
		c.entries[alias] = entry
	}
	c.groups[entry.group] = entry
	c.elements[entry.group] = c.order.PushBack(entry.group)
	c.enforceLimitLocked()
}

func (c *SessionCache) removeAliasGroupLocked(entry sessionEntry) {
	current, ok := c.groups[entry.group]
	if !ok || current.group != entry.group {
		return
	}
	for _, alias := range current.aliases {
		if mapped, exists := c.entries[alias]; exists && mapped.group == current.group {
			delete(c.entries, alias)
		}
	}
	delete(c.groups, current.group)
	if element, exists := c.elements[current.group]; exists {
		c.order.Remove(element)
		delete(c.elements, current.group)
	}
}

// enforceLimitLocked drops the least recently bound sessions once the cache exceeds
// its capacity bound. Every hit refreshes a group, so active sessions survive.
func (c *SessionCache) enforceLimitLocked() {
	if c.maxEntries <= 0 {
		return
	}
	for len(c.entries) > c.maxEntries {
		oldest := c.order.Front()
		if oldest == nil {
			return
		}
		group, _ := oldest.Value.(uint64)
		entry, ok := c.groups[group]
		if !ok {
			c.order.Remove(oldest)
			delete(c.elements, group)
			continue
		}
		c.removeAliasGroupLocked(entry)
		c.evictions++
	}
}

func compactSessionAliases(aliases []string) []string {
	return compactSessionAliasesWith(aliases, isLocalPromptCacheSessionAlias)
}

func compactHomeSessionAliases(aliases []string) []string {
	return compactSessionAliasesWith(aliases, func(alias string) bool {
		return strings.HasPrefix(alias, "pck:")
	})
}

func compactSessionAliasesWith(aliases []string, isPromptCacheAlias func(string) bool) []string {
	compacted := make([]string, 0, len(aliases))
	hasPromptCacheKey := false
	stableAliases := 0
	for _, alias := range aliases {
		if isPromptCacheAlias(alias) {
			if hasPromptCacheKey {
				continue
			}
			hasPromptCacheKey = true
		} else {
			if stableAliases >= maxStableSessionAliases {
				continue
			}
			stableAliases++
		}
		compacted = append(compacted, alias)
	}
	return compacted
}

func isLocalPromptCacheSessionAlias(alias string) bool {
	if strings.HasPrefix(alias, "pck:") {
		return true
	}
	return strings.HasPrefix(sessionCacheKeySessionID(alias), "pck:")
}

func equalSessionAliases(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func mergeSessionAliases(existing []string, candidates ...string) []string {
	aliases := make([]string, 0, len(existing)+len(candidates))
	seen := make(map[string]struct{}, cap(aliases))
	add := func(alias string) {
		if alias == "" {
			return
		}
		if _, ok := seen[alias]; ok {
			return
		}
		seen[alias] = struct{}{}
		aliases = append(aliases, alias)
	}
	for _, alias := range existing {
		add(alias)
	}
	for _, alias := range candidates {
		add(alias)
	}
	return aliases
}

// Invalidate removes a specific session binding without allowing another alias
// in the same group to recreate it on its next refresh.
func (c *SessionCache) Invalidate(sessionID string) {
	if sessionID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[sessionID]
	if !ok {
		delete(c.entries, sessionID)
		return
	}
	c.removeAliasGroupLocked(entry)

	remaining := make([]string, 0, len(entry.aliases))
	for _, alias := range entry.aliases {
		if alias != sessionID {
			remaining = append(remaining, alias)
		}
	}
	if len(remaining) == 0 {
		return
	}
	c.replaceAliasGroupsLocked(entry.authID, entry.expiresAt, remaining)
}

// InvalidateAuth removes all sessions bound to a specific auth ID.
// Used when an auth becomes unavailable.
func (c *SessionCache) InvalidateAuth(authID string) {
	if authID == "" {
		return
	}
	c.mu.Lock()
	stale := make([]sessionEntry, 0, len(c.groups))
	for _, entry := range c.groups {
		if entry.authID == authID {
			stale = append(stale, entry)
		}
	}
	for _, entry := range stale {
		c.removeAliasGroupLocked(entry)
	}
	c.mu.Unlock()
}

// Stop terminates the background cleanup goroutine.
func (c *SessionCache) Stop() {
	c.stopOnce.Do(func() {
		close(c.stopCh)
	})
}

func (c *SessionCache) cleanupLoop() {
	ticker := time.NewTicker(c.ttl / 2)
	defer ticker.Stop()
	for {
		select {
		case <-c.stopCh:
			return
		case <-ticker.C:
			c.cleanup()
		}
	}
}

func (c *SessionCache) cleanup() {
	now := time.Now()
	c.mu.Lock()
	expired := make([]sessionEntry, 0, len(c.groups))
	for _, entry := range c.groups {
		if !now.Before(entry.expiresAt) {
			expired = append(expired, entry)
		}
	}
	for _, entry := range expired {
		c.removeAliasGroupLocked(entry)
	}
	c.mu.Unlock()
}
