package auth

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// sessionEntry stores auth binding with expiration.
type sessionEntry struct {
	authID    string
	expiresAt time.Time
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
	c.mu.RLock()
	entry, ok := c.entries[sessionID]
	c.mu.RUnlock()
	if !ok {
		return "", false
	}
	if time.Now().After(entry.expiresAt) {
		c.mu.Lock()
		delete(c.entries, sessionID)
		c.mu.Unlock()
		return "", false
	}
	return entry.authID, true
}

// GetAndRefresh retrieves the auth ID bound to a session and refreshes TTL on hit.
// This extends the binding lifetime for active sessions.
func (c *SessionCache) GetAndRefresh(sessionID string) (string, bool) {
	if sessionID == "" {
		return "", false
	}
	now := time.Now()
	c.mu.Lock()
	entry, ok := c.entries[sessionID]
	if !ok {
		c.mu.Unlock()
		return "", false
	}
	if now.After(entry.expiresAt) {
		delete(c.entries, sessionID)
		c.mu.Unlock()
		return "", false
	}
	// Refresh TTL on successful access
	entry.expiresAt = now.Add(c.ttl)
	c.entries[sessionID] = entry
	c.mu.Unlock()
	return entry.authID, true
}

// Set binds a session to an auth ID with TTL refresh.
func (c *SessionCache) Set(sessionID, authID string) {
	if sessionID == "" || authID == "" {
		return
	}
	c.mu.Lock()
	c.entries[sessionID] = sessionEntry{
		authID:    authID,
		expiresAt: time.Now().Add(c.ttl),
	}
	c.mu.Unlock()
}

// Invalidate removes a specific session binding.
func (c *SessionCache) Invalidate(sessionID string) {
	if sessionID == "" {
		return
	}
	c.mu.Lock()
	delete(c.entries, sessionID)
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
		if now.After(entry.expiresAt) {
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
