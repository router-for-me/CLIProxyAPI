package util

import (
	"reflect"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/registry"
)

func TestResolveProviderPinnedModel(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("test-pinned-openai", "openai", []*registry.ModelInfo{{ID: "gpt-5.1"}})
	reg.RegisterClient("test-pinned-copilot", "github-copilot", []*registry.ModelInfo{{ID: "gpt-5.1"}})
	t.Cleanup(func() {
		reg.UnregisterClient("test-pinned-openai")
		reg.UnregisterClient("test-pinned-copilot")
	})

	provider, model, ok := ResolveProviderPinnedModel("github-copilot/gpt-5.1")
	if !ok {
		t.Fatal("expected github-copilot/gpt-5.1 to resolve as provider-pinned model")
	}
	if provider != "github-copilot" || model != "gpt-5.1" {
		t.Fatalf("got provider=%q model=%q, want provider=%q model=%q", provider, model, "github-copilot", "gpt-5.1")
	}

	if _, _, ok := ResolveProviderPinnedModel("unknown/gpt-5.1"); ok {
		t.Fatal("expected unknown/gpt-5.1 not to resolve")
	}
}

func TestGetProviderName_ProviderPinnedModel(t *testing.T) {
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient("test-provider-openai", "openai", []*registry.ModelInfo{{ID: "gpt-5.2"}})
	reg.RegisterClient("test-provider-copilot", "github-copilot", []*registry.ModelInfo{{ID: "gpt-5.2"}})
	t.Cleanup(func() {
		reg.UnregisterClient("test-provider-openai")
		reg.UnregisterClient("test-provider-copilot")
	})

	got := GetProviderName("github-copilot/gpt-5.2")
	want := []string{"github-copilot"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("GetProviderName() = %v, want %v", got, want)
	}
}
