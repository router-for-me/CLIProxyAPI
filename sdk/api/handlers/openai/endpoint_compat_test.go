package openai

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
)

func TestResolveEndpointOverride_StripsThinkingSuffix(t *testing.T) {
	const clientID = "test-endpoint-compat-suffix"
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(clientID, "github-copilot", []*registry.ModelInfo{
		{
			ID:                 "test-gemini-chat-only",
			SupportedEndpoints: []string{openAIChatEndpoint},
		},
	})
	t.Cleanup(func() {
		reg.UnregisterClient(clientID)
	})

	override, ok := resolveEndpointOverride("test-gemini-chat-only(high)", openAIResponsesEndpoint)
	if !ok {
		t.Fatalf("expected endpoint override to be resolved")
	}
	if override != openAIChatEndpoint {
		t.Fatalf("override endpoint = %q, want %q", override, openAIChatEndpoint)
	}
}
