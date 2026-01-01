package cliproxy

import "github.com/router-for-me/CLIProxyAPI/v6/internal/registry"

// ModelInfo re-exports the registry model info structure.
type ModelInfo = registry.ModelInfo

// ModelRegistry describes registry operations consumed by external callers.
type ModelRegistry interface {
	RegisterClient(clientID, clientProvider string, models []*ModelInfo)
	UnregisterClient(clientID string)
	SetModelQuotaExceeded(clientID, modelID string)
	ClearModelQuotaExceeded(clientID, modelID string)
	ClientSupportsModel(clientID, modelID string) bool
	GetAvailableModels(handlerType string) []map[string]any
	GetAvailableModelsByProvider(provider string) []*ModelInfo
}

// GlobalModelRegistry returns the shared registry instance.
func GlobalModelRegistry() ModelRegistry {
	return registry.GetGlobalRegistry()
}
