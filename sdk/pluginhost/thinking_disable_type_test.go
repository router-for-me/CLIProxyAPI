package pluginhost

import (
	"testing"

	internalregistry "github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

func TestRegistryModelToPluginModelKeepsDisableType(t *testing.T) {
	got := registryModelToPluginModel(&internalregistry.ModelInfo{
		ID:       "claude-sonnet-5-5",
		Thinking: &internalregistry.ThinkingSupport{Levels: []string{"low"}, DisableType: "between_tools"},
	})
	if got.Thinking == nil || got.Thinking.DisableType != "between_tools" {
		t.Fatalf("Thinking = %+v, want DisableType between_tools", got.Thinking)
	}
}
