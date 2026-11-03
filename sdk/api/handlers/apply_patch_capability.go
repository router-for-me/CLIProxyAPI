package handlers

// SupportsApplyPatchModel follows the same public, non-image routing as execution.
func (h *BaseAPIHandler) SupportsApplyPatchModel(model string) bool {
	if h == nil || h.AuthManager == nil {
		return false
	}
	providers, _, errGetRequestDetails := h.getRequestDetails(model)
	if errGetRequestDetails != nil {
		return false
	}
	return h.AuthManager.SupportsApplyPatchForProviders(providers)
}
