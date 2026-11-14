package auth

// ApplyPatchSupport optionally advertises the actual executor's tool contract.
// It does not change ProviderExecutor or the plugin ABI.
type ApplyPatchSupport interface {
	SupportsApplyPatch() bool
}

// SupportsApplyPatchForProviders requires support from every routing candidate.
func (m *Manager) SupportsApplyPatchForProviders(providers []string) bool {
	if m == nil || len(providers) == 0 {
		return false
	}
	for _, provider := range providers {
		executor, okExecutor := m.Executor(provider)
		if !okExecutor {
			return false
		}
		support, okSupport := executor.(ApplyPatchSupport)
		if !okSupport || !support.SupportsApplyPatch() {
			return false
		}
	}
	return true
}
