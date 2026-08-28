package auth

import (
	"strings"
	"sync"
	"time"
)

const maxStableSessionAliases = 64

// sessionEntry stores an auth binding, its identifier aliases, and expiration.
type sessionEntry struct {
	authID     string
	expiresAt  time.Time
	aliases    []string
	generation uint64
}

// SessionCache provides TTL-based session to auth mapping with automatic cleanup.
type SessionCache struct {
	mu         sync.RWMutex
	entries    map[string]sessionEntry
	ttl        time.Duration
	stopCh     chan struct{}
	stopOnce   sync.Once
	generation uint64
}

// NewSessionCache creates a cache with the specified TTL.
// A background goroutine periodically cleans expired entries.
func NewSessionCache(ttl time.Duration) *SessionCache {
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	c := &SessionCache{
		entries: make(map[string]sessionEntry),
		ttl:     ttl,
		stopCh:  make(chan struct{}),
	}
	go c.cleanupLoop()
	return c
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

// Observe returns the current generation token, auth ID, and aliases for a session ID
// without refreshing its TTL or acquiring a write lock.
func (c *SessionCache) Observe(sessionID string) (gen uint64, authID string, aliases []string, ok bool) {
	if c == nil || sessionID == "" {
		return 0, "", nil, false
	}
	now := time.Now()
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, exists := c.entries[sessionID]
	if !exists || !now.Before(entry.expiresAt) {
		return 0, "", nil, false
	}
	return entry.generation, entry.authID, append([]string(nil), entry.aliases...), true
}

// Set binds a session to an auth ID with TTL refresh. Existing aliases for the
// same logical session remain attached when the binding is refreshed or moved.
func (c *SessionCache) Set(sessionID, authID string) {
	c.SetAliases(authID, sessionID)
}

// SetAliases binds multiple identifiers for one logical session to an auth ID.
func (c *SessionCache) SetAliases(authID string, sessionIDs ...string) {
	c.setAliasesUntil(authID, time.Now().Add(c.ttl), sessionIDs...)
}

// RestoreAliasesIfAbsent atomically sets the still-absent aliases to authID.
// Any alias that is currently live (bound to another active group) is left untouched.
// Returns true if at least one alias was restored, false otherwise.
func (c *SessionCache) RestoreAliasesIfAbsent(authID string, sessionIDs ...string) bool {
	if c == nil || authID == "" || len(sessionIDs) == 0 {
		return false
	}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()

	var absent []string
	for _, sid := range sessionIDs {
		if sid == "" {
			continue
		}
		if entry, ok := c.entries[sid]; !ok || !now.Before(entry.expiresAt) {
			absent = append(absent, sid)
		}
	}
	aliases := compactSessionAliases(absent)
	if len(aliases) == 0 {
		return false
	}
	c.generation++
	entry := sessionEntry{
		authID:     authID,
		expiresAt:  now.Add(c.ttl),
		aliases:    aliases,
		generation: c.generation,
	}
	for _, alias := range aliases {
		c.entries[alias] = entry
	}
	return true
}

func (c *SessionCache) setAliasesUntil(authID string, expiresAt time.Time, sessionIDs ...string) {
	if authID == "" || expiresAt.IsZero() {
		return
	}
	now := time.Now()
	if !now.Before(expiresAt) {
		return
	}
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
	c.replaceAliasGroupsLocked(authID, expiresAt, aliases, previousGroups...)
}

func (c *SessionCache) replaceAliasGroupsLocked(authID string, expiresAt time.Time, aliases []string, previousGroups ...sessionEntry) {
	for _, previous := range previousGroups {
		c.removeAliasGroupLocked(previous)
	}
	c.generation++
	entry := sessionEntry{authID: authID, expiresAt: expiresAt, aliases: aliases, generation: c.generation}
	for _, alias := range aliases {
		c.entries[alias] = entry
	}
}

func (c *SessionCache) removeAliasGroupLocked(entry sessionEntry) {
	c.generation++
	for _, alias := range entry.aliases {
		current, ok := c.entries[alias]
		if !ok || current.authID != entry.authID || !current.expiresAt.Equal(entry.expiresAt) ||
			!equalSessionAliases(current.aliases, entry.aliases) {
			continue
		}
		delete(c.entries, alias)
	}
}

// CompareAndReplaceGroup atomically validates that an observed group has not mutated
// (matching expectedGen, expectedAuthID, and expectedAliases), confirms no requested
// new alias belongs to another active live group, and replaces the whole group with newAuthID.
func (c *SessionCache) CompareAndReplaceGroup(expectedGen uint64, expectedAuthID string, expectedAliases []string, newAuthID string, newSessionIDs ...string) bool {
	if c == nil || newAuthID == "" {
		return false
	}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()

	if expectedGen != 0 {
		if len(expectedAliases) == 0 {
			return false
		}
		for _, alias := range expectedAliases {
			current, ok := c.entries[alias]
			if !ok || !now.Before(current.expiresAt) {
				return false
			}
			if current.generation != expectedGen || current.authID != expectedAuthID || !equalSessionAliases(current.aliases, expectedAliases) {
				return false
			}
		}
	} else {
		for _, sid := range newSessionIDs {
			if sid == "" {
				continue
			}
			if current, ok := c.entries[sid]; ok && now.Before(current.expiresAt) {
				return false
			}
		}
	}

	candidateAliases := mergeSessionAliases(expectedAliases, newSessionIDs...)
	for _, alias := range candidateAliases {
		current, ok := c.entries[alias]
		if !ok || !now.Before(current.expiresAt) {
			continue
		}
		if expectedGen == 0 || current.generation != expectedGen || current.authID != expectedAuthID {
			return false
		}
	}

	newAliases := compactSessionAliases(candidateAliases)
	if len(newAliases) == 0 {
		return false
	}

	if expectedGen != 0 {
		for _, alias := range expectedAliases {
			delete(c.entries, alias)
		}
	}

	c.generation++
	entry := sessionEntry{
		authID:     newAuthID,
		expiresAt:  now.Add(c.ttl),
		aliases:    newAliases,
		generation: c.generation,
	}
	for _, alias := range newAliases {
		c.entries[alias] = entry
	}
	return true
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
	_, sessionAndModel, ok := strings.Cut(alias, "::")
	return ok && strings.HasPrefix(sessionAndModel, "pck:")
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

// Touch refreshes the expiration for a session binding if it currently matches expectedAuthID.
func (c *SessionCache) Touch(sessionID, expectedAuthID string) bool {
	if sessionID == "" || expectedAuthID == "" {
		return false
	}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[sessionID]
	if !ok || entry.authID != expectedAuthID || !now.Before(entry.expiresAt) {
		return false
	}
	aliases := compactSessionAliases(mergeSessionAliases([]string{sessionID}, entry.aliases...))
	c.replaceAliasGroupsLocked(expectedAuthID, now.Add(c.ttl), aliases, entry)
	return true
}

// CompareAndDelete removes the session binding only if it is currently bound to expectedAuthID.
func (c *SessionCache) CompareAndDelete(sessionID, expectedAuthID string) bool {
	if sessionID == "" || expectedAuthID == "" {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[sessionID]
	if !ok || entry.authID != expectedAuthID {
		return false
	}
	delete(c.entries, sessionID)
	for _, alias := range entry.aliases {
		if alias == sessionID {
			continue
		}
		current, exists := c.entries[alias]
		if !exists || current.authID != entry.authID {
			continue
		}
		filtered := make([]string, 0, len(current.aliases))
		for _, candidate := range current.aliases {
			if candidate != sessionID {
				filtered = append(filtered, candidate)
			}
		}
		current.aliases = filtered
		c.entries[alias] = current
	}
	return true
}

// Invalidate removes a specific session binding without allowing another alias
// in the same group to recreate it on its next refresh.
func (c *SessionCache) Invalidate(sessionID string) {
	if sessionID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.generation++
	entry, ok := c.entries[sessionID]
	delete(c.entries, sessionID)
	if ok {
		for _, alias := range entry.aliases {
			if alias == sessionID {
				continue
			}
			current, exists := c.entries[alias]
			if !exists || current.authID != entry.authID {
				continue
			}
			filtered := make([]string, 0, len(current.aliases))
			for _, candidate := range current.aliases {
				if candidate != sessionID {
					filtered = append(filtered, candidate)
				}
			}
			current.aliases = filtered
			current.generation = c.generation
			c.entries[alias] = current
		}
	}
}

// CompareAndDeleteAliases removes a binding and returns every alias that still
// belongs to the same expected auth. A stale result cannot remove a newer group.
func (c *SessionCache) CompareAndDeleteAliases(sessionID, expectedAuthID string) []string {
	if c == nil || sessionID == "" || expectedAuthID == "" {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.entries[sessionID]
	if !ok || entry.authID != expectedAuthID {
		return nil
	}
	aliases := append([]string(nil), entry.aliases...)
	c.removeAliasGroupLocked(entry)
	return aliases
}

// CompareAndDeleteGroup removes a binding only when its auth ID, generation,
// and alias set all still match the observed values, and returns the removed
// aliases. A concurrent refresh or extension of the group bumps the
// generation or changes the aliases, so a stale observation cannot delete
// newer state; callers retry their merge on a nil result.
func (c *SessionCache) CompareAndDeleteGroup(sessionID, expectedAuthID string, expectedGen uint64, expectedAliases []string) []string {
	if c == nil || sessionID == "" || expectedAuthID == "" {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.entries[sessionID]
	if !ok || entry.authID != expectedAuthID || entry.generation != expectedGen {
		return nil
	}
	if !equalSessionAliases(compactSessionAliases(entry.aliases), compactSessionAliases(expectedAliases)) {
		return nil
	}
	removed := append([]string(nil), entry.aliases...)
	c.removeAliasGroupLocked(entry)
	return removed
}

// InvalidateAuth removes all sessions bound to a specific auth ID.
// Used when an auth becomes unavailable.
func (c *SessionCache) InvalidateAuth(authID string) {
	if authID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.generation++
	for sid, entry := range c.entries {
		if entry.authID == authID {
			delete(c.entries, sid)
		}
	}
}

// Stop terminates the background cleanup goroutine.
func (c *SessionCache) Stop() {
	if c == nil {
		return
	}
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
	for sid, entry := range c.entries {
		if !now.Before(entry.expiresAt) {
			delete(c.entries, sid)
		}
	}
	c.mu.Unlock()
}
