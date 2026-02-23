package openai

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/registry"
)

func TestResolveEndpointOverride_UsesRegisteredResponsesOnlyModel(t *testing.T) {
	clientID := "endpoint-compat-test-client-1"
	registry.GetGlobalRegistry().RegisterClient(clientID, "codex", []*registry.ModelInfo{{
		ID:                 "gpt-5.1-codex",
		SupportedEndpoints: []string{openAIResponsesEndpoint},
	}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(clientID)
	})

	override, ok := resolveEndpointOverride("gpt-5.1-codex", openAIChatEndpoint)
	if !ok {
		t.Fatal("expected endpoint override")
	}
	if override != openAIResponsesEndpoint {
		t.Fatalf("override = %q, want %q", override, openAIResponsesEndpoint)
	}
}

func TestResolveEndpointOverride_UsesProviderPinnedSuffixedModel(t *testing.T) {
	clientID := "endpoint-compat-test-client-2"
	registry.GetGlobalRegistry().RegisterClient(clientID, "codex", []*registry.ModelInfo{{
		ID:                 "gpt-5.1-codex",
		SupportedEndpoints: []string{openAIResponsesEndpoint},
	}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(clientID)
	})

	override, ok := resolveEndpointOverride("codex/gpt-5.1-codex(high)", openAIChatEndpoint)
	if !ok {
		t.Fatal("expected endpoint override for provider-pinned model with suffix")
	}
	if override != openAIResponsesEndpoint {
		t.Fatalf("override = %q, want %q", override, openAIResponsesEndpoint)
	}
}
