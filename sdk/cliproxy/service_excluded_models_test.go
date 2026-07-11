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
						{Name: "upstream-shared-image", Alias: "compat-shared", Image: true},
						{Name: "upstream-shared-chat", Alias: "compat-shared", Thinking: &internalregistry.ThinkingSupport{Levels: []string{"high"}}, InputModalities: []string{"text", "image"}},
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
	var sharedModel *internalregistry.ModelInfo
	for _, model := range models {
		if model == nil {
			continue
		}
		switch strings.TrimSpace(model.ID) {
		case "compat-image":
			imageModel = model
		case "compat-chat":
			chatModel = model
		case "compat-shared":
			sharedModel = model
		}
	}
	if imageModel == nil {
		t.Fatal("expected compat-image to be registered")
	}
	if imageModel.Type != internalregistry.OpenAIImageModelType {
		t.Fatalf("image model type = %q, want %q", imageModel.Type, internalregistry.OpenAIImageModelType)
	}
	if !imageModel.SupportsImageAPI {
		t.Fatal("expected image model to support the image API")
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
	if sharedModel == nil || sharedModel.Type != "openai-compatibility" || !sharedModel.SupportsImageAPI {
		t.Fatalf("shared alias model = %+v, want chat-visible image-capable registration", sharedModel)
	}
	if sharedModel.Thinking == nil || len(sharedModel.Thinking.Levels) != 1 || sharedModel.Thinking.Levels[0] != "high" {
		t.Fatalf("shared alias thinking = %+v, want chat metadata", sharedModel.Thinking)
	}
	if got := strings.Join(sharedModel.SupportedInputModalities, ","); got != "text,image" {
		t.Fatalf("shared alias input modalities = %q, want text,image", got)
	}
}

func TestBuildAPIKeyConfigModelsPrefersConfiguredThinking(t *testing.T) {
	configured := &internalregistry.ThinkingSupport{Levels: []string{"xhigh", "high"}}
	tests := []struct {
		name  string
		build func() []*ModelInfo
	}{
		{name: "claude", build: func() []*ModelInfo {
			return buildClaudeConfigModels(&config.ClaudeKey{Models: []internalconfig.ClaudeModel{{Name: "custom-model", Alias: "public-model", Thinking: configured}}})
		}},
		{name: "codex", build: func() []*ModelInfo {
			return buildCodexConfigModels(&config.CodexKey{Models: []internalconfig.CodexModel{{Name: "custom-model", Alias: "public-model", Thinking: configured}}})
		}},
		{name: "xai", build: func() []*ModelInfo {
			return buildXAIConfigModels(&config.XAIKey{Models: []internalconfig.XAIModel{{Name: "custom-model", Alias: "public-model", Thinking: configured}}})
		}},
		{name: "gemini", build: func() []*ModelInfo {
			return buildGeminiConfigModels(&config.GeminiKey{Models: []internalconfig.GeminiModel{{Name: "custom-model", Alias: "public-model", Thinking: configured}}})
		}},
		{name: "vertex", build: func() []*ModelInfo {
			return buildVertexCompatConfigModels(&config.VertexCompatKey{Models: []config.VertexCompatModel{{Name: "custom-model", Alias: "public-model", Thinking: configured}}})
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got *ModelInfo
			for _, info := range tc.build() {
				if info != nil && info.ID == "public-model" {
					got = info
					break
				}
			}
			if got == nil || got.Thinking == nil || strings.Join(got.Thinking.Levels, ",") != "xhigh,high" {
				t.Fatalf("registered model = %+v, want configured thinking levels", got)
			}
		})
	}
}

func TestBuildGeminiStyleConfigModelsRebindsStaticResourceName(t *testing.T) {
	tests := []struct {
		name  string
		build func() []*ModelInfo
	}{
		{
			name: "gemini",
			build: func() []*ModelInfo {
				return buildGeminiConfigModels(&config.GeminiKey{Models: []internalconfig.GeminiModel{{
					Name: "gemini-2.5-flash", Alias: "public-flash",
				}}})
			},
		},
		{
			name: "vertex",
			build: func() []*ModelInfo {
				return buildVertexCompatConfigModels(&config.VertexCompatKey{Models: []config.VertexCompatModel{{
					Name: "gemini-2.5-flash", Alias: "public-flash",
				}}})
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			models := tc.build()
			if len(models) != 1 || models[0] == nil {
				t.Fatalf("registered models = %#v, want one configured alias", models)
			}
			if models[0].ID != "public-flash" || models[0].Name != "models/public-flash" {
				t.Fatalf("configured identity = %#v, want alias-bound Gemini resource name", models[0])
			}
		})
	}
}

