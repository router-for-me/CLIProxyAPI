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

func TestBuildOpenAICompatibilityConfigModels_InheritsStaticThinking(t *testing.T) {
	models := buildOpenAICompatibilityConfigModels(&config.OpenAICompatibility{
		Name: "compat",
		Models: []config.OpenAICompatibilityModel{
			{Name: "gpt-5.5", Alias: "static-gpt-5.5"},
			{
				Name:  "gpt-5.5",
				Alias: "explicit-gpt-5.5",
				Thinking: &internalregistry.ThinkingSupport{
					Levels: []string{"low"},
				},
			},
			{Name: "custom-upstream", Alias: "gpt-5.5"},
		},
	})
	if len(models) != 3 {
		t.Fatalf("models len = %d, want 3", len(models))
	}

	if models[0].Thinking == nil {
		t.Fatal("expected static-gpt-5.5 to inherit static thinking support")
	}
	hasXHigh := false
	for _, level := range models[0].Thinking.Levels {
		if level == "xhigh" {
			hasXHigh = true
			break
		}
	}
	if !hasXHigh {
		t.Fatalf("static-gpt-5.5 thinking levels = %v, want xhigh", models[0].Thinking.Levels)
	}

	if got := models[1].Thinking.Levels; len(got) != 1 || got[0] != "low" {
		t.Fatalf("explicit thinking levels = %v, want [low]", got)
	}

	if models[2].Thinking == nil {
		t.Fatal("expected custom-upstream alias to keep default thinking support")
	}
	for _, level := range models[2].Thinking.Levels {
		if level == "xhigh" {
			t.Fatalf("custom-upstream alias thinking levels = %v, want no inherited xhigh", models[2].Thinking.Levels)
		}
	}
}

func TestShouldRefreshAuthForModelCatalogChange(t *testing.T) {
	changedProviders := map[string]bool{"codex": true}

	if !shouldRefreshAuthForModelCatalogChange(&coreauth.Auth{Provider: "codex"}, changedProviders) {
		t.Fatal("expected direct codex auth to refresh")
	}
	if !shouldRefreshAuthForModelCatalogChange(&coreauth.Auth{
		Provider: "openai-compatibility",
		Attributes: map[string]string{
			"compat_name":  "compat",
			"provider_key": "compat",
		},
	}, changedProviders) {
		t.Fatal("expected openai-compatible auth to refresh after static catalog change")
	}
	if shouldRefreshAuthForModelCatalogChange(&coreauth.Auth{Provider: "gemini"}, changedProviders) {
		t.Fatal("did not expect unrelated provider auth to refresh")
	}
	if shouldRefreshAuthForModelCatalogChange(&coreauth.Auth{Provider: "openai-compatibility"}, nil) {
		t.Fatal("did not expect refresh without changed providers")
	}
}
