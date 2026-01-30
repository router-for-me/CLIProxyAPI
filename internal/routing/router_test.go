package routing

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
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
