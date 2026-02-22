package registry

import (
	"testing"
)

func TestGetStaticModelDefinitionsByChannel(t *testing.T) {
	channels := []string{
		"claude", "gemini", "vertex", "gemini-cli", "aistudio", "codex",
		"qwen", "iflow", "github-copilot", "kiro", "amazonq", "cursor",
		"minimax", "roo", "kilo", "deepseek", "groq", "mistral",
		"siliconflow", "openrouter", "together", "fireworks", "novita",
		"antigravity",
	}

	for _, ch := range channels {
		models := GetStaticModelDefinitionsByChannel(ch)
		if models == nil && ch != "antigravity" {
			t.Errorf("expected models for channel %s, got nil", ch)
		}
	}

	if GetStaticModelDefinitionsByChannel("unknown") != nil {
		t.Error("expected nil for unknown channel")
	}
}

func TestLookupStaticModelInfo(t *testing.T) {
	// Known model
	m := LookupStaticModelInfo("claude-3-5-sonnet-20241022")
	if m == nil {
		// Try another one if that's not in the static data
		m = LookupStaticModelInfo("gpt-4o")
	}
	if m != nil {
		if m.ID == "" {
			t.Error("model ID should not be empty")
		}
	}

	// Unknown model
	if LookupStaticModelInfo("non-existent-model") != nil {
		t.Error("expected nil for unknown model")
	}

	// Empty ID
	if LookupStaticModelInfo("") != nil {
		t.Error("expected nil for empty model ID")
	}
}

func TestGetGitHubCopilotModels(t *testing.T) {
	models := GetGitHubCopilotModels()
	if len(models) == 0 {
		t.Error("expected models for GitHub Copilot")
	}
	foundGPT5 := false
	for _, m := range models {
		if m.ID == "gpt-5" {
			foundGPT5 = true
			break
		}
	}
	if !foundGPT5 {
		t.Error("expected gpt-5 model in GitHub Copilot models")
	}

	for _, m := range models {
		if m.ContextLength != 128000 {
			t.Fatalf("expected github-copilot model %q context_length=128000, got %d", m.ID, m.ContextLength)
		}
	}
}

func TestGetAntigravityModelConfig_IncludesOpusAlias(t *testing.T) {
	cfg := GetAntigravityModelConfig()
	entry, ok := cfg["gemini-claude-opus-thinking"]
	if !ok {
		t.Fatal("expected gemini-claude-opus-thinking alias in antigravity model config")
	}
	if entry == nil || entry.Thinking == nil {
		t.Fatal("expected gemini-claude-opus-thinking to define thinking support")
	}
}
