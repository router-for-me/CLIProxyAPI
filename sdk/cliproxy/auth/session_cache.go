package auth

import (
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	maxStableSessionAliases = 64

	// maxSessionCacheEntries bounds the total number of cache keys. The alias
	// cap above limits aliases per logical session, not sessions; without an
	// entry bound, any per-request-ish session signal would grow the cache by
	// one entry per request for a full TTL.
	maxSessionCacheEntries = 10000
)

// sessionEntry stores an auth binding, its identifier aliases, and expiration.
type sessionEntry struct {
	authID    string
	expiresAt time.Time
	aliases   []string
	// lastTouched preserves insertion/refresh order for drop-oldest eviction
	// when the entry bound is exceeded. Aliases within one group share the
	// value, so the group is evicted as a unit.
	lastTouched time.Time
}

// SessionAffinityBinding is a safe, anonymized projection of one cache entry.
// The in-memory cache key is "provider::sessionID::model"; the dashboard does
// not need that internal format, so Snapshot splits it back into fields.
type SessionAffinityBinding struct {
	Provider  string
	SessionID string
	Model     string
	AuthID    string
	ExpiresAt time.Time
}

// SessionCache provides TTL-based session to auth mapping with automatic cleanup.
type SessionCache struct {
	mu      sync.RWMutex
	entries map[string]sessionEntry
	ttl     time.Duration
	stopCh  chan struct{}
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
	refreshed := sessionEntry{authID: entry.authID, expiresAt: now.Add(c.ttl), aliases: aliases, lastTouched: now}
	c.replaceAliasGroupsLocked(refreshed, refreshed, entry)
	return entry.authID, true
}

// replaceAliasGroupsLocked writes one alias group and enforces the entry
// bound. previousGroups are removed first so a refreshed binding replaces
// its stale copies instead of duplicating them.
func (c *SessionCache) replaceAliasGroupsLocked(entry sessionEntry, previousGroups ...sessionEntry) {
	for _, previous := range previousGroups {
		c.removeAliasGroupLocked(previous)
	}
	for _, alias := range entry.aliases {
		c.entries[alias] = entry
	}
	c.evictLocked(entry)
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
	entry := sessionEntry{authID: authID, expiresAt: now.Add(c.ttl), aliases: aliases, lastTouched: now}
	c.replaceAliasGroupsLocked(entry, previousGroups...)
}

