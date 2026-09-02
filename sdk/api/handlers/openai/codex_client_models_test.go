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

func TestCodexClientModelsResponseClientVersionFiltering(t *testing.T) {
	modelRegistry := registry.GetGlobalRegistry()
	modelRegistry.RegisterClient("codex-version-filter-sdk-test", "openai-compatibility", []*registry.ModelInfo{
		{
			ID:          "gpt-5.6-sol",
			Object:      "model",
			OwnedBy:     "openai",
			DisplayName: "GPT-5.6-Sol",
		},
	})
	t.Cleanup(func() {
		modelRegistry.UnregisterClient("codex-version-filter-sdk-test")
	})

	base := handlers.NewBaseAPIHandlers(&config.SDKConfig{}, nil)
	handler := NewOpenAIAPIHandler(base)

	// Test with older client version 0.137.0
	respOld := handler.codexClientModelsResponse("0.137.0")
	modelsOld, ok := respOld["models"].([]map[string]any)
	if !ok {
		t.Fatalf("models type = %T, want []map[string]any", respOld["models"])
	}
	foundOldSol := false
	for _, m := range modelsOld {
		if slug, _ := m["slug"].(string); slug == "gpt-5.6-sol" {
			foundOldSol = true
			levels, _ := m["supported_reasoning_levels"].([]any)
			gotEfforts := make([]string, 0, len(levels))
			for _, rawLevel := range levels {
				level, _ := rawLevel.(map[string]any)
				effort, _ := level["effort"].(string)
				gotEfforts = append(gotEfforts, effort)
				if effort == "max" || effort == "ultra" {
					t.Fatalf("0.137.0 received unsupported reasoning effort %q", effort)
				}
			}
			if len(gotEfforts) != 4 {
				t.Fatalf("0.137.0 expected 4 legacy reasoning levels, got %#v", gotEfforts)
			}
		}
	}
	if !foundOldSol {
		t.Fatal("0.137.0 expected gpt-5.6-sol in catalog")
	}

	// Test with newer client version 0.149.1
	respNew := handler.codexClientModelsResponse("0.149.1")
	modelsNew, ok := respNew["models"].([]map[string]any)
	if !ok {
		t.Fatalf("models type = %T, want []map[string]any", respNew["models"])
	}
	hasUltra := false
	for _, m := range modelsNew {
		if slug, _ := m["slug"].(string); slug == "gpt-5.6-sol" {
			levels, _ := m["supported_reasoning_levels"].([]any)
			for _, rawLevel := range levels {
				level, _ := rawLevel.(map[string]any)
				if effort, _ := level["effort"].(string); effort == "ultra" {
					hasUltra = true
				}
			}
		}
	}
	if !hasUltra {
		t.Fatal("0.149.1 expected ultra reasoning effort to be preserved")
	}
}
