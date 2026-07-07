package cliproxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	internalregistry "github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestRegisterModelsForAuth_UsesPreMergedExcludedModelsAttribute(t *testing.T) {
	service := &Service{
		cfg: &config.Config{
			OAuthExcludedModels: map[string][]string{
				"gemini": {"gemini-2.5-pro"},
			},
		},
	}
	auth := &coreauth.Auth{
		ID:       "auth-gemini",
		Provider: "gemini",
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

	service.registerModelsForAuth(context.Background(), auth)

	models := registry.GetAvailableModelsByProvider("gemini")
	if len(models) == 0 {
		t.Fatal("expected gemini models to be registered")
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

	service.registerModelsForAuth(context.Background(), auth)

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

func TestRegisterModelsForAuth_ConfigAliasKeepsOriginalModelRoutable(t *testing.T) {
	testCases := []struct {
		name          string
		service       *Service
		auth          *coreauth.Auth
		provider      string
		originalModel string
		aliasModel    string
	}{
		{
			name: "codex api key",
			service: &Service{cfg: &config.Config{
				CodexKey: []internalconfig.CodexKey{{
					APIKey:  "codex-key",
					BaseURL: "https://example.com",
					Models: []internalconfig.CodexModel{{
						Name:  "gpt-5.4-mini",
						Alias: "GPT-5.4 Mini",
					}},
				}},
			}},
			auth: &coreauth.Auth{
				ID:       "auth-codex-alias-route",
				Provider: "codex",
				Status:   coreauth.StatusActive,
				Attributes: map[string]string{
					"auth_kind": "api_key",
					"api_key":   "codex-key",
					"base_url":  "https://example.com",
				},
			},
			provider:      "codex",
			originalModel: "gpt-5.4-mini",
			aliasModel:    "GPT-5.4 Mini",
		},
		{
			name: "openai compatibility",
			service: &Service{cfg: &config.Config{
				OpenAICompatibility: []config.OpenAICompatibility{{
					Name:    "compat",
					BaseURL: "https://example.com/v1",
					Models: []config.OpenAICompatibilityModel{{
						Name:  "gpt-5.4-mini",
						Alias: "GPT-5.4 Mini",
					}},
				}},
			}},
			auth: &coreauth.Auth{
				ID:       "auth-openai-compat-alias-route",
				Provider: "openai-compatibility",
				Status:   coreauth.StatusActive,
				Attributes: map[string]string{
					"auth_kind":    "api_key",
					"compat_name":  "compat",
					"provider_key": "compat",
				},
			},
			provider:      "compat",
			originalModel: "gpt-5.4-mini",
			aliasModel:    "GPT-5.4 Mini",
		},
		{
			name: "codex case-distinct alias",
			service: &Service{cfg: &config.Config{
				CodexKey: []internalconfig.CodexKey{{
					APIKey:  "codex-case-key",
					BaseURL: "https://example.com",
					Models: []internalconfig.CodexModel{{
						Name:  "gpt-5",
						Alias: "GPT-5",
					}},
				}},
			}},
			auth: &coreauth.Auth{
				ID:       "auth-codex-case-alias-route",
				Provider: "codex",
				Status:   coreauth.StatusActive,
				Attributes: map[string]string{
					"auth_kind": "api_key",
					"api_key":   "codex-case-key",
					"base_url":  "https://example.com",
				},
			},
			provider:      "codex",
			originalModel: "gpt-5",
			aliasModel:    "GPT-5",
		},
		{
			name: "openai compatibility case-distinct alias",
			service: &Service{cfg: &config.Config{
				OpenAICompatibility: []config.OpenAICompatibility{{
					Name:    "compat-case",
					BaseURL: "https://example.com/v1",
					Models: []config.OpenAICompatibilityModel{{
						Name:  "gpt-5",
						Alias: "GPT-5",
					}},
				}},
			}},
			auth: &coreauth.Auth{
				ID:       "auth-openai-compat-case-alias-route",
				Provider: "openai-compatibility",
				Status:   coreauth.StatusActive,
				Attributes: map[string]string{
					"auth_kind":    "api_key",
					"compat_name":  "compat-case",
					"provider_key": "compat-case",
				},
			},
			provider:      "compat-case",
			originalModel: "gpt-5",
			aliasModel:    "GPT-5",
		},
	}

	for _, tt := range testCases {
		t.Run(tt.name, func(t *testing.T) {
			modelRegistry := internalregistry.GetGlobalRegistry()
			modelRegistry.UnregisterClient(tt.auth.ID)
			t.Cleanup(func() {
				modelRegistry.UnregisterClient(tt.auth.ID)
			})

			tt.service.registerModelsForAuth(context.Background(), tt.auth)

			if !providersContain(modelRegistry.GetModelProviders(tt.aliasModel), tt.provider) {
				t.Fatalf("alias model %q providers = %v, want %q", tt.aliasModel, modelRegistry.GetModelProviders(tt.aliasModel), tt.provider)
			}
			if !providersContain(modelRegistry.GetModelProviders(tt.originalModel), tt.provider) {
				t.Fatalf("original model %q providers = %v, want %q", tt.originalModel, modelRegistry.GetModelProviders(tt.originalModel), tt.provider)
			}
			if static := internalregistry.LookupStaticModelInfo(tt.originalModel); static != nil && static.ContextLength > 0 {
				registered := clientModelByID(modelRegistry.GetModelsForClient(tt.auth.ID), tt.originalModel)
				if registered == nil {
					t.Fatalf("registered original model %q not found", tt.originalModel)
				}
				if registered.ContextLength != static.ContextLength {
					t.Fatalf("original model %q context length = %d, want static %d", tt.originalModel, registered.ContextLength, static.ContextLength)
				}
				if registered.UserDefined {
					t.Fatalf("original model %q should preserve static metadata instead of being marked user-defined", tt.originalModel)
				}
			}
		})
	}
}

func clientModelByID(models []*internalregistry.ModelInfo, id string) *internalregistry.ModelInfo {
	for _, model := range models {
		if model != nil && model.ID == id {
			return model
		}
	}
	return nil
}

func TestRegisterModelsForAuth_OpenAICompatibilityOriginalModelKeepsCompatThinking(t *testing.T) {
	static := internalregistry.LookupStaticModelInfo("gemini-2.5-pro")
	if static == nil {
		t.Fatal("expected static gemini-2.5-pro metadata")
	}
	service := &Service{cfg: &config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{{
			Name:    "compat-thinking",
			BaseURL: "https://example.com/v1",
			Models: []config.OpenAICompatibilityModel{{
				Name:     "gemini-2.5-pro",
				Alias:    "gemini-pro",
				Thinking: &internalregistry.ThinkingSupport{Levels: []string{"low", "high"}},
			}},
		}},
	}}
	auth := &coreauth.Auth{
		ID:       "auth-openai-compat-thinking-route",
		Provider: "openai-compatibility",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind":    "api_key",
			"compat_name":  "compat-thinking",
			"provider_key": "compat-thinking",
		},
	}
	modelRegistry := internalregistry.GetGlobalRegistry()
	modelRegistry.UnregisterClient(auth.ID)
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(auth.ID)
	})

	service.registerModelsForAuth(context.Background(), auth)

	registered := clientModelByID(modelRegistry.GetModelsForClient(auth.ID), "gemini-2.5-pro")
	if registered == nil {
		t.Fatal("expected original model to be registered")
	}
	if static.ContextLength > 0 && registered.ContextLength != static.ContextLength {
		t.Fatalf("context length = %d, want static %d", registered.ContextLength, static.ContextLength)
	}
	if registered.Thinking == nil || len(registered.Thinking.Levels) != 2 || registered.Thinking.Levels[0] != "low" || registered.Thinking.Levels[1] != "high" {
		t.Fatalf("thinking = %+v, want compat levels [low high]", registered.Thinking)
	}
}

