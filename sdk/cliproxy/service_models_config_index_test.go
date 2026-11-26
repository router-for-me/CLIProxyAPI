package cliproxy

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/constant"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestOpenAICompatibilityRegistrationCacheUsesConfigIndex(t *testing.T) {
	service := &Service{cfg: &config.Config{OpenAICompatibility: []config.OpenAICompatibility{
		{Name: "shared", Models: []config.OpenAICompatibilityModel{{Name: "first"}}},
		{Name: "shared", Models: []config.OpenAICompatibilityModel{{Name: "second"}}},
	}}}
	cache := service.newOpenAICompatibilityRegistrationCache()
	auth := &coreauth.Auth{Attributes: map[string]string{
		coreauth.AttributeSource:      "config:shared[token-1]",
		coreauth.AttributeConfigIndex: "1",
	}}
	entry, ok := cache.lookup(auth, "shared")
	if !ok || entry == nil || len(entry.models) != 1 || entry.models[0].ID != "second" {
		t.Fatalf("cached config entry = %+v, want second model", entry)
	}
}

func TestResolveConfigClaudeKeyUsesConfigIndex(t *testing.T) {
	service := &Service{cfg: &config.Config{ClaudeKey: []config.ClaudeKey{
		{APIKey: "shared-key", Models: []config.ClaudeModel{{Name: "first"}}},
		{APIKey: "shared-key", Models: []config.ClaudeModel{{Name: "second"}}},
	}}}
	auth := &coreauth.Auth{Attributes: map[string]string{
		coreauth.AttributeAPIKey:      "shared-key",
		coreauth.AttributeSource:      "config:claude[token-1]",
		coreauth.AttributeConfigIndex: "1",
	}}

	entry := service.resolveConfigClaudeKey(auth)
	if entry == nil || len(entry.Models) != 1 || entry.Models[0].Name != "second" {
		t.Fatalf("resolved config entry = %+v, want second entry", entry)
	}
}

func TestResolveConfigClaudeKeyStaleConfigIndexFallsBack(t *testing.T) {
	t.Run("stale index falls back by api_key", func(t *testing.T) {
		service := &Service{cfg: &config.Config{ClaudeKey: []config.ClaudeKey{
			{APIKey: "shifted-key", Models: []config.ClaudeModel{{Name: "wrong"}}},
			{APIKey: "target-key", Models: []config.ClaudeModel{{Name: "right"}}},
		}}}
		auth := &coreauth.Auth{Attributes: map[string]string{
			coreauth.AttributeAPIKey:      "target-key",
			coreauth.AttributeSource:      "config:claude[token-1]",
			coreauth.AttributeConfigIndex: "0",
		}}

		entry := service.resolveConfigClaudeKey(auth)
		if entry == nil || len(entry.Models) != 1 || entry.Models[0].Name != "right" {
			t.Fatalf("resolved config entry = %+v, want target-key entry", entry)
		}
	})

	t.Run("stale index falls back by base_url when api_key matches", func(t *testing.T) {
		service := &Service{cfg: &config.Config{ClaudeKey: []config.ClaudeKey{
			{APIKey: "shared-key", BaseURL: "https://other.example.com", Models: []config.ClaudeModel{{Name: "wrong"}}},
			{APIKey: "shared-key", BaseURL: "https://target.example.com", Models: []config.ClaudeModel{{Name: "right"}}},
		}}}
		auth := &coreauth.Auth{Attributes: map[string]string{
			coreauth.AttributeAPIKey:      "shared-key",
			"base_url":                    "https://target.example.com",
			coreauth.AttributeSource:      "config:claude[token-1]",
			coreauth.AttributeConfigIndex: "0",
		}}

		entry := service.resolveConfigClaudeKey(auth)
		if entry == nil || len(entry.Models) != 1 || entry.Models[0].Name != "right" {
			t.Fatalf("resolved config entry = %+v, want matching base_url entry", entry)
		}
	})

	t.Run("deleted entry with stale index returns nil", func(t *testing.T) {
		service := &Service{cfg: &config.Config{ClaudeKey: []config.ClaudeKey{
			{APIKey: "other-key", Models: []config.ClaudeModel{{Name: "wrong"}}},
		}}}
		auth := &coreauth.Auth{Attributes: map[string]string{
			coreauth.AttributeAPIKey:      "deleted-key",
			coreauth.AttributeSource:      "config:claude[token-1]",
			coreauth.AttributeConfigIndex: "0",
		}}

		if entry := service.resolveConfigClaudeKey(auth); entry != nil {
			t.Fatalf("resolved config entry = %+v, want nil", entry)
		}
	})
}