func TestBuildNonImageProviderConfigModelsDoNotInheritStaticImageAPI(t *testing.T) {
	tests := []struct {
		name  string
		build func() []*ModelInfo
	}{
		{name: "claude", build: func() []*ModelInfo {
			return buildClaudeConfigModels(&config.ClaudeKey{Models: []internalconfig.ClaudeModel{{Name: "gpt-image-2", Alias: "public-image"}}})
		}},
		{name: "gemini", build: func() []*ModelInfo {
			return buildGeminiConfigModels(&config.GeminiKey{Models: []internalconfig.GeminiModel{{Name: "gpt-image-2", Alias: "public-image"}}})
		}},
		{name: "vertex", build: func() []*ModelInfo {
			return buildVertexCompatConfigModels(&config.VertexCompatKey{Models: []config.VertexCompatModel{{Name: "gpt-image-2", Alias: "public-image"}}})
		}},
		{name: "xai foreign image", build: func() []*ModelInfo {
			return buildXAIConfigModels(&config.XAIKey{Models: []internalconfig.XAIModel{{Name: "gpt-image-2", Alias: "public-image"}}})
		}},
		{name: "codex foreign image", build: func() []*ModelInfo {
			return buildCodexConfigModels(&config.CodexKey{Models: []internalconfig.CodexModel{{Name: "grok-imagine-image", Alias: "public-image"}}})
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			models := tc.build()
			var configured *ModelInfo
			for _, model := range models {
				if model != nil && model.ID == "public-image" {
					configured = model
					break
				}
			}
			if configured == nil {
				t.Fatalf("registered models = %+v, want configured alias", models)
			}
			if configured.SupportsImageAPI {
				t.Fatalf("%s model inherited provider-specific image API support", tc.name)
			}
		})
	}
}

func TestBuildOpenAICompatibilityConfigModelsFallsBackToStaticThinking(t *testing.T) {
	const upstream = "gpt-5.6-luna"
	static := internalregistry.LookupStaticModelInfo(upstream)
	if static == nil || static.Thinking == nil {
		t.Fatalf("static model %s has no thinking metadata", upstream)
	}
	models := buildOpenAICompatibilityConfigModels(&config.OpenAICompatibility{
		Name:   "compat",
		Models: []config.OpenAICompatibilityModel{{Name: upstream, Alias: "public-model"}},
	})
	if len(models) != 1 || models[0] == nil || models[0].Thinking == nil {
		t.Fatalf("registered models = %+v, want static thinking metadata", models)
	}
	if got, want := strings.Join(models[0].Thinking.Levels, ","), strings.Join(static.Thinking.Levels, ","); got != want {
		t.Fatalf("thinking levels = %q, want static levels %q", got, want)
	}
}

func TestBuildOpenAICompatibilityImageModelUsesSuffixFreeStaticCapabilities(t *testing.T) {
	const upstream = "gpt-5.6-luna(high)"
	static := internalregistry.LookupStaticModelInfo("gpt-5.6-luna")
	if static == nil || static.Thinking == nil {
		t.Fatal("gpt-5.6-luna static thinking metadata is unavailable")
	}
	models := buildOpenAICompatibilityConfigModels(&config.OpenAICompatibility{
		Name: "compat",
		Models: []config.OpenAICompatibilityModel{{
			Name: upstream, Alias: "public-image", Image: true,
		}},
	})
	if len(models) != 1 || models[0] == nil || models[0].Thinking == nil || !models[0].SupportsImageAPI {
		t.Fatalf("registered models = %+v, want static thinking and image support", models)
	}
	if got, want := strings.Join(models[0].Thinking.Levels, ","), strings.Join(static.Thinking.Levels, ","); got != want {
		t.Fatalf("thinking levels = %q, want %q", got, want)
	}
}

