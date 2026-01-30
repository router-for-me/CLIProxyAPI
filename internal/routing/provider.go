// Package routing provides unified model routing for all provider types.
package routing

import (
	"context"

	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

// ProviderType indicates the type of provider.
type ProviderType string

const (
	ProviderTypeOAuth  ProviderType = "oauth"
	ProviderTypeAPIKey ProviderType = "api_key"
	ProviderTypeVertex ProviderType = "vertex"
)

// Provider is the unified interface for all provider types (OAuth, API key, etc.).
type Provider interface {
	// Name returns the unique provider identifier.
	Name() string

	// Type returns the provider type.
	Type() ProviderType

	// SupportsModel returns true if this provider can handle the given model.
	SupportsModel(model string) bool

	// Available returns true if the provider is available for the model (not quota exceeded).
	Available(model string) bool

	// Priority returns the priority for this provider (lower = tried first).
	Priority() int

	// Execute sends the request to the provider.
	Execute(ctx context.Context, model string, req executor.Request) (executor.Response, error)

	// ExecuteStream sends a streaming request to the provider.
	ExecuteStream(ctx context.Context, model string, req executor.Request) (<-chan executor.StreamChunk, error)
}

// ProviderCandidate represents a provider + model combination to try.
type ProviderCandidate struct {
	Provider Provider
	Model    string // The actual model name to use (may be different from requested due to aliasing)
}

// Registry manages all available providers.
type Registry struct {
	providers []Provider
}

// NewRegistry creates a new provider registry.
func NewRegistry() *Registry {
	return &Registry{
		providers: make([]Provider, 0),
	}
}

// Register adds a provider to the registry.
func (r *Registry) Register(p Provider) {
	r.providers = append(r.providers, p)
}

// FindProviders returns all providers that support the given model and are available.
func (r *Registry) FindProviders(model string) []Provider {
	var result []Provider
	for _, p := range r.providers {
		if p.SupportsModel(model) && p.Available(model) {
			result = append(result, p)
		}
	}
	return result
}

// All returns all registered providers.
func (r *Registry) All() []Provider {
	return r.providers
}
