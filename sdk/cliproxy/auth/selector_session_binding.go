package auth

import "strings"

// SessionBindingLookup reports the credential a session is currently bound to.
type SessionBindingLookup interface {
	BoundAuthID(provider, sessionID, model string) (string, bool)
}

// BoundAuthID reports the auth a session is currently bound to without
// refreshing the binding's TTL.
//
// A read that refreshed the entry would let a background caller keep a stale
// binding alive, so this deliberately uses the non-refreshing lookup.
func (s *SessionAffinitySelector) BoundAuthID(provider, sessionID, model string) (string, bool) {
	if s == nil || s.cache == nil {
		return "", false
	}
	provider = strings.TrimSpace(provider)
	sessionID = strings.TrimSpace(sessionID)
	if provider == "" || sessionID == "" {
		return "", false
	}
	return s.cache.Get(provider + "::" + sessionID + "::" + canonicalModelKey(model))
}

// SessionBindingLookup returns the active session-to-credential binding lookup.
//
// It reports false when routing is not session-sticky, which callers must treat
// as "binding unknown" rather than "session unbound": with no affinity selector
// there is no binding to lose.
func (m *Manager) SessionBindingLookup() (SessionBindingLookup, bool) {
	if m == nil {
		return nil, false
	}
	lookup, ok := m.Selector().(SessionBindingLookup)
	if !ok || lookup == nil {
		return nil, false
	}
	return lookup, true
}
