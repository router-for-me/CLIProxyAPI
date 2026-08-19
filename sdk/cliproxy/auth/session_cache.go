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
	generation uint64
	stopOnce   sync.Once
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

// GetWithGeneration retrieves the auth ID, monotonic generation token, and alias list
// bound to a session without refreshing the TTL.
func (c *SessionCache) GetWithGeneration(sessionID string) (string, uint64, []string, bool) {
	if sessionID == "" {
		return "", 0, nil, false
	}
	now := time.Now()
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.entries[sessionID]
	if !ok || !now.Before(entry.expiresAt) {
		return "", 0, nil, false
	}
	return entry.authID, entry.generation, append([]string(nil), entry.aliases...), true
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
	c.generation++
	gen := c.generation
	for _, previous := range previousGroups {
		c.removeAliasGroupLocked(previous)
	}
	entry := sessionEntry{authID: authID, expiresAt: expiresAt, aliases: aliases, generation: gen}
	for _, alias := range aliases {
		c.entries[alias] = entry
	}
}

func (c *SessionCache) removeAliasGroupLocked(entry sessionEntry) {
	for _, alias := range entry.aliases {
		current, ok := c.entries[alias]
		if !ok || current.authID != entry.authID || !current.expiresAt.Equal(entry.expiresAt) ||
			!equalSessionAliases(current.aliases, entry.aliases) {
			continue
		}
		delete(c.entries, alias)
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
	entry, ok := c.entries[sessionID]
	if !ok {
		return
	}
	delete(c.entries, sessionID)
	c.generation++
	gen := c.generation
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
		current.generation = gen
		c.entries[alias] = current
	}
}

// CompareAndDeleteAliases removes the alias group holding sessionID when it is
// still bound to expectedAuthID, and returns the group's aliases. A stale
// expectation cannot remove a newer group.
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
	c.generation++
	for _, alias := range aliases {
		if current, exists := c.entries[alias]; exists && current.authID == expectedAuthID && equalSessionAliases(current.aliases, entry.aliases) {
			delete(c.entries, alias)
		}
	}
	return aliases
}

// CompareAndDeleteGroup removes a binding only when its auth ID, generation,
// and alias set all still match the observed values, and returns the removed
// aliases. A concurrent refresh or extension of the group bumps the
// generation or changes the aliases, so a stale observation cannot delete
// newer state; callers retry their merge on a nil result.
//
// Mirror of CLIProxyAPI dd8c72a3.
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
	c.generation++
	for _, alias := range removed {
		if current, exists := c.entries[alias]; exists && current.authID == expectedAuthID && equalSessionAliases(current.aliases, entry.aliases) {
			delete(c.entries, alias)
		}
	}
	return removed
}

// CompareAndReplaceAliases atomically validates that every observed alias still
// maps to expectedAuthID with expectedGen, that all observed aliases belong to the
// exact same group, and that no additional alias is currently bound to
// another active group. Upon validation, it replaces the entire alias group with
// newAuthID, a refreshed TTL, and an incremented monotonic generation token.
// It returns true if replaced, or false if the CAS precondition failed.
func (c *SessionCache) CompareAndReplaceAliases(
	expectedAuthID string,
	expectedGen uint64,
	observedAliases []string,
	newAuthID string,
	additionalAliases ...string,
) bool {
	if c == nil || expectedAuthID == "" || expectedGen == 0 || len(observedAliases) == 0 || newAuthID == "" {
		return false
	}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()

	var matchedEntry *sessionEntry
	for _, alias := range observedAliases {
		entry, exists := c.entries[alias]
		if !exists || !now.Before(entry.expiresAt) {
			return false
		}
		if entry.authID != expectedAuthID || entry.generation != expectedGen {
			return false
		}
		if !equalSessionAliases(entry.aliases, observedAliases) {
			return false
		}
		if matchedEntry == nil {
			entryCopy := entry
			matchedEntry = &entryCopy
		}
	}
	if matchedEntry == nil || len(matchedEntry.aliases) != len(observedAliases) {
		return false
	}

	allAliases := mergeSessionAliases(observedAliases, additionalAliases...)
	allAliases = compactSessionAliases(allAliases)
	if len(allAliases) == 0 {
		return false
	}

	observedSet := make(map[string]struct{}, len(observedAliases))
	for _, a := range observedAliases {
		observedSet[a] = struct{}{}
	}

	for _, alias := range allAliases {
		if _, isObserved := observedSet[alias]; isObserved {
			continue
		}
		if existing, exists := c.entries[alias]; exists && now.Before(existing.expiresAt) {
			return false
		}
	}

	c.replaceAliasGroupsLocked(newAuthID, now.Add(c.ttl), allAliases, *matchedEntry)
	return true
}

// InvalidateAuth removes all sessions bound to a specific auth ID.
// Used when an auth becomes unavailable.
func (c *SessionCache) InvalidateAuth(authID string) {
	if authID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
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
	defer c.mu.Unlock()
	for sid, entry := range c.entries {
		if !now.Before(entry.expiresAt) {
			delete(c.entries, sid)
		}
	}
}
