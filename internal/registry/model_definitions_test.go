package registry

import "testing"

func TestGitHubCopilotGeminiModelsAreChatOnly(t *testing.T) {
	models := GetGitHubCopilotModels()
	required := map[string]bool{
		"gemini-2.5-pro":         false,
		"gemini-3-pro-preview":   false,
		"gemini-3-flash-preview": false,
	}

	for _, model := range models {
		if _, ok := required[model.ID]; !ok {
			continue
		}
		required[model.ID] = true
		if len(model.SupportedEndpoints) != 1 || model.SupportedEndpoints[0] != "/chat/completions" {
			t.Fatalf("model %q supported endpoints = %v, want [/chat/completions]", model.ID, model.SupportedEndpoints)
		}
	}

	for modelID, found := range required {
		if !found {
			t.Fatalf("expected GitHub Copilot model %q in definitions", modelID)
		}
	}
}