func providersContain(providers []string, want string) bool {
	for _, provider := range providers {
		if strings.EqualFold(strings.TrimSpace(provider), want) {
			return true
		}
	}
	return false
}

func TestRegisterModelsForAuth_ConfigAliasExclusionBlocksOriginalModelPair(t *testing.T) {
	service := &Service{cfg: &config.Config{
		CodexKey: []internalconfig.CodexKey{{
			APIKey:         "codex-excluded-key",
			BaseURL:        "https://example.com",
			ExcludedModels: []string{"my-gpt"},
			Models: []internalconfig.CodexModel{{
				Name:  "gpt-5",
				Alias: "my-gpt",
			}},
		}},
	}}
	auth := &coreauth.Auth{
		ID:       "auth-codex-alias-excluded-route",
		Provider: "codex",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind": "api_key",
			"api_key":   "codex-excluded-key",
			"base_url":  "https://example.com",
		},
	}
	modelRegistry := internalregistry.GetGlobalRegistry()
	modelRegistry.UnregisterClient(auth.ID)
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(auth.ID)
	})

	service.registerModelsForAuth(context.Background(), auth)

	if providersContain(modelRegistry.GetModelProviders("my-gpt"), "codex") {
		t.Fatalf("alias model remained routable after exclusion")
	}
	if providersContain(modelRegistry.GetModelProviders("gpt-5"), "codex") {
		t.Fatalf("original model remained routable after alias exclusion")
	}
}

