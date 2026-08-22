package auth

import (
	"strings"
	"sync"
	"time"
)

const maxStableSessionAliases = 64

// sessionEntry stores an auth binding, its identifier aliases, and expiration.
type sessionEntry struct {
	authID    string
	expiresAt time.Time
	aliases   []string
}

// SessionCache provides TTL-based session to auth mapping with automatic cleanup.
type SessionCache struct {
	mu       sync.RWMutex
	entries  map[string]sessionEntry
	ttl      time.Duration
	stopCh   chan struct{}
	stopOnce sync.Once
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
	c.setAliasesUntilLocked(authID, now.Add(c.ttl), sessionIDs...)
}

func (c *SessionCache) setAliasesUntilLocked(authID string, expiresAt time.Time, sessionIDs ...string) {
	now := time.Now()
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
	entry := sessionEntry{authID: authID, expiresAt: expiresAt, aliases: aliases}
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
	entry := sessionEntry{
		authID:    authID,
		expiresAt: now.Add(c.ttl),
		aliases:   aliases,
	}
	for _, alias := range aliases {
		c.entries[alias] = entry
	}
	return true
}

// SetAliasesIfAllAbsent atomically binds all sessionIDs to authID only when every
// alias is currently absent or expired. If any alias is already live, it returns
// the authID currently bound to the first occupied alias and false, without
// modifying anything.
func (c *SessionCache) SetAliasesIfAllAbsent(authID string, sessionIDs ...string) (string, bool) {
	if c == nil || authID == "" || len(sessionIDs) == 0 {
		return "", false
	}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()

	var absent []string
	for _, sid := range sessionIDs {
		if sid == "" {
			continue
		}
		if entry, ok := c.entries[sid]; ok && now.Before(entry.expiresAt) {
			return entry.authID, false
		}
		absent = append(absent, sid)
	}
	aliases := compactSessionAliases(absent)
	if len(aliases) == 0 {
		return "", false
	}
	entry := sessionEntry{
		authID:    authID,
		expiresAt: now.Add(c.ttl),
		aliases:   aliases,
	}
	for _, alias := range aliases {
		c.entries[alias] = entry
	}
	return authID, true
}

// SetAliasesIfNoConflict atomically binds all sessionIDs to authID. It succeeds
// when every alias is either absent or already bound to authID, attaching any
// free aliases to the existing group. If any alias is bound to a different auth,
// it returns that auth and false without modifying the cache. This combines the
// occupied-alias check and the attachment under a single lock so a concurrent
// request cannot bind a free alias to another auth between the two steps.
func (c *SessionCache) SetAliasesIfNoConflict(authID string, sessionIDs ...string) (string, bool) {
	if c == nil || authID == "" || len(sessionIDs) == 0 {
		return "", false
	}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()

	for _, sid := range sessionIDs {
		if sid == "" {
			continue
		}
		if entry, ok := c.entries[sid]; ok && now.Before(entry.expiresAt) && entry.authID != authID {
			return entry.authID, false
		}
	}
	c.setAliasesUntilLocked(authID, now.Add(c.ttl), sessionIDs...)
	return authID, true
}

// ReplaceAliasesIfUnchanged rebinds the alias group currently bound to
// expectedAuthID to newAuthID, merging in the provided sessionIDs. It succeeds
// only when every provided sessionID is either absent or bound to
// expectedAuthID, at least one provided sessionID is live and bound to
// expectedAuthID, and every reachable alias of that auth still maps to
// expectedAuthID. Multiple alias groups bound to the same expectedAuthID are
// merged into one before the rebind. If a concurrent caller already rebound the
// group to a different auth, the current auth is returned and the cache is left
// untouched.
func (c *SessionCache) ReplaceAliasesIfUnchanged(expectedAuthID, newAuthID string, sessionIDs ...string) (string, bool) {
	if c == nil || expectedAuthID == "" || newAuthID == "" || len(sessionIDs) == 0 {
		return "", false
	}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()

	aliasesSet := make(map[string]struct{})
	foundLive := false
	for _, sid := range sessionIDs {
		if sid == "" {
			continue
		}
		entry, ok := c.entries[sid]
		if !ok || !now.Before(entry.expiresAt) {
			aliasesSet[sid] = struct{}{}
			continue
		}
		if entry.authID != expectedAuthID {
			return entry.authID, false
		}
		foundLive = true
		aliasesSet[sid] = struct{}{}
		for _, alias := range entry.aliases {
			aliasesSet[alias] = struct{}{}
		}
	}
	if !foundLive || len(aliasesSet) == 0 {
		return "", false
	}

	// Closure: merge every alias group currently bound to expectedAuthID that
	// is reachable from the cold keys.
	for {
		added := false
		for alias := range aliasesSet {
			entry, ok := c.entries[alias]
			if !ok || !now.Before(entry.expiresAt) || entry.authID != expectedAuthID {
				continue
			}
			for _, a := range entry.aliases {
				if _, exists := aliasesSet[a]; !exists {
					aliasesSet[a] = struct{}{}
					added = true
				}
			}
		}
		if !added {
			break
		}
	}

	expectedAliases := compactSessionAliases(setToSlice(aliasesSet))

	// Verify the merged view is consistent: any already-live alias in the merged
	// group is still bound to expectedAuthID and has no aliases outside the
	// merged set. Absent aliases are added by the replacement.
	for _, alias := range expectedAliases {
		entry, ok := c.entries[alias]
		if !ok || !now.Before(entry.expiresAt) {
			continue
		}
		if entry.authID != expectedAuthID {
			return "", false
		}
		for _, a := range entry.aliases {
			if _, exists := aliasesSet[a]; !exists {
				return "", false
			}
		}
	}

	// Capture previous groups for deletion. Distinct entries are keyed by their
	// alias list so overlapping alias groups are only removed once.
	previousGroups := make(map[string]sessionEntry, len(expectedAliases))
	for _, alias := range expectedAliases {
		entry, ok := c.entries[alias]
		if !ok || !now.Before(entry.expiresAt) || entry.authID != expectedAuthID {
			continue
		}
		key := strings.Join(entry.aliases, "\x00")
		previousGroups[key] = entry
	}

	groups := make([]sessionEntry, 0, len(previousGroups))
	for _, entry := range previousGroups {
		groups = append(groups, entry)
	}

	newAliases := compactSessionAliases(mergeSessionAliases(expectedAliases, sessionIDs...))
	if len(newAliases) == 0 {
		return "", false
	}
	c.replaceAliasGroupsLocked(newAuthID, now.Add(c.ttl), newAliases, groups...)
	return newAuthID, true
}

func setToSlice(set map[string]struct{}) []string {
	slice := make([]string, 0, len(set))
	for s := range set {
		slice = append(slice, s)
	}
	return slice
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
			c.entries[alias] = current
		}
	}
	c.mu.Unlock()
}

// InvalidateAuth removes all sessions bound to a specific auth ID.
// Used when an auth becomes unavailable.
func (c *SessionCache) InvalidateAuth(authID string) {
	if authID == "" {
		return
	}
	c.mu.Lock()
	for sid, entry := range c.entries {
		if entry.authID == authID {
			delete(c.entries, sid)
		}
	}
	c.mu.Unlock()
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