func TestResolveConfigGeminiKeyStaleConfigIndexFallsBack(t *testing.T) {
	t.Run("gemini stale index falls back by api_key", func(t *testing.T) {
		service := &Service{cfg: &config.Config{GeminiKey: []config.GeminiKey{
			{APIKey: "shifted-key", Models: []config.GeminiModel{{Name: "wrong"}}},
			{APIKey: "target-key", Models: []config.GeminiModel{{Name: "right"}}},
		}}}
		auth := &coreauth.Auth{
			Provider: constant.Gemini,
			Attributes: map[string]string{
				coreauth.AttributeAPIKey:      "target-key",
				coreauth.AttributeSource:      "config:gemini[token-1]",
				coreauth.AttributeConfigIndex: "0",
			},
		}

		entry := service.resolveConfigGeminiKey(auth)
		if entry == nil || len(entry.Models) != 1 || entry.Models[0].Name != "right" {
			t.Fatalf("resolved Gemini config entry = %+v, want target-key entry", entry)
		}
	})

	t.Run("gemini stale index falls back by base_url when api_key matches", func(t *testing.T) {
		service := &Service{cfg: &config.Config{GeminiKey: []config.GeminiKey{
			{APIKey: "shared-key", BaseURL: "https://other.example.com", Models: []config.GeminiModel{{Name: "wrong"}}},
			{APIKey: "shared-key", BaseURL: "https://target.example.com", Models: []config.GeminiModel{{Name: "right"}}},
		}}}
		auth := &coreauth.Auth{
			Provider: constant.Gemini,
			Attributes: map[string]string{
				coreauth.AttributeAPIKey:      "shared-key",
				"base_url":                    "https://target.example.com",
				coreauth.AttributeSource:      "config:gemini[token-1]",
				coreauth.AttributeConfigIndex: "0",
			},
		}

		entry := service.resolveConfigGeminiKey(auth)
		if entry == nil || len(entry.Models) != 1 || entry.Models[0].Name != "right" {
			t.Fatalf("resolved Gemini config entry = %+v, want matching base_url entry", entry)
		}
	})

	t.Run("interactions stale index falls back by api_key", func(t *testing.T) {
		service := &Service{cfg: &config.Config{InteractionsKey: []config.GeminiKey{
			{APIKey: "shifted-key", Models: []config.GeminiModel{{Name: "wrong"}}},
			{APIKey: "target-key", Models: []config.GeminiModel{{Name: "right"}}},
		}}}
		auth := &coreauth.Auth{
			Provider: constant.GeminiInteractions,
			Attributes: map[string]string{
				coreauth.AttributeAPIKey:      "target-key",
				coreauth.AttributeSource:      "config:interactions[token-1]",
				coreauth.AttributeConfigIndex: "0",
			},
		}

		entry := service.resolveConfigInteractionsKey(auth)
		if entry == nil || len(entry.Models) != 1 || entry.Models[0].Name != "right" {
			t.Fatalf("resolved Interactions config entry = %+v, want target-key entry", entry)
		}
	})

	t.Run("gemini matching credentials still disambiguate via config_index", func(t *testing.T) {
		service := &Service{cfg: &config.Config{GeminiKey: []config.GeminiKey{
			{APIKey: "shared-key", Models: []config.GeminiModel{{Name: "first"}}},
			{APIKey: "shared-key", Models: []config.GeminiModel{{Name: "second"}}},
		}}}
		auth := &coreauth.Auth{
			Provider: constant.Gemini,
			Attributes: map[string]string{
				coreauth.AttributeAPIKey:      "shared-key",
				coreauth.AttributeSource:      "config:gemini[token-1]",
				coreauth.AttributeConfigIndex: "1",
			},
		}

		entry := service.resolveConfigGeminiKey(auth)
		if entry == nil || len(entry.Models) != 1 || entry.Models[0].Name != "second" {
			t.Fatalf("resolved Gemini config entry = %+v, want second entry", entry)
		}
	})
}

