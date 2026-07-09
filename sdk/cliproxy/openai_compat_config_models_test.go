package cliproxy

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

func TestBuildOpenAICompatibilityConfigModels_InputModalities(t *testing.T) {
	compat := &config.OpenAICompatibility{
		Name: "mimo",
		Models: []config.OpenAICompatibilityModel{
			{
				Name:            "upstream-vision",
				Alias:           "mimo-v2.5-pro",
				InputModalities: []string{"TEXT", "image", "image"},
			},
			{
				Name:  "upstream-image",
				Alias: "compat-image",
				Image: true,
			},
		},
	}

	models := buildOpenAICompatibilityConfigModels(compat)
	if len(models) != 2 {
		t.Fatalf("model count = %d, want 2", len(models))
	}

	var vision *ModelInfo
	var imageModel *ModelInfo
	for _, model := range models {
		if model == nil {
			continue
		}
		switch model.ID {
		case "mimo-v2.5-pro":
			vision = model
		case "compat-image":
			imageModel = model
		}
	}
	if vision == nil {
		t.Fatal("expected vision model")
	}
	if got := joinModalities(vision.SupportedInputModalities); got != "text,image" {
		t.Fatalf("SupportedInputModalities = %q, want text,image", got)
	}
	if imageModel == nil {
		t.Fatal("expected image model")
	}
	if imageModel.Type != registry.OpenAIImageModelType {
		t.Fatalf("image model type = %q, want %q", imageModel.Type, registry.OpenAIImageModelType)
	}
	if len(imageModel.SupportedInputModalities) != 0 {
		t.Fatalf("image model input modalities = %+v, want none", imageModel.SupportedInputModalities)
	}
}

func TestBuildOpenAICompatibilityConfigModels_ThinkingExplicit(t *testing.T) {
	models := buildOpenAICompatibilityConfigModels(&config.OpenAICompatibility{
		Name: "compat",
		Models: []config.OpenAICompatibilityModel{
			{
				Name:     "explicit-thinking",
				Thinking: &registry.ThinkingSupport{Levels: []string{"xhigh"}},
			},
			{
				Name: "default-thinking",
			},
		},
	})
	if len(models) != 2 {
		t.Fatalf("model count = %d, want 2", len(models))
	}
	if !models[0].ThinkingExplicit {
		t.Fatal("explicit model ThinkingExplicit = false, want true")
	}
	if models[1].ThinkingExplicit {
		t.Fatal("default model ThinkingExplicit = true, want false")
	}
}

func TestBuildClaudeConfigModels_UsesConfiguredThinking(t *testing.T) {
	models := buildClaudeConfigModels(&config.ClaudeKey{
		Models: []config.ClaudeModel{
			{
				Name: "claude-opus-4-6",
				Thinking: &registry.ThinkingSupport{
					Levels: []string{"high", "medium", "low", "minimal", "none", "auto"},
				},
			},
		},
	})
	if len(models) != 1 {
		t.Fatalf("model count = %d, want 1", len(models))
	}
	if models[0].Thinking == nil {
		t.Fatal("Thinking = nil, want configured support")
	}
	if !models[0].ThinkingExplicit {
		t.Fatal("ThinkingExplicit = false, want true for configured support")
	}
	if got := joinModalities(models[0].Thinking.Levels); got != "high,medium,low,minimal,none,auto" {
		t.Fatalf("Thinking.Levels = %q, want configured levels", got)
	}
}

func TestBuildClaudeConfigModels_InferredThinkingIsNotExplicit(t *testing.T) {
	models := buildClaudeConfigModels(&config.ClaudeKey{
		Models: []config.ClaudeModel{{Name: "claude-sonnet-4-6"}},
	})
	if len(models) != 1 {
		t.Fatalf("model count = %d, want 1", len(models))
	}
	if models[0].Thinking == nil {
		t.Fatal("Thinking = nil, want static inferred support")
	}
	if models[0].ThinkingExplicit {
		t.Fatal("ThinkingExplicit = true, want false for static inferred support")
	}
}

func TestBuildAPIKeyConfigModels_UseConfiguredThinking(t *testing.T) {
	tests := []struct {
		name   string
		models []*ModelInfo
	}{
		{
			name: "gemini",
			models: buildGeminiConfigModels(&config.GeminiKey{Models: []config.GeminiModel{{
				Name:     "gemini-test",
				Thinking: &registry.ThinkingSupport{Levels: []string{"xhigh"}},
			}}}),
		},
		{
			name: "codex",
			models: buildCodexConfigModels(&config.CodexKey{Models: []config.CodexModel{{
				Name:     "codex-test",
				Thinking: &registry.ThinkingSupport{Levels: []string{"xhigh"}},
			}}}),
		},
		{
			name: "claude",
			models: buildClaudeConfigModels(&config.ClaudeKey{Models: []config.ClaudeModel{{
				Name:     "claude-test",
				Thinking: &registry.ThinkingSupport{Levels: []string{"xhigh"}},
			}}}),
		},
		{
			name: "vertex",
			models: buildVertexCompatConfigModels(&config.VertexCompatKey{Models: []config.VertexCompatModel{{
				Name:     "vertex-test",
				Thinking: &registry.ThinkingSupport{Levels: []string{"xhigh"}},
			}}}),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			model := findModelByPrefix(tt.models, tt.name+"-test")
			if model == nil {
				t.Fatalf("configured model %q not found in %+v", tt.name+"-test", tt.models)
			}
			if model.Thinking == nil {
				t.Fatal("Thinking = nil, want configured support")
			}
			if !model.ThinkingExplicit {
				t.Fatal("ThinkingExplicit = false, want true for configured support")
			}
			if got := joinModalities(model.Thinking.Levels); got != "xhigh" {
				t.Fatalf("Thinking.Levels = %q, want xhigh", got)
			}
		})
	}
}

func findModelByPrefix(models []*ModelInfo, prefix string) *ModelInfo {
	for _, model := range models {
		if model != nil && len(model.ID) >= len(prefix) && model.ID[:len(prefix)] == prefix {
			return model
		}
	}
	return nil
}

func joinModalities(modalities []string) string {
	if len(modalities) == 0 {
		return ""
	}
	out := modalities[0]
	for i := 1; i < len(modalities); i++ {
		out += "," + modalities[i]
	}
	return out
}
