package routing

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	"github.com/stretchr/testify/assert"
)

func TestRouter_DefaultMode_PrefersLocal(t *testing.T) {
	// Setup: Create a router with a mock provider that supports "gpt-4"
	registry := NewRegistry()
	mockProvider := &MockProvider{
		name:            "openai",
		supportedModels: []string{"gpt-4"},
		available:       true,
		priority:        1,
	}
	registry.Register(mockProvider)

	cfg := &config.Config{
		AmpCode: config.AmpCode{
			ModelMappings: []config.AmpModelMapping{
				{From: "gpt-4", To: "claude-local"},
			},
		},
	}

	router := NewRouter(registry, cfg)

	// Test: Request gpt-4 when local provider exists
	req := RoutingRequest{
		RequestedModel:      "gpt-4",
		PreferLocalProvider: true,
		ForceModelMapping:   false,
	}

	decision := router.ResolveV2(req)

	// Assert: Should return LOCAL_PROVIDER, not MODEL_MAPPING
	assert.Equal(t, RouteTypeLocalProvider, decision.RouteType)
	assert.Equal(t, "gpt-4", decision.ResolvedModel)
	assert.Equal(t, "openai", decision.ProviderName)
	assert.False(t, decision.ShouldProxy)
}

func TestRouter_DefaultMode_MapsWhenNoLocal(t *testing.T) {
	// Setup: Create a router with NO provider for "gpt-4" but a mapping to "claude-local"
	// which has a provider
	registry := NewRegistry()
	mockProvider := &MockProvider{
		name:            "anthropic",
		supportedModels: []string{"claude-local"},
		available:       true,
		priority:        1,
	}
	registry.Register(mockProvider)

	cfg := &config.Config{
		AmpCode: config.AmpCode{
			ModelMappings: []config.AmpModelMapping{
				{From: "gpt-4", To: "claude-local"},
			},
		},
	}

	router := NewRouter(registry, cfg)

	// Test: Request gpt-4 when no local provider exists, but mapping exists
	req := RoutingRequest{
		RequestedModel:      "gpt-4",
		PreferLocalProvider: true,
		ForceModelMapping:   false,
	}

	decision := router.ResolveV2(req)

	// Assert: Should return MODEL_MAPPING
	assert.Equal(t, RouteTypeModelMapping, decision.RouteType)
	assert.Equal(t, "claude-local", decision.ResolvedModel)
	assert.Equal(t, "anthropic", decision.ProviderName)
	assert.False(t, decision.ShouldProxy)
}

func TestRouter_DefaultMode_AmpCreditsWhenNoLocalOrMapping(t *testing.T) {
	// Setup: Create a router with no providers and no mappings
	registry := NewRegistry()

	cfg := &config.Config{
		AmpCode: config.AmpCode{
			ModelMappings: []config.AmpModelMapping{},
		},
	}

	router := NewRouter(registry, cfg)

	// Test: Request a model with no local provider and no mapping
	req := RoutingRequest{
		RequestedModel:      "unknown-model",
		PreferLocalProvider: true,
		ForceModelMapping:   false,
	}

	decision := router.ResolveV2(req)

	// Assert: Should return AMP_CREDITS with ShouldProxy=true
	assert.Equal(t, RouteTypeAmpCredits, decision.RouteType)
	assert.Equal(t, "unknown-model", decision.ResolvedModel)
	assert.True(t, decision.ShouldProxy)
	assert.Empty(t, decision.ProviderName)
}

func TestRouter_ForceMode_MapsEvenWithLocal(t *testing.T) {
	// Setup: Create a router with BOTH a local provider for "gpt-4" AND a mapping from "gpt-4" to "claude-local"
	// The mapping target "claude-local" also has a provider
	registry := NewRegistry()

	// Local provider for gpt-4
	openaiProvider := &MockProvider{
		name:            "openai",
		supportedModels: []string{"gpt-4"},
		available:       true,
		priority:        1,
	}
	registry.Register(openaiProvider)

	// Local provider for the mapped model
	anthropicProvider := &MockProvider{
		name:            "anthropic",
		supportedModels: []string{"claude-local"},
		available:       true,
		priority:        2,
	}
	registry.Register(anthropicProvider)

	cfg := &config.Config{
		AmpCode: config.AmpCode{
			ModelMappings: []config.AmpModelMapping{
				{From: "gpt-4", To: "claude-local"},
			},
		},
	}

	router := NewRouter(registry, cfg)

	// Test: Request gpt-4 with ForceModelMapping=true
	// Even though gpt-4 has a local provider, mapping should take precedence
	req := RoutingRequest{
		RequestedModel:      "gpt-4",
		PreferLocalProvider: false,
		ForceModelMapping:   true,
	}

	decision := router.ResolveV2(req)

	// Assert: Should return MODEL_MAPPING, not LOCAL_PROVIDER
	assert.Equal(t, RouteTypeModelMapping, decision.RouteType)
	assert.Equal(t, "claude-local", decision.ResolvedModel)
	assert.Equal(t, "anthropic", decision.ProviderName)
	assert.False(t, decision.ShouldProxy)
}

func TestRouter_ThinkingSuffix_Preserved(t *testing.T) {
	// Setup: Create a router with mapping and provider for mapped model
	registry := NewRegistry()

	mockProvider := &MockProvider{
		name:            "anthropic",
		supportedModels: []string{"claude-local"},
		available:       true,
		priority:        1,
	}
	registry.Register(mockProvider)

	cfg := &config.Config{
		AmpCode: config.AmpCode{
			ModelMappings: []config.AmpModelMapping{
				{From: "claude-3-5-sonnet", To: "claude-local"},
			},
		},
	}

	router := NewRouter(registry, cfg)

	// Test: Request claude-3-5-sonnet with thinking suffix
	req := RoutingRequest{
		RequestedModel:      "claude-3-5-sonnet(thinking:foo)",
		PreferLocalProvider: true,
		ForceModelMapping:   false,
	}

	decision := router.ResolveV2(req)

	// Assert: Thinking suffix should be preserved in resolved model
	assert.Equal(t, RouteTypeModelMapping, decision.RouteType)
	assert.Equal(t, "claude-local(thinking:foo)", decision.ResolvedModel)
	assert.Equal(t, "anthropic", decision.ProviderName)
}

// MockProvider is a mock implementation of Provider for testing
type MockProvider struct {
	name            string
	providerType    ProviderType
	supportedModels []string
	available       bool
	priority        int
}

func (m *MockProvider) Name() string {
	return m.name
}

func (m *MockProvider) Type() ProviderType {
	if m.providerType == "" {
		return ProviderTypeOAuth
	}
	return m.providerType
}

func (m *MockProvider) SupportsModel(model string) bool {
	for _, supported := range m.supportedModels {
		if supported == model {
			return true
		}
	}
	return false
}

func (m *MockProvider) Available(model string) bool {
	return m.available
}

func (m *MockProvider) Priority() int {
	return m.priority
}

func (m *MockProvider) Execute(ctx context.Context, model string, req executor.Request) (executor.Response, error) {
	return executor.Response{}, nil
}

func (m *MockProvider) ExecuteStream(ctx context.Context, model string, req executor.Request) (<-chan executor.StreamChunk, error) {
	return nil, nil
}
