package openai

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
)

func TestCodexClientModelsResponseMultiAgentV2FollowsConfig(t *testing.T) {
	modelID := "codex-client-multi-agent-v2-test"
	clientID := "codex-client-multi-agent-v2-test-client"
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient(clientID, "openai-compatibility", []*registry.ModelInfo{{ID: modelID}})
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(clientID)
	})

	base := handlers.NewBaseAPIHandlers(&config.SDKConfig{}, nil)
	handler := NewOpenAIAPIHandler(base)
	for _, tt := range []struct {
		name    string
		enabled bool
	}{
		{name: "disabled", enabled: false},
		{name: "enabled", enabled: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			base.Cfg.CodexOptimizeMultiAgentV2 = tt.enabled
			response := handler.codexClientModelsResponse()
			models, ok := response["models"].([]map[string]any)
			if !ok {
				t.Fatalf("models type = %T, want []map[string]any", response["models"])
			}
			var entry map[string]any
			for _, model := range models {
				slug, _ := model["slug"].(string)
				if slug == modelID {
					entry = model
					break
				}
			}
			if entry == nil {
				t.Fatalf("missing synthesized model %q", modelID)
			}
			value, exists := entry["multi_agent_version"]
			if tt.enabled {
				if !exists || value != "v2" {
					t.Fatalf("multi_agent_version = %#v, want v2", value)
				}
				return
			}
			if !exists || value != nil {
				t.Fatalf("multi_agent_version = %#v, want preserved null", value)
			}
		})
	}
}
