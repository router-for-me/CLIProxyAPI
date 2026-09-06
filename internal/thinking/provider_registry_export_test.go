package thinking

import (
	"sort"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func RegisteredProvidersForTest() []string {
	providerAppliersMu.RLock()
	defer providerAppliersMu.RUnlock()
	providers := make([]string, 0, len(nativeProviderAppliers)+len(pluginProviderAppliers))
	for provider, applier := range nativeProviderAppliers {
		if applier != nil {
			providers = append(providers, provider)
		}
	}
	for provider, record := range pluginProviderAppliers {
		if record.applier != nil {
			providers = append(providers, provider)
		}
	}
	sort.Strings(providers)
	return providers
}

func ResolveOpenAICompatibilityConfigForTest(config ThinkingConfig, modelInfo *registry.ModelInfo) (ThinkingConfig, bool) {
	return resolveOpenAICompatibilityConfig(config, modelInfo)
}