func TestBuildNativeImageConfigModelsUsesAliasAsUpstreamFallback(t *testing.T) {
	tests := []struct {
		name    string
		modelID string
		build   func() []*ModelInfo
	}{
		{
			name:    "codex",
			modelID: "gpt-image-2",
			build: func() []*ModelInfo {
				return buildCodexConfigModels(&config.CodexKey{Models: []internalconfig.CodexModel{{Alias: "gpt-image-2"}}})
			},
		},
		{
			name:    "xai",
			modelID: "grok-imagine-image",
			build: func() []*ModelInfo {
				return buildXAIConfigModels(&config.XAIKey{Models: []internalconfig.XAIModel{{Alias: "grok-imagine-image"}}})
			},
		},
		{
			name:    "xai public alias",
			modelID: "public-image",
			build: func() []*ModelInfo {
				return buildXAIConfigModels(&config.XAIKey{Models: []internalconfig.XAIModel{{
					Name: "grok-imagine-image", Alias: "public-image",
				}}})
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var configured *ModelInfo
			for _, model := range tc.build() {
				if model != nil && model.ID == tc.modelID {
					configured = model
					break
				}
			}
			if configured == nil || !configured.SupportsImageAPI {
				t.Fatalf("registered model = %+v, want alias-only static image support", configured)
			}
		})
	}
}

func TestRegisterModelsForAuth_NativeImageAliasRemainsNonChat(t *testing.T) {
	tests := []struct {
		name      string
		provider  string
		upstream  string
		configure func(*config.Config, config.CodexKey)
	}{
		{
			name:     "codex",
			provider: "codex",
			upstream: "gpt-image-2",
			configure: func(cfg *config.Config, entry config.CodexKey) {
				cfg.CodexKey = []config.CodexKey{entry}
			},
		},
		{
			name:     "xai",
			provider: "xai",
			upstream: "grok-imagine-image",
			configure: func(cfg *config.Config, entry config.CodexKey) {
				cfg.XAIKey = []config.XAIKey{entry}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			const (
				apiKey = "test-key"
				prefix = "tenant"
				alias  = "public-image"
			)
			cfg := &config.Config{SDKConfig: config.SDKConfig{ForceModelPrefix: true}}
			tc.configure(cfg, config.CodexKey{
				APIKey: apiKey,
				Prefix: prefix,
				Models: []internalconfig.CodexModel{{Name: tc.upstream, Alias: alias}},
			})
			service := &Service{cfg: cfg}
			auth := &coreauth.Auth{
				ID:       "auth-native-image-alias-" + tc.name,
				Provider: tc.provider,
				Prefix:   prefix,
				Status:   coreauth.StatusActive,
				Attributes: map[string]string{
					"auth_kind": "apikey",
					"api_key":   apiKey,
				},
			}

			modelRegistry := internalregistry.GetGlobalRegistry()
			modelRegistry.UnregisterClient(auth.ID)
			t.Cleanup(func() { modelRegistry.UnregisterClient(auth.ID) })
			service.registerModelsForAuth(context.Background(), auth)

			modelID := prefix + "/" + alias
			info, supportsChat := modelRegistry.GetCatalogModelInfo(modelID)
			if info == nil || !info.SupportsImageAPI || !info.ChatDisabled {
				t.Fatalf("registered model = %#v, want native image-only metadata", info)
			}
			if supportsChat || modelRegistry.ClientModelSupportsChat(auth.ID, modelID) {
				t.Fatalf("native image alias %q unexpectedly supports chat", modelID)
			}
			if !modelRegistry.ClientModelSupportsImageAPI(auth.ID, modelID) {
				t.Fatalf("native image alias %q lost image execution support", modelID)
			}
		})
	}
}

