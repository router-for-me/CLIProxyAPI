package auth

// WatchAuthChanges observes structural changes to one runtime credential,
// including token preparation/refresh, replacement, removal and store reload.
// Notifications are coalesced: consumers must inspect the latest manager state.
// The caller must unsubscribe when its work ends. Request result accounting does
// not notify watchers, so ordinary Generation increments do not invalidate work.
func (m *Manager) WatchAuthChanges(authID string) (<-chan struct{}, func()) {
	changes := make(chan struct{}, 1)
	if m == nil || authID == "" {
		close(changes)
		return changes, func() {}
	}
	m.mu.Lock()
	if m.authChangeWatchers == nil {
		m.authChangeWatchers = make(map[string]map[chan struct{}]struct{})
	}
	if m.authChangeWatchers[authID] == nil {
		m.authChangeWatchers[authID] = make(map[chan struct{}]struct{})
	}
	m.authChangeWatchers[authID][changes] = struct{}{}
	m.mu.Unlock()
	return changes, func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		watchers := m.authChangeWatchers[authID]
		if _, exists := watchers[changes]; !exists {
			return
		}
		delete(watchers, changes)
		if len(watchers) == 0 {
			delete(m.authChangeWatchers, authID)
		}
		close(changes)
	}
}

// Caller holds m.mu. No user callback or blocking work runs under the manager lock.
func (m *Manager) notifyAuthChangeLocked(authID string) {
	for changes := range m.authChangeWatchers[authID] {
		select {
		case changes <- struct{}{}:
		default:
		}
	}
}
