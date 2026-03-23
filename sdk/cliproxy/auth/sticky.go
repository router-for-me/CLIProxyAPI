package auth

import (
	"sync"
	"time"
)

// stickyStore maintains session-to-auth bindings so that requests carrying the
// same session ID are routed to the same auth/account.  Entries expire after a
// configurable TTL and are garbage-collected by Cleanup.
//
// maxEntries caps the number of stored bindings to prevent memory exhaustion
// from untrusted X-CLIProxyAPI-Session-ID headers.
type stickyStore struct {
	mu         sync.RWMutex
	entries    map[string]stickyEntry
	maxEntries int
}

type stickyEntry struct {
	authID    string
	expiresAt time.Time
}

// stickyMaxEntries is the upper bound on stored session bindings.
const stickyMaxEntries = 10_000

// StickyMaxSessionIDLen limits the accepted X-CLIProxyAPI-Session-ID length.
const StickyMaxSessionIDLen = 256

func newStickyStore() *stickyStore {
	return &stickyStore{entries: make(map[string]stickyEntry), maxEntries: stickyMaxEntries}
}

// Get returns the bound auth ID for the given session, if it exists and has not
// expired.
func (s *stickyStore) Get(sessionID string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	e, ok := s.entries[sessionID]
	if !ok || time.Now().After(e.expiresAt) {
		return "", false
	}
	return e.authID, true
}

// Set binds a session to an auth ID with the specified TTL.
// If the store is at capacity, the write is silently dropped.
func (s *stickyStore) Set(sessionID, authID string, ttl time.Duration) {
	s.mu.Lock()
	if _, exists := s.entries[sessionID]; !exists && len(s.entries) >= s.maxEntries {
		s.mu.Unlock()
		return
	}
	s.entries[sessionID] = stickyEntry{authID: authID, expiresAt: time.Now().Add(ttl)}
	s.mu.Unlock()
}

// Delete removes the binding for the given session ID.
func (s *stickyStore) Delete(sessionID string) {
	s.mu.Lock()
	delete(s.entries, sessionID)
	s.mu.Unlock()
}

// Cleanup removes all expired entries.
func (s *stickyStore) Cleanup() {
	now := time.Now()
	s.mu.Lock()
	for k, e := range s.entries {
		if now.After(e.expiresAt) {
			delete(s.entries, k)
		}
	}
	s.mu.Unlock()
}

// Len returns the number of entries (including possibly-expired ones).
func (s *stickyStore) Len() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.entries)
}