func TestRegisterModelsForAuth_NativeVideoAliasesRemainVideoOnly(t *testing.T) {
	tests := []struct {
		name       string
		configure  func(*config.Config)
		attributes map[string]string
	}{
		{
			name: "api-key alias and prefix",
			configure: func(cfg *config.Config) {
				cfg.XAIKey = []config.XAIKey{{
					APIKey: "test-key",
					Prefix: "tenant",
					Models: []internalconfig.CodexModel{{
						Name:  "grok-imagine-video",
						Alias: "public-video",
					}},
				}}
			},
			attributes: map[string]string{
				"auth_kind": "apikey",
				"api_key":   "test-key",
			},
		},
		{
			name: "oauth alias and prefix",
			configure: func(cfg *config.Config) {
				cfg.OAuthModelAlias = map[string][]internalconfig.OAuthModelAlias{
					"xai": {{
						Name:  "grok-imagine-video",
						Alias: "public-video",
					}},
				}
			},
			attributes: map[string]string{"auth_kind": "oauth"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{SDKConfig: config.SDKConfig{ForceModelPrefix: true}}
			tc.configure(cfg)
			service := &Service{cfg: cfg}
			auth := &coreauth.Auth{
				ID:         "auth-native-video-" + strings.ReplaceAll(tc.name, " ", "-"),
				Provider:   "xai",
				Prefix:     "tenant",
				Status:     coreauth.StatusActive,
				Attributes: tc.attributes,
			}

			modelRegistry := internalregistry.GetGlobalRegistry()
			modelRegistry.UnregisterClient(auth.ID)
			t.Cleanup(func() { modelRegistry.UnregisterClient(auth.ID) })
			service.registerModelsForAuth(context.Background(), auth)

			const modelID = "tenant/public-video"
			info, supportsChat := modelRegistry.GetCatalogModelInfo(modelID)
			if info == nil || !info.SupportsVideoAPI || !info.ChatDisabled {
				t.Fatalf("registered model = %#v, want native video-only metadata", info)
			}
			if supportsChat || modelRegistry.ClientModelSupportsChat(auth.ID, modelID) {
				t.Fatalf("native video alias %q unexpectedly supports chat", modelID)
			}
			if !modelRegistry.ClientModelSupportsExecution(auth.ID, modelID, internalregistry.ModelExecutionVideo) {
				t.Fatalf("native video alias %q lost video execution support", modelID)
			}
			if modelRegistry.ClientModelSupportsImageAPI(auth.ID, modelID) {
				t.Fatalf("native video alias %q unexpectedly supports image execution", modelID)
			}
		})
	}
}

func TestRegisterModelsForAuth_NativeAliasOnlyImageModelWithPrefix(t *testing.T) {
	tests := []struct {
		name      string
		provider  string
		modelID   string
		configure func(*config.Config, config.CodexKey)
	}{
		{
			name:     "codex",
			provider: "codex",
			modelID:  "gpt-image-2",
			configure: func(cfg *config.Config, entry config.CodexKey) {
				cfg.CodexKey = []config.CodexKey{entry}
			},
		},
		{
			name:     "xai",
			provider: "xai",
			modelID:  "grok-imagine-image",
			configure: func(cfg *config.Config, entry config.CodexKey) {
				cfg.XAIKey = []config.XAIKey{entry}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			const (
				apiKey = "test-key"
				prefix = "tenant"
			)
			cfg := &config.Config{SDKConfig: config.SDKConfig{ForceModelPrefix: true}}
			tc.configure(cfg, config.CodexKey{
				APIKey: apiKey,
				Prefix: prefix,
				Models: []internalconfig.CodexModel{{Alias: tc.modelID}},
			})
			service := &Service{cfg: cfg}
			auth := &coreauth.Auth{
				ID:       "auth-native-alias-only-" + tc.name,
				Provider: tc.provider,
				Prefix:   prefix,
				Status:   coreauth.StatusActive,
				Attributes: map[string]string{
					"auth_kind": "apikey",
					"api_key":   apiKey,
				},
			}

			modelRegistry := internalregistry.GetGlobalRegistry()
			modelRegistry.UnregisterClient(auth.ID)
			t.Cleanup(func() { modelRegistry.UnregisterClient(auth.ID) })
			service.registerModelsForAuth(context.Background(), auth)

			prefixedModel := prefix + "/" + tc.modelID
			if !modelRegistry.ClientSupportsModel(auth.ID, prefixedModel) {
				t.Fatalf("prefixed alias-only model %q is not selectable", prefixedModel)
			}
			if !modelRegistry.ClientModelSupportsImageAPI(auth.ID, prefixedModel) {
				t.Fatalf("prefixed alias-only model %q does not support image execution", prefixedModel)
			}
		})
	}
}