// evictLocked enforces the entry bound after an insert. keep is the group the
// caller just inserted or refreshed — it is never a candidate for eviction in
// this pass. Expired entries are reclaimed first; if the cap is still
// exceeded, the least recently touched alias groups are dropped until the
// cache is back under the low-water mark (90% of the cap), so the O(n log n)
// selection runs once per ~10% overshoot rather than on every insert.
func (c *SessionCache) evictLocked(keep sessionEntry) {
	if len(c.entries) <= maxSessionCacheEntries {
		return
	}
	now := time.Now()
	for key, entry := range c.entries {
		if entry.authID == keep.authID && entry.expiresAt.Equal(keep.expiresAt) && equalSessionAliases(entry.aliases, keep.aliases) {
			continue
		}
		if !now.Before(entry.expiresAt) {
			delete(c.entries, key)
		}
	}
	if len(c.entries) <= maxSessionCacheEntries {
		return
	}
	lowWaterMark := maxSessionCacheEntries * 9 / 10
	over := len(c.entries) - lowWaterMark
	groups := make([]sessionEntry, 0, len(c.entries))
	seen := make(map[string]struct{}, len(c.entries))
	for _, entry := range c.entries {
		if len(entry.aliases) == 0 {
			continue
		}
		// Aliases within one group are identical entries; the first alias
		// identifies the group. sessionEntry holds a slice so it cannot be a
		// map key itself.
		if _, dup := seen[entry.aliases[0]]; dup {
			continue
		}
		seen[entry.aliases[0]] = struct{}{}
		groups = append(groups, entry)
	}
	sort.Slice(groups, func(i, j int) bool { return groups[i].lastTouched.Before(groups[j].lastTouched) })
	for _, group := range groups {
		if over <= 0 {
			return
		}
		if group.authID == keep.authID && group.expiresAt.Equal(keep.expiresAt) && equalSessionAliases(group.aliases, keep.aliases) {
			continue
		}
		c.removeAliasGroupLocked(group)
		over -= len(group.aliases)
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

// Adopt copies every unexpired entry from src into c, skipping keys that
// already have a live destination entry. Used when a routing-config update
// replaces one SessionAffinitySelector with another: the old cache would
// otherwise be dropped with all its session bindings. The source cache is not
// modified or stopped — the caller owns its lifecycle. Returns how many
// entries were copied.
func (c *SessionCache) Adopt(src *SessionCache) int {
	if src == nil || src == c {
		return 0
	}
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	src.mu.RLock()
	adopted := 0
	for key, entry := range src.entries {
		if !now.Before(entry.expiresAt) {
			continue
		}
		if current, ok := c.entries[key]; ok && now.Before(current.expiresAt) {
			continue
		}
		c.entries[key] = entry
		adopted++
	}
	src.mu.RUnlock()
	return adopted
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

// InvalidateAuth removes all sessions bound to a specific auth ID and returns
// how many LIVE bindings were removed (entries whose TTL had already expired
// are deleted but not counted). Used when an auth becomes unavailable or is
// removed; the count lets callers report the blast radius.
func (c *SessionCache) InvalidateAuth(authID string) int {
	if authID == "" {
		return 0
	}
	now := time.Now()
	c.mu.Lock()
	deleted := 0
	for sid, entry := range c.entries {
		if entry.authID == authID {
			delete(c.entries, sid)
			if now.Before(entry.expiresAt) {
				deleted++
			}
		}
	}
	c.mu.Unlock()
	return deleted
}

// invalidateSessionID removes every binding whose session-ID component matches
// the given sessionID. Because the cache key is composite
// "provider::sessionID::model", one logical session may span several entries
// (different providers/models for the same conversation). Dropping by the
// sessionID part revokes the whole session in one call, which is the
// operator-facing "revoke session" semantics.
func (c *SessionCache) invalidateSessionID(sessionID string) {
	if sessionID == "" {
		return
	}
	c.mu.Lock()
	for key := range c.entries {
		if _, sid, _ := splitAffinityKeyLocked(key); sid == sessionID {
			delete(c.entries, key)
		}
	}
	c.mu.Unlock()
}

// Stop terminates the background cleanup goroutine.
func (c *SessionCache) Stop() {
	select {
	case <-c.stopCh:
	default:
		close(c.stopCh)
	}
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

// affinityKeySeparator is the delimiter used to build the composite cache key
// "provider::sessionID::model" in SessionAffinitySelector.Pick. Splitting it
// back here lets Snapshot expose structured fields to the dashboard.
const affinityKeySeparator = "::"

// Snapshot returns a stable, anonymized view of the live bindings, skipping
// expired entries. The returned slice is sorted by provider, session ID, then
// model so callers see deterministic ordering across polls. Callers must not
// mutate the returned structs' fields expecting to affect cache state — the
// only supported mutation paths are Invalidate / InvalidateAuth / Set.
func (c *SessionCache) Snapshot() []SessionAffinityBinding {
	if c == nil {
		return nil
	}
	now := time.Now()
	type pending struct {
		provider, sessionID, model, authID string
		expiresAt                          time.Time
	}
	out := make([]SessionAffinityBinding, 0, len(c.entries))
	c.mu.RLock()
	for key, entry := range c.entries {
		if now.After(entry.expiresAt) {
			continue // expired; cleaned lazily on access
		}
		provider, sessionID, model := splitAffinityKeyLocked(key)
		out = append(out, SessionAffinityBinding{
			Provider:  provider,
			SessionID: sessionID,
			Model:     model,
			AuthID:    entry.authID,
			ExpiresAt: entry.expiresAt,
		})
	}
	c.mu.RUnlock()

	sort.Slice(out, func(i, j int) bool {
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		if out[i].SessionID != out[j].SessionID {
			return out[i].SessionID < out[j].SessionID
		}
		return out[i].Model < out[j].Model
	})
	return out
}

// splitAffinityKeyLocked splits a composite cache key
// "provider::sessionID::model" back into its three parts. Session IDs and
// models are validated upstream to never contain the "::" sequence, so a
// SplitN with limit 3 reproduces the original triple exactly even when one
// field is empty (e.g. an empty model leaves a trailing "::model" is not
// possible; empties are preserved positionally). Unparseable keys fall back to
// returning the whole key as the session ID so the binding is still visible
// rather than silently dropped.
func splitAffinityKeyLocked(key string) (provider, sessionID, model string) {
	parts := strings.SplitN(key, affinityKeySeparator, 3)
	switch len(parts) {
	case 3:
		return parts[0], parts[1], parts[2]
	case 2:
		return parts[0], parts[1], ""
	case 1:
		return "", parts[0], ""
	default:
		return "", key, ""
	}
}