func TestRegisterModelsForAuth_AntigravityFetchesWebSearchCapability(t *testing.T) {
	var sawFetch bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != antigravityModelsPath {
			t.Fatalf("path = %q, want %s", r.URL.Path, antigravityModelsPath)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer token" {
			t.Fatalf("Authorization = %q, want bearer token", got)
		}
		sawFetch = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
				"models": {
					"gemini-3.1-flash-lite": {
						"displayName": "Gemini 3.1 Flash Lite",
						"maxTokens": 1,
						"maxOutputTokens": 2
					},
					"fetched-only-search-model": {
						"displayName": "Fetched Only Search Model"
					}
				},
				"webSearchModelIds": ["gemini-3.1-flash-lite", "fetched-only-search-model"]
			}`))
	}))
	defer server.Close()

	service := &Service{cfg: &config.Config{}}
	auth := &coreauth.Auth{
		ID:       "auth-antigravity-fetch-models",
		Provider: "antigravity",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"base_url": server.URL,
		},
		Metadata: map[string]any{
			"access_token": "token",
		},
	}

	registry := internalregistry.GetGlobalRegistry()
	registry.UnregisterClient(auth.ID)
	t.Cleanup(func() {
		registry.UnregisterClient(auth.ID)
	})

	service.registerModelsForAuth(context.Background(), auth)
	if !sawFetch {
		t.Fatal("expected fetchAvailableModels request")
	}

	models := registry.GetModelsForClient(auth.ID)
	staticModels := internalregistry.GetAntigravityModels()
	staticByID := make(map[string]*internalregistry.ModelInfo, len(staticModels))
	for _, model := range staticModels {
		if model != nil {
			staticByID[model.ID] = model
		}
	}

	var webSearchModel, agentModel, staticOnlyModel, fetchedOnlyModel *internalregistry.ModelInfo
	for _, model := range models {
		if model == nil {
			continue
		}
		switch strings.TrimSpace(model.ID) {
		case "gemini-3.1-flash-lite":
			webSearchModel = model
		case "gemini-3-flash-agent":
			agentModel = model
		case "gpt-oss-120b-medium":
			staticOnlyModel = model
		case "fetched-only-search-model":
			fetchedOnlyModel = model
		}
	}
	if webSearchModel == nil {
		t.Fatal("expected gemini-3.1-flash-lite to be registered")
	}
	if !webSearchModel.SupportsWebSearch {
		t.Fatal("expected gemini-3.1-flash-lite to support web search")
	}
	staticWebSearchModel := staticByID["gemini-3.1-flash-lite"]
	if staticWebSearchModel == nil {
		t.Fatal("expected static gemini-3.1-flash-lite definition")
	}
	if webSearchModel.ContextLength != staticWebSearchModel.ContextLength || webSearchModel.MaxCompletionTokens != staticWebSearchModel.MaxCompletionTokens {
		t.Fatalf("static token limits should be preserved, got=%#v static=%#v", webSearchModel, staticWebSearchModel)
	}
	if agentModel == nil {
		t.Fatal("expected gemini-3-flash-agent to be registered")
	}
	if agentModel.SupportsWebSearch {
		t.Fatal("gemini-3-flash-agent should not support web search")
	}
	if staticOnlyModel == nil {
		t.Fatal("expected static-only Antigravity model to remain registered")
	}
	if fetchedOnlyModel != nil {
		t.Fatalf("fetched-only model should not be registered: %#v", fetchedOnlyModel)
	}
}
