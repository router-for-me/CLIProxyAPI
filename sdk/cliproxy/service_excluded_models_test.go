package cliproxy

import (
	"strings"
	"testing"

	internalregistry "github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestRegisterModelsForAuth_UsesPreMergedExcludedModelsAttribute(t *testing.T) {
	service := &Service{
		cfg: &config.Config{
			OAuthExcludedModels: map[string][]string{
				"gemini-cli": {"gemini-2.5-pro"},
			},
		},
	}
	auth := &coreauth.Auth{
		ID:       "auth-gemini-cli",
		Provider: "gemini-cli",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind":       "oauth",
			"excluded_models": "gemini-2.5-flash",
		},
	}

	registry := GlobalModelRegistry()
	registry.UnregisterClient(auth.ID)
	t.Cleanup(func() {
		registry.UnregisterClient(auth.ID)
	})

	service.registerModelsForAuth(auth)

	models := registry.GetAvailableModelsByProvider("gemini-cli")
	if len(models) == 0 {
		t.Fatal("expected gemini-cli models to be registered")
	}

	for _, model := range models {
		if model == nil {
			continue
		}
		modelID := strings.TrimSpace(model.ID)
		if strings.EqualFold(modelID, "gemini-2.5-flash") {
			t.Fatalf("expected model %q to be excluded by auth attribute", modelID)
		}
	}

	seenGlobalExcluded := false
	for _, model := range models {
		if model == nil {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(model.ID), "gemini-2.5-pro") {
			seenGlobalExcluded = true
			break
		}
	}
	if !seenGlobalExcluded {
		t.Fatal("expected global excluded model to be present when attribute override is set")
	}
}

func TestRegisterModelsForAuth_OpenAICompatibilityImageModelType(t *testing.T) {
	service := &Service{
		cfg: &config.Config{
			OpenAICompatibility: []config.OpenAICompatibility{
				{
					Name:    "images",
					BaseURL: "https://example.com/v1",
					Models: []config.OpenAICompatibilityModel{
						{Name: "upstream-image", Alias: "compat-image", Image: true},
						{Name: "upstream-chat", Alias: "compat-chat"},
					},
				},
			},
		},
	}
	auth := &coreauth.Auth{
		ID:       "auth-openai-compat-image",
		Provider: "openai-compatibility",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind":    "api_key",
			"compat_name":  "images",
			"provider_key": "images",
		},
	}

	modelRegistry := internalregistry.GetGlobalRegistry()
	modelRegistry.UnregisterClient(auth.ID)
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(auth.ID)
	})

	service.registerModelsForAuth(auth)

	models := modelRegistry.GetModelsForClient(auth.ID)
	var imageModel *internalregistry.ModelInfo
	var chatModel *internalregistry.ModelInfo
	for _, model := range models {
		if model == nil {
			continue
		}
		switch strings.TrimSpace(model.ID) {
		case "compat-image":
			imageModel = model
		case "compat-chat":
			chatModel = model
		}
	}
	if imageModel == nil {
		t.Fatal("expected compat-image to be registered")
	}
	if imageModel.Type != internalregistry.OpenAIImageModelType {
		t.Fatalf("image model type = %q, want %q", imageModel.Type, internalregistry.OpenAIImageModelType)
	}
	if imageModel.Thinking != nil {
		t.Fatalf("image model thinking = %+v, want nil", imageModel.Thinking)
	}
	if chatModel == nil {
		t.Fatal("expected compat-chat to be registered")
	}
	if chatModel.Type != "openai-compatibility" {
		t.Fatalf("chat model type = %q, want openai-compatibility", chatModel.Type)
	}
	if chatModel.Thinking == nil {
		t.Fatal("expected chat model to keep default thinking support")
	}
}

func TestBuildOpenAICompatibilityConfigModels_DeepSeekOfficialThinkingOverride(t *testing.T) {
	compat := &config.OpenAICompatibility{
		Name:    "deepseek-official",
		Kind:    "deepseek",
		BaseURL: "https://api.deepseek.com/v1",
		Models: []config.OpenAICompatibilityModel{
			{Name: "deepseek-v4-pro", Alias: "deepseek-v4-pro"},
			{Name: "deepseek-v4-flash", Alias: "claude-sonnet-4-5"},
			{Name: "other-openai-model", Alias: "other-openai-model"},
		},
	}

	models := buildOpenAICompatibilityConfigModels(compat)
	if len(models) != 3 {
		t.Fatalf("model count = %d, want 3", len(models))
	}

	byID := make(map[string]*internalregistry.ModelInfo, len(models))
	for _, model := range models {
		if model != nil {
			byID[model.ID] = model
		}
	}

	for _, modelID := range []string{"deepseek-v4-pro", "claude-sonnet-4-5"} {
		model := byID[modelID]
		if model == nil || model.Thinking == nil {
			t.Fatalf("expected thinking support for %q", modelID)
		}
		want := []string{"low", "medium", "high", "xhigh", "max"}
		if strings.Join(model.Thinking.Levels, ",") != strings.Join(want, ",") {
			t.Fatalf("%s thinking levels = %v, want %v", modelID, model.Thinking.Levels, want)
		}
	}

	other := byID["other-openai-model"]
	if other == nil || other.Thinking == nil {
		t.Fatal("expected default thinking support for other-openai-model")
	}
	wantDefault := []string{"low", "medium", "high"}
	if strings.Join(other.Thinking.Levels, ",") != strings.Join(wantDefault, ",") {
		t.Fatalf("other-openai-model thinking levels = %v, want %v", other.Thinking.Levels, wantDefault)
	}
}
