package management

// SerializeRoutingPublication orders routing publication with management
// configuration reads and writes, so a stale panel write cannot drop it.
func (h *Handler) SerializeRoutingPublication(publish func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	publish()
}
