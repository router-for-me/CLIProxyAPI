package cliproxy

import "github.com/router-for-me/CLIProxyAPI/v6/sdk/registry"

// ModelInfo re-exports the registry model info structure.
// This is a type alias to sdk/registry.ModelInfo.
type ModelInfo = registry.ModelInfo

// ModelRegistry re-exports the registry type.
// This is a type alias to sdk/registry.ModelRegistry.
type ModelRegistry = registry.ModelRegistry

// GlobalModelRegistry returns the shared registry instance.
// This delegates to sdk/registry.GetGlobalRegistry.
func GlobalModelRegistry() *ModelRegistry {
	return registry.GetGlobalRegistry()
}