func TestResolveConfigVertexCompatKeyStaleConfigIndexFallsBack(t *testing.T) {
	t.Run("stale index falls back by api_key", func(t *testing.T) {
		service := &Service{cfg: &config.Config{VertexCompatAPIKey: []config.VertexCompatKey{
			{APIKey: "shifted-key", Models: []config.VertexCompatModel{{Name: "wrong"}}},
			{APIKey: "target-key", Models: []config.VertexCompatModel{{Name: "right"}}},
		}}}
		auth := &coreauth.Auth{Attributes: map[string]string{
			coreauth.AttributeAPIKey:      "target-key",
			coreauth.AttributeSource:      "config:vertex-apikey[token-1]",
			coreauth.AttributeConfigIndex: "0",
		}}

		entry := service.resolveConfigVertexCompatKey(auth)
		if entry == nil || len(entry.Models) != 1 || entry.Models[0].Name != "right" {
			t.Fatalf("resolved Vertex config entry = %+v, want target-key entry", entry)
		}
	})

	t.Run("stale index falls back by base_url when api_key matches", func(t *testing.T) {
		service := &Service{cfg: &config.Config{VertexCompatAPIKey: []config.VertexCompatKey{
			{APIKey: "shared-key", BaseURL: "https://other.example.com", Models: []config.VertexCompatModel{{Name: "wrong"}}},
			{APIKey: "shared-key", BaseURL: "https://target.example.com", Models: []config.VertexCompatModel{{Name: "right"}}},
		}}}
		auth := &coreauth.Auth{Attributes: map[string]string{
			coreauth.AttributeAPIKey:      "shared-key",
			"base_url":                    "https://target.example.com",
			coreauth.AttributeSource:      "config:vertex-apikey[token-1]",
			coreauth.AttributeConfigIndex: "0",
		}}

		entry := service.resolveConfigVertexCompatKey(auth)
		if entry == nil || len(entry.Models) != 1 || entry.Models[0].Name != "right" {
			t.Fatalf("resolved Vertex config entry = %+v, want matching base_url entry", entry)
		}
	})

	t.Run("matching credentials still disambiguate via config_index", func(t *testing.T) {
		service := &Service{cfg: &config.Config{VertexCompatAPIKey: []config.VertexCompatKey{
			{APIKey: "shared-key", Models: []config.VertexCompatModel{{Name: "first"}}},
			{APIKey: "shared-key", Models: []config.VertexCompatModel{{Name: "second"}}},
		}}}
		auth := &coreauth.Auth{Attributes: map[string]string{
			coreauth.AttributeAPIKey:      "shared-key",
			coreauth.AttributeSource:      "config:vertex-apikey[token-1]",
			coreauth.AttributeConfigIndex: "1",
		}}

		entry := service.resolveConfigVertexCompatKey(auth)
		if entry == nil || len(entry.Models) != 1 || entry.Models[0].Name != "second" {
			t.Fatalf("resolved Vertex config entry = %+v, want second entry", entry)
		}
	})
}

func TestResolveConfigXAIKeyStaleConfigIndexFallsBack(t *testing.T) {
	t.Run("stale index falls back by api_key", func(t *testing.T) {
		service := &Service{cfg: &config.Config{XAIKey: []config.XAIKey{
			{APIKey: "shifted-key", Models: []config.CodexModel{{Name: "wrong"}}},
			{APIKey: "target-key", Models: []config.CodexModel{{Name: "right"}}},
		}}}
		auth := &coreauth.Auth{Attributes: map[string]string{
			coreauth.AttributeAPIKey:      "target-key",
			coreauth.AttributeSource:      "config:xai[token-1]",
			coreauth.AttributeConfigIndex: "0",
		}}

		entry := service.resolveConfigXAIKey(auth)
		if entry == nil || len(entry.Models) != 1 || entry.Models[0].Name != "right" {
			t.Fatalf("resolved xAI config entry = %+v, want target-key entry", entry)
		}
	})

	t.Run("matching credentials still disambiguate via config_index", func(t *testing.T) {
		service := &Service{cfg: &config.Config{XAIKey: []config.XAIKey{
			{APIKey: "shared-key", Models: []config.CodexModel{{Name: "first"}}},
			{APIKey: "shared-key", Models: []config.CodexModel{{Name: "second"}}},
		}}}
		auth := &coreauth.Auth{Attributes: map[string]string{
			coreauth.AttributeAPIKey:      "shared-key",
			coreauth.AttributeSource:      "config:xai[token-1]",
			coreauth.AttributeConfigIndex: "1",
		}}

		entry := service.resolveConfigXAIKey(auth)
		if entry == nil || len(entry.Models) != 1 || entry.Models[0].Name != "second" {
			t.Fatalf("resolved xAI config entry = %+v, want second entry", entry)
		}
	})
}

