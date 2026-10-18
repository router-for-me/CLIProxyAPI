package auth

import "strings"

// SupportsCodexUltrafast reports the ProMax entitlement of a Codex account.
func (a *Auth) SupportsCodexUltrafast() bool {
	return a != nil && strings.EqualFold(strings.TrimSpace(a.Provider), "codex") &&
		strings.EqualFold(authAttribute(a, "plan_type"), "promax")
}

// HasCodexUltrafast reports whether any enabled account supports Ultrafast.
func (m *Manager) HasCodexUltrafast() bool {
	if m == nil {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	for _, a := range m.auths {
		if !a.Disabled && a.Status != StatusDisabled && a.SupportsCodexUltrafast() {
			return true
		}
	}
	return false
}