func TestRegisterModelsForAuth_OpenAICompatibilityImageModelWithPrefix(t *testing.T) {
	service := &Service{
		cfg: &config.Config{
			SDKConfig: config.SDKConfig{ForceModelPrefix: true},
			OpenAICompatibility: []config.OpenAICompatibility{{
				Name:    "images",
				Prefix:  "tenant",
				BaseURL: "https://example.com/v1",
				Models: []config.OpenAICompatibilityModel{{
					Name: "upstream-image", Alias: "compat-image", Image: true,
				}},
			}},
		},
	}
	auth := &coreauth.Auth{
		ID:       "auth-openai-compat-prefixed-image",
		Provider: "openai-compatibility",
		Prefix:   "tenant",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind":    "api_key",
			"api_key":      "test-key",
			"compat_name":  "images",
			"provider_key": "images",
		},
	}

	modelRegistry := internalregistry.GetGlobalRegistry()
	modelRegistry.UnregisterClient(auth.ID)
	t.Cleanup(func() { modelRegistry.UnregisterClient(auth.ID) })
	service.registerModelsForAuth(context.Background(), auth)

	var info *internalregistry.ModelInfo
	for _, candidate := range modelRegistry.GetModelsForClient(auth.ID) {
		if candidate != nil && candidate.ID == "tenant/compat-image" {
			info = candidate
			break
		}
	}
	if info == nil {
		t.Fatal("expected prefixed image model to be registered")
	}
	if info.Type != internalregistry.OpenAIImageModelType {
		t.Fatalf("prefixed image model type = %q, want %q", info.Type, internalregistry.OpenAIImageModelType)
	}
	if !info.SupportsImageAPI {
		t.Fatal("expected prefixed image model to support the image API")
	}
	if !modelRegistry.ClientSupportsModel(auth.ID, "tenant/compat-image") {
		t.Fatal("prefixed image model is not selectable for its configured auth")
	}
}

func TestRegisterModelsForAuth_OpenAICompatibilityInputModalities(t *testing.T) {
	service := &Service{
		cfg: &config.Config{
			OpenAICompatibility: []config.OpenAICompatibility{
				{
					Name:    "mimo",
					BaseURL: "https://example.com/v1",
					Models: []config.OpenAICompatibilityModel{
						{
							Name:             "mimo-v2.5-pro",
							Alias:            "mimo-v2.5-pro",
							InputModalities:  []string{"text", "image"},
							OutputModalities: []string{"text"},
						},
						{Name: "upstream-image", Alias: "compat-image", Image: true},
					},
				},
			},
		},
	}
	auth := &coreauth.Auth{
		ID:       "auth-openai-compat-modalities",
		Provider: "openai-compatibility",
		Status:   coreauth.StatusActive,
		Attributes: map[string]string{
			"auth_kind":    "api_key",
			"compat_name":  "mimo",
			"provider_key": "mimo",
		},
	}

	modelRegistry := internalregistry.GetGlobalRegistry()
	modelRegistry.UnregisterClient(auth.ID)
	t.Cleanup(func() {
		modelRegistry.UnregisterClient(auth.ID)
	})

	service.registerModelsForAuth(context.Background(), auth)

	models := modelRegistry.GetModelsForClient(auth.ID)
	var visionModel *internalregistry.ModelInfo
	var imageEndpointModel *internalregistry.ModelInfo
	for _, model := range models {
		if model == nil {
			continue
		}
		switch strings.TrimSpace(model.ID) {
		case "mimo-v2.5-pro":
			visionModel = model
		case "compat-image":
			imageEndpointModel = model
		}
	}
	if visionModel == nil {
		t.Fatal("expected mimo-v2.5-pro to be registered")
	}
	if visionModel.Type != "openai-compatibility" {
		t.Fatalf("vision model type = %q, want openai-compatibility", visionModel.Type)
	}
	if got := strings.Join(visionModel.SupportedInputModalities, ","); got != "text,image" {
		t.Fatalf("SupportedInputModalities = %q, want text,image", got)
	}
	if got := strings.Join(visionModel.SupportedOutputModalities, ","); got != "text" {
		t.Fatalf("SupportedOutputModalities = %q, want text", got)
	}
	if imageEndpointModel == nil {
		t.Fatal("expected compat-image to be registered")
	}
	if imageEndpointModel.Type != internalregistry.OpenAIImageModelType {
		t.Fatalf("image endpoint model type = %q, want %q", imageEndpointModel.Type, internalregistry.OpenAIImageModelType)
	}
	if len(imageEndpointModel.SupportedInputModalities) != 0 {
		t.Fatalf("image endpoint model should not inherit chat input modalities: %+v", imageEndpointModel.SupportedInputModalities)
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