func TestResolveConfigMetaKeyStaleConfigIndexFallsBack(t *testing.T) {
	t.Run("stale index falls back by api_key", func(t *testing.T) {
		service := &Service{cfg: &config.Config{MetaKey: []config.MetaKey{
			{APIKey: "shifted-key", Models: []config.CodexModel{{Name: "wrong"}}},
			{APIKey: "target-key", Models: []config.CodexModel{{Name: "right"}}},
		}}}
		auth := &coreauth.Auth{Attributes: map[string]string{
			coreauth.AttributeAPIKey:      "target-key",
			coreauth.AttributeSource:      "config:meta[token-1]",
			coreauth.AttributeConfigIndex: "0",
		}}

		entry := service.resolveConfigMetaKey(auth)
		if entry == nil || len(entry.Models) != 1 || entry.Models[0].Name != "right" {
			t.Fatalf("resolved Meta config entry = %+v, want target-key entry", entry)
		}
	})

	t.Run("matching credentials still disambiguate via config_index", func(t *testing.T) {
		service := &Service{cfg: &config.Config{MetaKey: []config.MetaKey{
			{APIKey: "shared-key", Models: []config.CodexModel{{Name: "first"}}},
			{APIKey: "shared-key", Models: []config.CodexModel{{Name: "second"}}},
		}}}
		auth := &coreauth.Auth{Attributes: map[string]string{
			coreauth.AttributeAPIKey:      "shared-key",
			coreauth.AttributeSource:      "config:meta[token-1]",
			coreauth.AttributeConfigIndex: "1",
		}}

		entry := service.resolveConfigMetaKey(auth)
		if entry == nil || len(entry.Models) != 1 || entry.Models[0].Name != "second" {
			t.Fatalf("resolved Meta config entry = %+v, want second entry", entry)
		}
	})
}

