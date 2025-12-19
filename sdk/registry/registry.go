// Package registry provides model registry types for the CLI Proxy SDK.
// This package re-exports types from internal/registry, allowing SDK consumers
// to use registry types without directly importing internal packages.
package registry

import (
	internalregistry "github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
)

// ModelInfo represents information about an available model.
// This is a type alias to internal/registry.ModelInfo.
type ModelInfo = internalregistry.ModelInfo

// ThinkingSupport describes a model family's supported internal reasoning budget range.
// This is a type alias to internal/registry.ThinkingSupport.
type ThinkingSupport = internalregistry.ThinkingSupport

// ModelRegistration tracks a model's availability.
// This is a type alias to internal/registry.ModelRegistration.
type ModelRegistration = internalregistry.ModelRegistration

// ModelRegistry is a type alias to internal/registry.ModelRegistry.
// It manages the global registry of available models.
type ModelRegistry = internalregistry.ModelRegistry

// GetGlobalRegistry returns the global model registry instance.
// This delegates to internal/registry.GetGlobalRegistry.
func GetGlobalRegistry() *ModelRegistry {
	return internalregistry.GetGlobalRegistry()
}
