package openai

import (
	"sort"
	"testing"

	"github.com/kooshapari/CLIProxyAPI/v7/pkg/llmproxy/registry"
)

func TestResolveEndpointOverride_IflowSupportsChatOnlyForResponses(t *testing.T) {
	registry.GetGlobalRegistry().RegisterClient("endpoint-compat-iflow-chat-only", "iflow", []*registry.ModelInfo{{
		ID:                 "minimax-m2.5-chat-only-test",
		SupportedEndpoints: []string{"/chat/completions"},
	}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient("endpoint-compat-iflow-chat-only")
	})

	overrideEndpoint, ok := resolveEndpointOverride("minimax-m2.5-chat-only-test", "/responses")
	if !ok {
		t.Fatal("expected endpoint override for /responses on chat-only model")
	}
	if overrideEndpoint != "/chat/completions" {
		t.Fatalf("override = %q, want %q", overrideEndpoint, "/chat/completions")
	}
}

func TestResolveEndpointOverride_IflowSupportsBothEndpointsNoOverride(t *testing.T) {
	registry.GetGlobalRegistry().RegisterClient("endpoint-compat-iflow-both", "iflow", []*registry.ModelInfo{{
		ID:                 "minimax-m2.5-both-endpoints-test",
		SupportedEndpoints: []string{"/chat/completions", "/responses"},
	}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient("endpoint-compat-iflow-both")
	})

	overrideEndpoint, ok := resolveEndpointOverride("minimax-m2.5-both-endpoints-test", "/responses")
	if ok {
		t.Fatalf("expected no endpoint override when /responses already supported, got %q", overrideEndpoint)
	}
}

func TestResolveEndpointOverride_RespectsHandlerEndpointDirectionality(t *testing.T) {
	registry.GetGlobalRegistry().RegisterClient("endpoint-compat-iflow-direction", "iflow", []*registry.ModelInfo{{
		ID:                 "minimax-m2.5-directionality-test",
		SupportedEndpoints: []string{"/chat/completions"},
	}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient("endpoint-compat-iflow-directionality")
	})

	_, ok := resolveEndpointOverride("minimax-m2.5-directionality-test", "/chat/completions")
	if ok {
		t.Fatalf("expected no override for /chat/completions when provider supports chat")
	}
}

func TestEndpointCompatProviderCandidatesAreSortedByPrecedence(t *testing.T) {
	modelID := "directional-provider-test"
	registry.GetGlobalRegistry().RegisterClient("endpoint-compat-provider-iflow", "iflow", []*registry.ModelInfo{{
		ID:                 modelID,
		SupportedEndpoints: []string{"/chat/completions"},
	}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient("endpoint-compat-provider-iflow")
	})

	reg := registry.GetGlobalRegistry().GetModelProviders(modelID)
	sort.Strings(reg)
	if len(reg) != 1 || reg[0] != "iflow" {
		t.Fatalf("expected single iflow registration, got %v", reg)
	}

	overrideEndpoint, ok := resolveEndpointOverride(modelID, "/responses")
	if !ok || overrideEndpoint != "/chat/completions" {
		t.Fatalf("provider-agnostic model lookup should still resolve override: ok=%v endpoint=%q", ok, overrideEndpoint)
	}
}

func TestShouldForceNonStreamingChatBridgeForMinimax(t *testing.T) {
	if !shouldForceNonStreamingChatBridge("minimax-m2.5") {
		t.Fatalf("expected minimax model to force non-stream bridge")
	}
	if !shouldForceNonStreamingChatBridge("MiniMax-M2.5") {
		t.Fatalf("expected minimax alias to force non-stream bridge")
	}
	if shouldForceNonStreamingChatBridge("gpt-4o-mini") {
		t.Fatalf("did not expect non-minimax model to force non-stream bridge")
	}
}
