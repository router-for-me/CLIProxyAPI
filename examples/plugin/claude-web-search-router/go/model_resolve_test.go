package main

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestResolveCodexWebSearchTargetModelNeverUsesClaudeName(t *testing.T) {
	got := resolveCodexWebSearchTargetModel("")
	if got != defaultCodexWebSearchModel {
		t.Fatalf("empty config = %q, want %q", got, defaultCodexWebSearchModel)
	}
	if got := resolveCodexWebSearchTargetModel("gpt-5.5"); got != "gpt-5.5" {
		t.Fatalf("configured = %q", got)
	}
}

func TestResolveXAIWebSearchTargetModelNeverUsesClaudeName(t *testing.T) {
	got := resolveXAIWebSearchTargetModel("")
	if got != defaultXAIWebSearchModel {
		t.Fatalf("empty config = %q, want %q", got, defaultXAIWebSearchModel)
	}
}

func TestResolveAntigravityWebSearchTargetModelConfiguredWins(t *testing.T) {
	if got := resolveAntigravityWebSearchTargetModel("my-gemini", "claude-sonnet-4-6"); got != "my-gemini" {
		t.Fatalf("configured = %q", got)
	}
}

func TestResolveAntigravityWebSearchTargetModelFromRegistry(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	const clientID = "test-claude-web-search-router-antigravity"
	reg.RegisterClient(clientID, "antigravity", []*registry.ModelInfo{
		{ID: "gemini-web-search-test", SupportsWebSearch: true},
	})
	t.Cleanup(func() { reg.UnregisterClient(clientID) })
	got := resolveAntigravityWebSearchTargetModel("", "claude-sonnet-4-6")
	if got != "gemini-web-search-test" {
		t.Fatalf("fallback = %q, want gemini-web-search-test", got)
	}
}
