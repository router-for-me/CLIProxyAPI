package routing

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	globalRegistry "github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	"github.com/stretchr/testify/assert"
)

// mockProvider is a test double for Provider.
type mockProvider struct {
	name           string
	providerType   ProviderType
	supportsModels map[string]bool
	available      bool
	priority       int
}

func (m *mockProvider) Name() string                             { return m.name }
func (m *mockProvider) Type() ProviderType                       { return m.providerType }
func (m *mockProvider) SupportsModel(model string) bool          { return m.supportsModels[model] }
func (m *mockProvider) Available(model string) bool              { return m.available }
func (m *mockProvider) Priority() int                            { return m.priority }
func (m *mockProvider) Execute(ctx context.Context, model string, req executor.Request) (executor.Response, error) {
	return executor.Response{}, nil
}
func (m *mockProvider) ExecuteStream(ctx context.Context, model string, req executor.Request) (<-chan executor.StreamChunk, error) {
	return nil, nil
}

func TestRouter_Resolve_ModelMappings(t *testing.T) {
	registry := NewRegistry()
	
	// Add a provider
	p := &mockProvider{
		name:           "test-provider",
		providerType:   ProviderTypeOAuth,
		supportsModels: map[string]bool{"target-model": true},
		available:      true,
		priority:       1,
	}
	registry.Register(p)
	
	// Create router with model mapping
	cfg := &config.Config{
		AmpCode: config.AmpCode{
			ModelMappings: []config.AmpModelMapping{
				{From: "user-model", To: "target-model"},
			},
		},
	}
	router := NewRouter(registry, cfg)
	
	// Resolve
	decision := router.Resolve("user-model")
	
	assert.Equal(t, "user-model", decision.RequestedModel)
	assert.Equal(t, "target-model", decision.ResolvedModel)
	assert.Len(t, decision.Candidates, 1)
	assert.Equal(t, "target-model", decision.Candidates[0].Model)
}

func TestRouter_Resolve_OAuthAliases(t *testing.T) {
	registry := NewRegistry()
	
	// Add providers
	p1 := &mockProvider{
		name:           "oauth-1",
		providerType:   ProviderTypeOAuth,
		supportsModels: map[string]bool{"primary-model": true},
		available:      true,
		priority:       1,
	}
	p2 := &mockProvider{
		name:           "oauth-2",
		providerType:   ProviderTypeOAuth,
		supportsModels: map[string]bool{"fallback-model": true},
		available:      true,
		priority:       2,
	}
	registry.Register(p1)
	registry.Register(p2)
	
	// Create router with oauth aliases
	cfg := &config.Config{
		OAuthModelAlias: map[string][]config.OAuthModelAlias{
			"test-channel": {
				{Name: "primary-model", Alias: "fallback-model"},
			},
		},
	}
	router := NewRouter(registry, cfg)
	
	// Resolve
	decision := router.Resolve("primary-model")
	
	assert.Equal(t, "primary-model", decision.ResolvedModel)
	assert.Len(t, decision.Candidates, 2)
	// Primary should come first (lower priority value)
	assert.Equal(t, "primary-model", decision.Candidates[0].Model)
	assert.Equal(t, "fallback-model", decision.Candidates[1].Model)
}

func TestRouter_Resolve_NoProviders(t *testing.T) {
	registry := NewRegistry()
	cfg := &config.Config{}
	router := NewRouter(registry, cfg)
	
	decision := router.Resolve("unknown-model")
	
	assert.Equal(t, "unknown-model", decision.ResolvedModel)
	assert.Empty(t, decision.Candidates)
}

// === Global Registry Fallback Tests (T-027) ===
// These tests verify that when the internal registry is empty,
// the router falls back to the global model registry.
// This is the core fix for the thinking signature 400 error.

func TestRouter_GlobalRegistryFallback_LocalProvider(t *testing.T) {
	// This test requires registering a model in the global registry.
	// We use a model that's already registered via api-key config in production.
	// For isolated testing, we can skip if global registry is not populated.
	
	globalReg := globalRegistry.GetGlobalRegistry()
	modelCount := globalReg.GetModelCount("claude-sonnet-4-20250514")
	
	if modelCount == 0 {
		t.Skip("Global registry not populated - run with server context")
	}
	
	// Empty internal registry
	emptyRegistry := NewRegistry()
	cfg := &config.Config{}
	router := NewRouter(emptyRegistry, cfg)
	
	req := RoutingRequest{
		RequestedModel:      "claude-sonnet-4-20250514",
		PreferLocalProvider: true,
	}
	decision := router.ResolveV2(req)
	
	// Should find provider from global registry
	assert.Equal(t, RouteTypeLocalProvider, decision.RouteType)
	assert.Equal(t, "claude-sonnet-4-20250514", decision.ResolvedModel)
	assert.False(t, decision.ShouldProxy)
}

func TestRouter_GlobalRegistryFallback_ModelMapping(t *testing.T) {
	// This test verifies that model mapping works with global registry fallback.
	
	globalReg := globalRegistry.GetGlobalRegistry()
	modelCount := globalReg.GetModelCount("claude-opus-4-5-thinking")
	
	if modelCount == 0 {
		t.Skip("Global registry not populated - run with server context")
	}
	
	// Empty internal registry
	emptyRegistry := NewRegistry()
	cfg := &config.Config{
		AmpCode: config.AmpCode{
			ModelMappings: []config.AmpModelMapping{
				{From: "claude-opus-4-5-20251101", To: "claude-opus-4-5-thinking"},
			},
		},
	}
	router := NewRouter(emptyRegistry, cfg)
	
	req := RoutingRequest{
		RequestedModel:      "claude-opus-4-5-20251101",
		PreferLocalProvider: true,
	}
	decision := router.ResolveV2(req)
	
	// Should find mapped model from global registry
	assert.Equal(t, RouteTypeModelMapping, decision.RouteType)
	assert.Equal(t, "claude-opus-4-5-thinking", decision.ResolvedModel)
	assert.False(t, decision.ShouldProxy)
}

func TestRouter_GlobalRegistryFallback_AmpCreditsWhenNotFound(t *testing.T) {
	// Empty internal registry
	emptyRegistry := NewRegistry()
	cfg := &config.Config{}
	router := NewRouter(emptyRegistry, cfg)
	
	// Use a model that definitely doesn't exist anywhere
	req := RoutingRequest{
		RequestedModel:      "nonexistent-model-12345",
		PreferLocalProvider: true,
	}
	decision := router.ResolveV2(req)
	
	// Should fall back to AMP credits proxy
	assert.Equal(t, RouteTypeAmpCredits, decision.RouteType)
	assert.Equal(t, "nonexistent-model-12345", decision.ResolvedModel)
	assert.True(t, decision.ShouldProxy)
}
