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

func TestResolveAntigravityWebSearchTargetModelIncludesHiddenCatalogModels(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	const clientID = "test-claude-web-search-router-hidden-antigravity"
	reg.RegisterClient(clientID, "antigravity", []*registry.ModelInfo{{
		ID:                     "gemini-hidden-web-search-router-test",
		SupportsWebSearch:      true,
		HiddenFromModelCatalog: true,
	}})
	t.Cleanup(func() { reg.UnregisterClient(clientID) })

	got := resolveAntigravityWebSearchTargetModel("", "unknown-model")
	if got != "gemini-hidden-web-search-router-test" {
		t.Fatalf("fallback = %q, want hidden capable model", got)
	}
}

func TestResolveAntigravityWebSearchTargetModelRejectsMixedCapabilities(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	const modelID = "gemini-mixed-capability-web-search-router-test"
	const capableClientID = "test-claude-web-search-router-mixed-capable-antigravity"
	const incapableClientID = "test-claude-web-search-router-mixed-incapable-antigravity"
	reg.RegisterClient(capableClientID, "antigravity", []*registry.ModelInfo{{
		ID:                modelID,
		SupportsWebSearch: true,
	}})
	reg.RegisterClient(incapableClientID, "antigravity", []*registry.ModelInfo{{ID: modelID}})
	t.Cleanup(func() {
		reg.UnregisterClient(capableClientID)
		reg.UnregisterClient(incapableClientID)
	})

	if got := resolveAntigravityWebSearchTargetModel("", "unknown-model"); got != "" {
		t.Fatalf("mixed-capability fallback = %q, want no fallback", got)
	}
}

func TestResolveAntigravityWebSearchTargetModelAcceptsAllCapableFallback(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	const modelID = "gemini-all-capable-web-search-router-test"
	const firstClientID = "test-claude-web-search-router-all-capable-antigravity-1"
	const secondClientID = "test-claude-web-search-router-all-capable-antigravity-2"
	models := []*registry.ModelInfo{{ID: modelID, SupportsWebSearch: true}}
	reg.RegisterClient(firstClientID, "antigravity", models)
	reg.RegisterClient(secondClientID, "antigravity", models)
	t.Cleanup(func() {
		reg.UnregisterClient(firstClientID)
		reg.UnregisterClient(secondClientID)
	})

	if got := resolveAntigravityWebSearchTargetModel("", "unknown-model"); got != modelID {
		t.Fatalf("all-capable fallback = %q, want %q", got, modelID)
	}
}