func TestRegisterModelsForAuthOpenAICompatStaleConfigIndexFallsBack(t *testing.T) {
	reg := GlobalModelRegistry()

	t.Run("stale index falls back by compat name without cache", func(t *testing.T) {
		authID := "openai-compat-stale-index-name-nocache"
		reg.UnregisterClient(authID)
		t.Cleanup(func() { reg.UnregisterClient(authID) })

		service := &Service{cfg: &config.Config{OpenAICompatibility: []config.OpenAICompatibility{
			{Name: "other", BaseURL: "https://other.example.com/v1", Models: []config.OpenAICompatibilityModel{{Name: "wrong-model"}}},
			{Name: "target", BaseURL: "https://target.example.com/v1", Models: []config.OpenAICompatibilityModel{{Name: "right-model"}}},
		}}}
		auth := &coreauth.Auth{
			ID:       authID,
			Provider: "target",
			Status:   coreauth.StatusActive,
			Attributes: map[string]string{
				"compat_name":                 "target",
				"provider_key":                "target",
				"base_url":                    "https://target.example.com/v1",
				coreauth.AttributeSource:      "config:target[token-1]",
				coreauth.AttributeConfigIndex: "0",
			},
		}

		service.registerModelsForAuth(context.Background(), auth)
		models := reg.GetModelsForClient(authID)
		if len(models) != 1 || models[0].ID != "right-model" {
			t.Fatalf("registered models = %+v, want [right-model]", models)
		}
	})

	t.Run("stale index falls back by base_url when compat name is shared", func(t *testing.T) {
		authID := "openai-compat-stale-index-baseurl-nocache"
		reg.UnregisterClient(authID)
		t.Cleanup(func() { reg.UnregisterClient(authID) })

		service := &Service{cfg: &config.Config{OpenAICompatibility: []config.OpenAICompatibility{
			{Name: "shared", BaseURL: "https://other.example.com/v1", Models: []config.OpenAICompatibilityModel{{Name: "wrong-model"}}},
			{Name: "shared", BaseURL: "https://target.example.com/v1", Models: []config.OpenAICompatibilityModel{{Name: "right-model"}}},
		}}}
		auth := &coreauth.Auth{
			ID:       authID,
			Provider: "shared",
			Status:   coreauth.StatusActive,
			Attributes: map[string]string{
				"compat_name":                 "shared",
				"provider_key":                "shared",
				"base_url":                    "https://target.example.com/v1",
				coreauth.AttributeSource:      "config:shared[token-1]",
				coreauth.AttributeConfigIndex: "0",
			},
		}

		service.registerModelsForAuth(context.Background(), auth)
		models := reg.GetModelsForClient(authID)
		if len(models) != 1 || models[0].ID != "right-model" {
			t.Fatalf("registered models = %+v, want [right-model]", models)
		}
	})

	t.Run("deleted compat entry with stale index unregisters models", func(t *testing.T) {
		authID := "openai-compat-stale-index-deleted"
		reg.UnregisterClient(authID)
		reg.RegisterClient(authID, "deleted", []*ModelInfo{{ID: "old-model"}})
		t.Cleanup(func() { reg.UnregisterClient(authID) })

		service := &Service{cfg: &config.Config{OpenAICompatibility: []config.OpenAICompatibility{
			{Name: "other", BaseURL: "https://other.example.com/v1", Models: []config.OpenAICompatibilityModel{{Name: "wrong-model"}}},
		}}}
		auth := &coreauth.Auth{
			ID:       authID,
			Provider: "deleted",
			Status:   coreauth.StatusActive,
			Attributes: map[string]string{
				"compat_name":                 "deleted",
				"provider_key":                "deleted",
				"base_url":                    "https://deleted.example.com/v1",
				coreauth.AttributeSource:      "config:deleted[token-1]",
				coreauth.AttributeConfigIndex: "0",
			},
		}

		service.registerModelsForAuth(context.Background(), auth)
		if models := reg.GetModelsForClient(authID); len(models) != 0 {
			t.Fatalf("registered models = %+v, want empty", models)
		}
	})

	t.Run("stale index falls back with registration cache", func(t *testing.T) {
		authID := "openai-compat-stale-index-cache"
		reg.UnregisterClient(authID)
		t.Cleanup(func() { reg.UnregisterClient(authID) })

		service := &Service{cfg: &config.Config{OpenAICompatibility: []config.OpenAICompatibility{
			{Name: "other", BaseURL: "https://other.example.com/v1", Models: []config.OpenAICompatibilityModel{{Name: "wrong-model"}}},
			{Name: "target", BaseURL: "https://target.example.com/v1", Models: []config.OpenAICompatibilityModel{{Name: "right-model"}}},
		}}}
		cache := service.newOpenAICompatibilityRegistrationCache()
		auth := &coreauth.Auth{
			ID:       authID,
			Provider: "target",
			Status:   coreauth.StatusActive,
			Attributes: map[string]string{
				"compat_name":                 "target",
				"provider_key":                "target",
				"base_url":                    "https://target.example.com/v1",
				coreauth.AttributeSource:      "config:target[token-1]",
				coreauth.AttributeConfigIndex: "0",
			},
		}

		service.registerModelsForAuthWithCache(context.Background(), auth, cache)
		models := reg.GetModelsForClient(authID)
		if len(models) != 1 || models[0].ID != "right-model" {
			t.Fatalf("registered models with cache = %+v, want [right-model]", models)
		}
	})

	t.Run("stale index with shared compat name and cache falls back by base_url", func(t *testing.T) {
		authID := "openai-compat-stale-index-shared-cache"
		reg.UnregisterClient(authID)
		t.Cleanup(func() { reg.UnregisterClient(authID) })

		service := &Service{cfg: &config.Config{OpenAICompatibility: []config.OpenAICompatibility{
			{Name: "shared", BaseURL: "https://other.example.com/v1", Models: []config.OpenAICompatibilityModel{{Name: "wrong-model"}}},
			{Name: "shared", BaseURL: "https://target.example.com/v1", Models: []config.OpenAICompatibilityModel{{Name: "right-model"}}},
		}}}
		cache := service.newOpenAICompatibilityRegistrationCache()
		auth := &coreauth.Auth{
			ID:       authID,
			Provider: "shared",
			Status:   coreauth.StatusActive,
			Attributes: map[string]string{
				"compat_name":                 "shared",
				"provider_key":                "shared",
				"base_url":                    "https://target.example.com/v1",
				coreauth.AttributeSource:      "config:shared[token-1]",
				coreauth.AttributeConfigIndex: "0",
			},
		}

		service.registerModelsForAuthWithCache(context.Background(), auth, cache)
		models := reg.GetModelsForClient(authID)
		if len(models) != 1 || models[0].ID != "right-model" {
			t.Fatalf("registered models with cache = %+v, want [right-model]", models)
		}
	})

	t.Run("deleted shared compat endpoint with different remaining base_url unregisters without cache", func(t *testing.T) {
		authID := "openai-compat-deleted-shared-nocache"
		reg.UnregisterClient(authID)
		reg.RegisterClient(authID, "shared", []*ModelInfo{{ID: "old-model"}})
		t.Cleanup(func() { reg.UnregisterClient(authID) })

		service := &Service{cfg: &config.Config{OpenAICompatibility: []config.OpenAICompatibility{
			{Name: "shared", BaseURL: "https://remaining.example.com/v1", Models: []config.OpenAICompatibilityModel{{Name: "wrong-model"}}},
		}}}
		auth := &coreauth.Auth{
			ID:       authID,
			Provider: "shared",
			Status:   coreauth.StatusActive,
			Attributes: map[string]string{
				"compat_name":                 "shared",
				"provider_key":                "shared",
				"base_url":                    "https://deleted.example.com/v1",
				coreauth.AttributeSource:      "config:shared[token-1]",
				coreauth.AttributeConfigIndex: "0",
			},
		}

		service.registerModelsForAuth(context.Background(), auth)
		if models := reg.GetModelsForClient(authID); len(models) != 0 {
			t.Fatalf("registered models = %+v, want empty", models)
		}
	})

	t.Run("deleted shared compat endpoint with different remaining base_url unregisters with cache", func(t *testing.T) {
		authID := "openai-compat-deleted-shared-cache"
		reg.UnregisterClient(authID)
		reg.RegisterClient(authID, "shared", []*ModelInfo{{ID: "old-model"}})
		t.Cleanup(func() { reg.UnregisterClient(authID) })

		service := &Service{cfg: &config.Config{OpenAICompatibility: []config.OpenAICompatibility{
			{Name: "shared", BaseURL: "https://remaining.example.com/v1", Models: []config.OpenAICompatibilityModel{{Name: "wrong-model"}}},
		}}}
		cache := service.newOpenAICompatibilityRegistrationCache()
		auth := &coreauth.Auth{
			ID:       authID,
			Provider: "shared",
			Status:   coreauth.StatusActive,
			Attributes: map[string]string{
				"compat_name":                 "shared",
				"provider_key":                "shared",
				"base_url":                    "https://deleted.example.com/v1",
				coreauth.AttributeSource:      "config:shared[token-1]",
				coreauth.AttributeConfigIndex: "0",
			},
		}

		service.registerModelsForAuthWithCache(context.Background(), auth, cache)
		if models := reg.GetModelsForClient(authID); len(models) != 0 {
			t.Fatalf("registered models with cache = %+v, want empty", models)
		}
	})

	t.Run("base_url path matching is case-sensitive while scheme and host remain case-insensitive", func(t *testing.T) {
		authID := "openai-compat-baseurl-case-sensitivity"
		reg.UnregisterClient(authID)
		t.Cleanup(func() { reg.UnregisterClient(authID) })

		service := &Service{cfg: &config.Config{OpenAICompatibility: []config.OpenAICompatibility{
			{Name: "shared", BaseURL: "https://example.com/TenantA", Models: []config.OpenAICompatibilityModel{{Name: "wrong-model"}}},
			{Name: "shared", BaseURL: "https://example.com/tenanta", Models: []config.OpenAICompatibilityModel{{Name: "right-model"}}},
		}}}
		cache := service.newOpenAICompatibilityRegistrationCache()
		auth := &coreauth.Auth{
			ID:       authID,
			Provider: "shared",
			Status:   coreauth.StatusActive,
			Attributes: map[string]string{
				"compat_name":                 "shared",
				"provider_key":                "shared",
				"base_url":                    "HTTPS://EXAMPLE.COM/tenanta",
				coreauth.AttributeSource:      "config:shared[token-1]",
				coreauth.AttributeConfigIndex: "0",
			},
		}

		service.registerModelsForAuthWithCache(context.Background(), auth, cache)
		models := reg.GetModelsForClient(authID)
		if len(models) != 1 || models[0].ID != "right-model" {
			t.Fatalf("registered models = %+v, want [right-model]", models)
		}
	})
}

