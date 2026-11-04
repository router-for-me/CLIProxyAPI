package handlers

// SupportsNativeToolSearchModel reports whether every credential serving the
// public model can receive Codex tool_search as Anthropic native tool search.
func (h *BaseAPIHandler) SupportsNativeToolSearchModel(model string) bool {
	if h == nil || h.AuthManager == nil {
		return false
	}
	return h.AuthManager.SupportsNativeToolSearchForModel(model)
}
