package registry

import (
	"testing"
)

func TestGetStaticModelDefinitionsByChannel(t *testing.T) {
	channels := []string{
		"claude", "gemini", "vertex", "gemini-cli", "aistudio", "codex",
		"qwen", "iflow", "github-copilot", "kiro", "amazonq", "cursor",
		"minimax", "roo", "kilo", "kilocode", "deepseek", "groq", "mistral",
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
<<<<<<< HEAD
=======
	foundGPT5CodexVariants := map[string]bool{
		"gpt-5-codex-low":    false,
		"gpt-5-codex-medium": false,
		"gpt-5-codex-high":   false,
	}
>>>>>>> archive/pr-234-head-20260223
	for _, m := range models {
		if m.ID == "gpt-5" {
			foundGPT5 = true
			break
		}
	}
<<<<<<< HEAD
	if !foundGPT5 {
		t.Error("expected gpt-5 model in GitHub Copilot models")
	}
=======
	for _, m := range models {
		if _, ok := foundGPT5CodexVariants[m.ID]; ok {
			foundGPT5CodexVariants[m.ID] = true
		}
	}
	if !foundGPT5 {
		t.Error("expected gpt-5 model in GitHub Copilot models")
	}
	for modelID, found := range foundGPT5CodexVariants {
		if !found {
			t.Errorf("expected %s model in GitHub Copilot models", modelID)
		}
	}
>>>>>>> archive/pr-234-head-20260223

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

func TestGetQwenModels_IncludesQwen35Alias(t *testing.T) {
	models := GetQwenModels()
	foundAlias := false
	for _, model := range models {
		if model.ID == "qwen3.5" {
			foundAlias = true
			if model.DisplayName == "" {
				t.Fatal("expected qwen3.5 to expose display name")
			}
			break
		}
	}
	if !foundAlias {
		t.Fatal("expected qwen3.5 in Qwen model definitions")
	}
	if LookupStaticModelInfo("qwen3.5") == nil {
		t.Fatal("expected static lookup for qwen3.5")
	}
}
<<<<<<< HEAD
=======

func TestGetOpenAIModels_GPT51Metadata(t *testing.T) {
	models := GetOpenAIModels()
	for _, model := range models {
		if model.ID != "gpt-5.1" {
			continue
		}
		if model.DisplayName != "GPT 5.1" {
			t.Fatalf("expected gpt-5.1 display name %q, got %q", "GPT 5.1", model.DisplayName)
		}
		if model.Description == "" || model.Description == "Stable version of GPT 5, The best model for coding and agentic tasks across domains." {
			t.Fatalf("expected gpt-5.1 description to explicitly mention version 5.1, got %q", model.Description)
		}
		return
	}
	t.Fatal("expected gpt-5.1 in OpenAI model definitions")
}
>>>>>>> archive/pr-234-head-20260223