func TestResolveConfigAPIKeyStrictConfigIndexAndCaseSensitivity(t *testing.T) {
	t.Run("claude deleted endpoint with same api_key does not bind to remaining endpoint", func(t *testing.T) {
		service := &Service{cfg: &config.Config{ClaudeKey: []config.ClaudeKey{
			{APIKey: "shared-key", BaseURL: "https://remaining.example.com", Models: []config.ClaudeModel{{Name: "wrong"}}},
		}}}
		auth := &coreauth.Auth{Attributes: map[string]string{
			coreauth.AttributeAPIKey:      "shared-key",
			"base_url":                    "https://deleted.example.com",
			coreauth.AttributeSource:      "config:claude[token-1]",
			coreauth.AttributeConfigIndex: "0",
		}}

		if entry := service.resolveConfigClaudeKey(auth); entry != nil {
			t.Fatalf("resolved Claude entry = %+v, want nil", entry)
		}
	})

	t.Run("vertex deleted endpoint with same api_key does not bind to remaining endpoint", func(t *testing.T) {
		service := &Service{cfg: &config.Config{VertexCompatAPIKey: []config.VertexCompatKey{
			{APIKey: "shared-key", BaseURL: "https://remaining.example.com", Models: []config.VertexCompatModel{{Name: "wrong"}}},
		}}}
		auth := &coreauth.Auth{Attributes: map[string]string{
			coreauth.AttributeAPIKey:      "shared-key",
			"base_url":                    "https://deleted.example.com",
			coreauth.AttributeSource:      "config:vertex-apikey[token-1]",
			coreauth.AttributeConfigIndex: "0",
		}}

		if entry := service.resolveConfigVertexCompatKey(auth); entry != nil {
			t.Fatalf("resolved Vertex entry = %+v, want nil", entry)
		}
	})

	t.Run("gemini deleted custom base_url endpoint does not bind to empty base_url entry", func(t *testing.T) {
		service := &Service{cfg: &config.Config{GeminiKey: []config.GeminiKey{
			{APIKey: "shared-key", BaseURL: "", Models: []config.GeminiModel{{Name: "wrong"}}},
		}}}
		auth := &coreauth.Auth{
			Provider: constant.Gemini,
			Attributes: map[string]string{
				coreauth.AttributeAPIKey:      "shared-key",
				"base_url":                    "https://deleted.example.com",
				coreauth.AttributeSource:      "config:gemini[token-1]",
				coreauth.AttributeConfigIndex: "0",
			},
		}

		if entry := service.resolveConfigGeminiKey(auth); entry != nil {
			t.Fatalf("resolved Gemini entry = %+v, want nil", entry)
		}
	})

	t.Run("xai deleted custom base_url endpoint does not bind to empty base_url entry", func(t *testing.T) {
		service := &Service{cfg: &config.Config{XAIKey: []config.XAIKey{
			{APIKey: "shared-key", BaseURL: "", Models: []config.CodexModel{{Name: "wrong"}}},
		}}}
		auth := &coreauth.Auth{Attributes: map[string]string{
			coreauth.AttributeAPIKey:      "shared-key",
			"base_url":                    "https://deleted.example.com",
			coreauth.AttributeSource:      "config:xai[token-1]",
			coreauth.AttributeConfigIndex: "0",
		}}

		if entry := service.resolveConfigXAIKey(auth); entry != nil {
			t.Fatalf("resolved xAI entry = %+v, want nil", entry)
		}
	})

	t.Run("api_key matching is case-sensitive and base_url path matching is case-sensitive", func(t *testing.T) {
		service := &Service{cfg: &config.Config{ClaudeKey: []config.ClaudeKey{
			{APIKey: "KeyA", BaseURL: "https://example.com/TenantA", Models: []config.ClaudeModel{{Name: "wrong-key-case"}}},
			{APIKey: "keya", BaseURL: "https://example.com/TenantA", Models: []config.ClaudeModel{{Name: "wrong-path-case"}}},
			{APIKey: "keya", BaseURL: "https://example.com/tenanta", Models: []config.ClaudeModel{{Name: "right"}}},
		}}}
		auth := &coreauth.Auth{Attributes: map[string]string{
			coreauth.AttributeAPIKey:      "keya",
			"base_url":                    "HTTPS://EXAMPLE.COM/tenanta",
			coreauth.AttributeSource:      "config:claude[token-1]",
			coreauth.AttributeConfigIndex: "0",
		}}

		entry := service.resolveConfigClaudeKey(auth)
		if entry == nil || len(entry.Models) != 1 || entry.Models[0].Name != "right" {
			t.Fatalf("resolved Claude entry = %+v, want exact case-matched entry", entry)
		}
	})

	t.Run("config-indexed keyless auth does not bind to keyed entry at same base_url", func(t *testing.T) {
		service := &Service{cfg: &config.Config{ClaudeKey: []config.ClaudeKey{
			{APIKey: "secret-key", BaseURL: "https://proxy.example.com", Models: []config.ClaudeModel{{Name: "keyed"}}},
			{APIKey: "", BaseURL: "https://proxy.example.com", Models: []config.ClaudeModel{{Name: "keyless"}}},
		}}}
		auth := &coreauth.Auth{Attributes: map[string]string{
			"base_url":                    "https://proxy.example.com",
			coreauth.AttributeSource:      "config:claude[token-1]",
			coreauth.AttributeConfigIndex: "0",
		}}

		entry := service.resolveConfigClaudeKey(auth)
		if entry == nil || len(entry.Models) != 1 || entry.Models[0].Name != "keyless" {
			t.Fatalf("resolved Claude entry = %+v, want keyless entry", entry)
		}
	})

	t.Run("non-indexed base_url-only auth keeps matching keyed config entry", func(t *testing.T) {
		service := &Service{cfg: &config.Config{
			ClaudeKey: []config.ClaudeKey{
				{APIKey: "secret-key", BaseURL: "https://proxy.example.com", Models: []config.ClaudeModel{{Name: "claude-custom"}}},
			},
			GeminiKey: []config.GeminiKey{
				{APIKey: "secret-key", BaseURL: "https://proxy.example.com", Models: []config.GeminiModel{{Name: "gemini-custom"}}},
			},
			VertexCompatAPIKey: []config.VertexCompatKey{
				{APIKey: "secret-key", BaseURL: "https://proxy.example.com", Models: []config.VertexCompatModel{{Name: "vertex-custom"}}},
			},
			XAIKey: []config.XAIKey{
				{APIKey: "secret-key", BaseURL: "https://proxy.example.com", Models: []config.CodexModel{{Name: "xai-custom"}}},
			},
		}}
		auth := &coreauth.Auth{Attributes: map[string]string{
			"base_url": "https://proxy.example.com",
		}}

		if entry := service.resolveConfigClaudeKey(auth); entry == nil || len(entry.Models) != 1 || entry.Models[0].Name != "claude-custom" {
			t.Fatalf("resolved non-indexed Claude entry = %+v, want claude-custom", entry)
		}
		if entry := service.resolveConfigGeminiKey(auth); entry == nil || len(entry.Models) != 1 || entry.Models[0].Name != "gemini-custom" {
			t.Fatalf("resolved non-indexed Gemini entry = %+v, want gemini-custom", entry)
		}
		if entry := service.resolveConfigVertexCompatKey(auth); entry == nil || len(entry.Models) != 1 || entry.Models[0].Name != "vertex-custom" {
			t.Fatalf("resolved non-indexed Vertex entry = %+v, want vertex-custom", entry)
		}
		if entry := service.resolveConfigXAIKey(auth); entry == nil || len(entry.Models) != 1 || entry.Models[0].Name != "xai-custom" {
			t.Fatalf("resolved non-indexed xAI entry = %+v, want xai-custom", entry)
		}
	})
}
