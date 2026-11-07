package cliproxy

import (
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

func TestBuildOpenAICompatibilityConfigModels_InputModalities(t *testing.T) {
	compat := &config.OpenAICompatibility{
		Name: "mimo",
		Models: []config.OpenAICompatibilityModel{
			{
				Name:            "upstream-vision",
				Alias:           "mimo-v2.5-pro",
				DisplayName:     "Mimo Vision",
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
	if vision.DisplayName != "Mimo Vision" {
		t.Fatalf("DisplayName = %q, want Mimo Vision", vision.DisplayName)
	}
	if got := joinModalities(vision.SupportedInputModalities); got != "text,image" {
		t.Fatalf("SupportedInputModalities = %q, want text,image", got)
	}
	if imageModel == nil {
		t.Fatal("expected image model")
	}
	if imageModel.DisplayName != "compat-image" {
		t.Fatalf("image DisplayName = %q, want compat-image", imageModel.DisplayName)
	}
	if imageModel.Type != registry.OpenAIImageModelType {
		t.Fatalf("image model type = %q, want %q", imageModel.Type, registry.OpenAIImageModelType)
	}
	if len(imageModel.SupportedInputModalities) != 0 {
		t.Fatalf("image model input modalities = %+v, want none", imageModel.SupportedInputModalities)
	}
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

func TestBuildOpenAICompatibilityConfigModels_OpenRouterPublishedReasoningAndOwner(t *testing.T) {
	restore := registry.SetOpenRouterModelsForTest(map[string]registry.OpenRouterModel{
		"anthropic/claude-opus-5.5":  {ContextLength: 1000000, MaxCompletionTokens: 128000, ReasoningEfforts: []string{"max", "xhigh", "high", "medium", "low"}},
		"anthropic/claude-haiku-5.5": {ReasoningEfforts: []string{"max", "xhigh", "high", "medium", "low", "none"}},
		"openai/gpt-6.1-terra":       {ReasoningEfforts: []string{"max", "high"}},
	})
	defer restore()

	openRouter := &config.OpenAICompatibility{
		Name:    "OpenRouter",
		BaseURL: "https://openrouter.ai/api/v1",
		Models: []config.OpenAICompatibilityModel{
			{Name: "anthropic/claude-opus-5.5", Alias: "api/claude-opus-5-5"},
			{Name: "anthropic/claude-haiku-5.5"},
			{Name: "openai/gpt-6.1-terra", Thinking: &registry.ThinkingSupport{Levels: []string{"low"}}},
			{Name: "~openai/gpt-sol-latest"},
			{Name: "vendor/unlisted"},
		},
	}
	byID := make(map[string]*ModelInfo)
	for _, model := range buildOpenAICompatibilityConfigModels(openRouter) {
		byID[model.ID] = model
	}

	opus := byID["api/claude-opus-5-5"]
	if opus == nil || opus.Thinking == nil {
		t.Fatalf("opus = %+v", opus)
	}
	if got := strings.Join(opus.Thinking.Levels, ","); got != "low,medium,high,xhigh,max" || opus.Thinking.ZeroAllowed {
		t.Fatalf("opus levels = %s zero=%v, want OpenRouter's efforts low..max without none", got, opus.Thinking.ZeroAllowed)
	}
	if opus.OwnedBy != "anthropic" || opus.ContextLength != 1000000 || opus.MaxCompletionTokens != 128000 {
		t.Fatalf("opus owner/limits = %q %d %d", opus.OwnedBy, opus.ContextLength, opus.MaxCompletionTokens)
	}
	if haiku := byID["anthropic/claude-haiku-5.5"]; haiku == nil || haiku.Thinking == nil || !haiku.Thinking.ZeroAllowed {
		t.Fatalf("haiku publishes none, so thinking can be turned off: %+v", haiku)
	}
	if terra := byID["openai/gpt-6.1-terra"]; terra == nil || strings.Join(terra.Thinking.Levels, ",") != "low" {
		t.Fatalf("configured thinking must win over OpenRouter's efforts: %+v", terra)
	}
	if latest := byID["~openai/gpt-sol-latest"]; latest == nil || latest.OwnedBy != "openai" {
		t.Fatalf("~vendor ids report the vendor: %+v", latest)
	}
	if unlisted := byID["vendor/unlisted"]; unlisted == nil || strings.Join(unlisted.Thinking.Levels, ",") != "low,medium,high" || unlisted.OwnedBy != "vendor" {
		t.Fatalf("a model OpenRouter doesn't list keeps the default levels: %+v", unlisted)
	}

	other := &config.OpenAICompatibility{
		Name:    "Other",
		BaseURL: "https://api.example.com/v1",
		Models:  []config.OpenAICompatibilityModel{{Name: "anthropic/claude-opus-5.5"}},
	}
	models := buildOpenAICompatibilityConfigModels(other)
	if len(models) != 1 || models[0].OwnedBy != "Other" || strings.Join(models[0].Thinking.Levels, ",") != "low,medium,high" {
		t.Fatalf("entries not pointing at OpenRouter are unchanged: %+v", models)
	}
}
