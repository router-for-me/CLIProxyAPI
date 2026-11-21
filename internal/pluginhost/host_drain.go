package pluginhost

// HasLoadedPlugins includes pending loads and retired instances whose work may
// outlive a request. Maintenance cannot safely drain those plugin-owned tasks.
func (h *Host) HasLoadedPlugins() bool {
	if h == nil {
		return false
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.loaded) > 0 || len(h.loading) > 0 || len(h.retired) > 0
}
