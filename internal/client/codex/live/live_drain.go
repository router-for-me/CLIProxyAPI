package live

// ActiveSessions includes detached WebRTC calls that outlive bootstrap requests.
func (h *Handler) ActiveSessions() int {
	if h == nil || h.sessions == nil {
		return 0
	}
	h.sessions.mu.Lock()
	defer h.sessions.mu.Unlock()
	return len(h.sessions.sessions)
}
