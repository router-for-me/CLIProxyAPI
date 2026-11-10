package auth

import (
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestAttachResolvedAPIKeyModelInfoUsesOpenRouterReasoningEfforts(t *testing.T) {
	restore := registry.SetOpenRouterModelsForTest(map[string]registry.OpenRouterModel{
		"anthropic/claude-opus-5.5": {ReasoningEfforts: []string{"max", "xhigh", "high", "medium", "low"}},
		"openai/gpt-6.1-terra":      {ReasoningEfforts: []string{"max", "xhigh", "high", "medium", "low", "none"}},
	})
	defer restore()

	newManager := func(baseURL string) (*Manager, *Auth) {
		manager := NewManager(nil, nil, nil)
		manager.SetConfig(&internalconfig.Config{OpenAICompatibility: []internalconfig.OpenAICompatibility{{
			Name:    "OpenRouter",
			BaseURL: baseURL,
			Models: []internalconfig.OpenAICompatibilityModel{
				{Name: "anthropic/claude-opus-5.5", Alias: "api/claude-opus-5-5"},
				{Name: "openai/gpt-6.1-terra", Alias: "gpt-6.1-terra", Thinking: &registry.ThinkingSupport{Levels: []string{"low", "high"}}},
				{Name: "vendor/unlisted", Alias: "unlisted"},
			},
		}}})
		auth := &Auth{
			ID:       "auth-openrouter",
			Provider: "openai-compatibility:openrouter",
			Attributes: map[string]string{
				AttributeSource: "config:openrouter[0]",
				"base_url":      baseURL,
				"compat_name":   "OpenRouter",
				"provider_key":  "openai-compatibility:openrouter",
			},
		}
		registerCapabilityTestAuth(t, manager, auth)
		return manager, auth
	}

	manager, auth := newManager("https://openrouter.ai/api/v1")
	assertResolvedThinkingLevels(t, manager.attachResolvedAPIKeyModelInfo(cliproxyexecutor.Request{}, auth, "api/claude-opus-5-5", "anthropic/claude-opus-5.5"),
		"low", "medium", "high", "xhigh", "max")
	// A thinking suffix on the upstream model doesn't stop the catalog lookup.
	assertResolvedThinkingLevels(t, manager.attachResolvedAPIKeyModelInfo(cliproxyexecutor.Request{}, auth, "api/claude-opus-5-5", "anthropic/claude-opus-5.5(high)"),
		"low", "medium", "high", "xhigh", "max")
	// Configured thinking wins over OpenRouter's efforts, but the lookup that keeps the catalog fresh still runs.
	var lookedUp []string
	realLookup := lookupOpenRouterModel
	lookupOpenRouterModel = func(providerKey, modelID string) (registry.OpenRouterModel, bool) {
		lookedUp = append(lookedUp, modelID)
		return realLookup(providerKey, modelID)
	}
	assertResolvedThinkingLevels(t, manager.attachResolvedAPIKeyModelInfo(cliproxyexecutor.Request{}, auth, "gpt-6.1-terra", "openai/gpt-6.1-terra"),
		"low", "high")
	lookupOpenRouterModel = realLookup
	if len(lookedUp) != 1 || lookedUp[0] != "openai/gpt-6.1-terra" {
		t.Fatalf("lookups = %v, want the configured-thinking model looked up", lookedUp)
	}
	// Models OpenRouter doesn't list keep the default levels.
	assertResolvedThinkingLevels(t, manager.attachResolvedAPIKeyModelInfo(cliproxyexecutor.Request{}, auth, "unlisted", "vendor/unlisted"),
		"low", "medium", "high")

	// OpenRouter's levels are applied to a copy per request; the stored capability keeps the default.
	info, ok := lookupAPIKeyModelCapability(manager.loadAPIKeyModelRouting(), auth, "api/claude-opus-5-5", "anthropic/claude-opus-5.5")
	if !ok || info == nil || len(info.Thinking.Levels) != 3 {
		t.Fatalf("stored capability was changed: %+v", info)
	}

	other, otherAuth := newManager("https://api.example.com/v1")
	assertResolvedThinkingLevels(t, other.attachResolvedAPIKeyModelInfo(cliproxyexecutor.Request{}, otherAuth, "api/claude-opus-5-5", "anthropic/claude-opus-5.5"),
		"low", "medium", "high")
}
