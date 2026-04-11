package registry

import "testing"

func TestGitHubCopilotGeminiModelsAreChatOnly(t *testing.T) {
	models := GetGitHubCopilotModels()
	required := map[string]bool{
		"gemini-2.5-pro":         false,
		"gemini-3-pro-preview":   false,
		"gemini-3.1-pro-preview": false,
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

func TestGitHubCopilotClaudeModelsSupportMessages(t *testing.T) {
	models := GetGitHubCopilotModels()
	required := map[string]bool{
		"claude-haiku-4.5":  false,
		"claude-opus-4.1":   false,
		"claude-opus-4.5":   false,
		"claude-opus-4.6":   false,
		"claude-sonnet-4":   false,
		"claude-sonnet-4.5": false,
		"claude-sonnet-4.6": false,
	}

	for _, model := range models {
		if _, ok := required[model.ID]; !ok {
			continue
		}
		required[model.ID] = true
		if !containsString(model.SupportedEndpoints, "/chat/completions") {
			t.Fatalf("model %q supported endpoints = %v, missing /chat/completions", model.ID, model.SupportedEndpoints)
		}
		if !containsString(model.SupportedEndpoints, "/messages") {
			t.Fatalf("model %q supported endpoints = %v, missing /messages", model.ID, model.SupportedEndpoints)
		}
	}

	for modelID, found := range required {
		if !found {
			t.Fatalf("expected GitHub Copilot model %q in definitions", modelID)
		}
	}
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
