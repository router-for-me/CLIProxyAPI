package cliproxy

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
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
	service := &Service{cfg: &config.Config{ClaudeKey: []config.ClaudeKey{
		{APIKey: "key-b", BaseURL: "https://b.example", Models: []config.ClaudeModel{{Name: "model-b"}}, ExcludedModels: []string{"excl-b"}},
		{APIKey: "key-a", BaseURL: "https://a.example", Models: []config.ClaudeModel{{Name: "model-a"}}, ExcludedModels: []string{"excl-a"}},
	}}}
	auth := &coreauth.Auth{Attributes: map[string]string{
		coreauth.AttributeAPIKey:      "key-a",
		"base_url":                    "https://a.example",
		coreauth.AttributeSource:      "config:claude[token-0]",
		coreauth.AttributeConfigIndex: "0",
	}}

	entry := service.resolveConfigClaudeKey(auth)
	if entry == nil || entry.APIKey != "key-a" || len(entry.Models) != 1 || entry.Models[0].Name != "model-a" {
		t.Fatalf("resolved Claude entry = %+v, want key-a/model-a", entry)
	}
}

func TestResolveConfigGeminiKeyStaleConfigIndexFallsBack(t *testing.T) {
	service := &Service{cfg: &config.Config{GeminiKey: []config.GeminiKey{
		{APIKey: "gem-b", BaseURL: "https://b.example", ExcludedModels: []string{"excl-b"}},
		{APIKey: "gem-a", BaseURL: "https://a.example", ExcludedModels: []string{"excl-a"}},
	}}}
	auth := &coreauth.Auth{Attributes: map[string]string{
		coreauth.AttributeAPIKey:      "gem-a",
		"base_url":                    "https://a.example",
		coreauth.AttributeSource:      "config:gemini[token-0]",
		coreauth.AttributeConfigIndex: "0",
	}}

	entry := service.resolveConfigGeminiKey(auth)
	if entry == nil || entry.APIKey != "gem-a" || len(entry.ExcludedModels) != 1 || entry.ExcludedModels[0] != "excl-a" {
		t.Fatalf("resolved Gemini entry = %+v, want gem-a/excl-a", entry)
	}
}

func TestResolveConfigVertexCompatKeyStaleConfigIndexFallsBack(t *testing.T) {
	service := &Service{cfg: &config.Config{VertexCompatAPIKey: []config.VertexCompatKey{
		{APIKey: "vtx-b", BaseURL: "https://b.example", Models: []config.VertexCompatModel{{Name: "model-b"}}},
		{APIKey: "vtx-a", BaseURL: "https://a.example", Models: []config.VertexCompatModel{{Name: "model-a"}}},
	}}}
	auth := &coreauth.Auth{Attributes: map[string]string{
		coreauth.AttributeAPIKey:      "vtx-a",
		"base_url":                    "https://a.example",
		coreauth.AttributeSource:      "config:vertex[token-0]",
		coreauth.AttributeConfigIndex: "0",
	}}

	entry := service.resolveConfigVertexCompatKey(auth)
	if entry == nil || entry.APIKey != "vtx-a" || len(entry.Models) != 1 || entry.Models[0].Name != "model-a" {
		t.Fatalf("resolved VertexCompat entry = %+v, want vtx-a/model-a", entry)
	}
}

func TestResolveConfigXAIKeyStaleConfigIndexFallsBack(t *testing.T) {
	service := &Service{cfg: &config.Config{XAIKey: []config.XAIKey{
		{APIKey: "xai-b", BaseURL: "https://b.example", ExcludedModels: []string{"excl-b"}},
		{APIKey: "xai-a", BaseURL: "https://a.example", ExcludedModels: []string{"excl-a"}},
	}}}
	auth := &coreauth.Auth{Attributes: map[string]string{
		coreauth.AttributeAPIKey:      "xai-a",
		"base_url":                    "https://a.example",
		coreauth.AttributeSource:      "config:xai[token-0]",
		coreauth.AttributeConfigIndex: "0",
	}}

	entry := service.resolveConfigXAIKey(auth)
	if entry == nil || entry.APIKey != "xai-a" || len(entry.ExcludedModels) != 1 || entry.ExcludedModels[0] != "excl-a" {
		t.Fatalf("resolved XAI entry = %+v, want xai-a/excl-a", entry)
	}
}

func TestResolveConfigMetaKeyStaleConfigIndexFallsBack(t *testing.T) {
	service := &Service{cfg: &config.Config{MetaKey: []config.MetaKey{
		{APIKey: "meta-b", BaseURL: "https://b.example", ExcludedModels: []string{"excl-b"}},
		{APIKey: "meta-a", BaseURL: "https://a.example", ExcludedModels: []string{"excl-a"}},
	}}}
	auth := &coreauth.Auth{Attributes: map[string]string{
		coreauth.AttributeAPIKey:      "meta-a",
		"base_url":                    "https://a.example",
		coreauth.AttributeSource:      "config:meta[token-0]",
		coreauth.AttributeConfigIndex: "0",
	}}

	entry := service.resolveConfigMetaKey(auth)
	if entry == nil || entry.APIKey != "meta-a" || len(entry.ExcludedModels) != 1 || entry.ExcludedModels[0] != "excl-a" {
		t.Fatalf("resolved Meta entry = %+v, want meta-a/excl-a", entry)
	}
}

func TestRegisterModelsForAuthOpenAICompatStaleConfigIndexFallsBack(t *testing.T) {
	cfg := &config.Config{OpenAICompatibility: []config.OpenAICompatibility{
		{Name: "provider-b", BaseURL: "https://b.example/v1", Models: []config.OpenAICompatibilityModel{{Name: "model-b"}}},
		{Name: "provider-a", BaseURL: "https://a.example/v1", Models: []config.OpenAICompatibilityModel{{Name: "model-a"}}},
	}}
	authID := "test-openai-compat-stale-config-index"
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(authID)
	})
	auth := &coreauth.Auth{
		ID:       authID,
		Provider: "provider-a",
		Attributes: map[string]string{
			coreauth.AttributeSource:      "config:provider-a[token-0]",
			coreauth.AttributeConfigIndex: "0",
			"compat_name":                 "provider-a",
			"base_url":                    "https://a.example/v1",
		},
	}
	service := &Service{cfg: cfg}
	service.registerModelsForAuthWithConfigAndCache(context.Background(), cfg, auth, nil)

	models := registry.GetGlobalRegistry().GetModelsForClient(authID)
	if len(models) != 1 || models[0].ID != "model-a" {
		t.Fatalf("registered models = %+v, want [model-a]", models)
	}
}

func TestRegisterModelsForAuthOpenAICompatCacheStaleConfigIndexFallsBack(t *testing.T) {
	cfg := &config.Config{OpenAICompatibility: []config.OpenAICompatibility{
		{Name: "provider-b", BaseURL: "https://b.example/v1", Models: []config.OpenAICompatibilityModel{{Name: "model-b"}}},
		{Name: "provider-a", BaseURL: "https://a.example/v1", Models: []config.OpenAICompatibilityModel{{Name: "model-a"}}},
	}}
	authID := "test-openai-compat-cache-stale-config-index"
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(authID)
	})
	auth := &coreauth.Auth{
		ID:       authID,
		Provider: "provider-a",
		Attributes: map[string]string{
			coreauth.AttributeSource:      "config:provider-a[token-0]",
			coreauth.AttributeConfigIndex: "0",
			"compat_name":                 "provider-a",
			"base_url":                    "https://a.example/v1",
		},
	}
	service := &Service{cfg: cfg}
	compatCache := newOpenAICompatibilityRegistrationCacheForConfig(cfg)
	service.registerModelsForAuthWithConfigAndCache(context.Background(), cfg, auth, compatCache)

	models := registry.GetGlobalRegistry().GetModelsForClient(authID)
	if len(models) != 1 || models[0].ID != "model-a" {
		t.Fatalf("registered models with compatCache = %+v, want [model-a]", models)
	}
}

func TestResolveConfigKeyDuplicateCredentialsDisambiguatesOnReorder(t *testing.T) {
	t.Run("claude duplicate credentials distinct prefix", func(t *testing.T) {
		service := &Service{cfg: &config.Config{ClaudeKey: []config.ClaudeKey{
			{APIKey: "shared-key", BaseURL: "https://api.example", Prefix: "team-b", Models: []config.ClaudeModel{{Name: "model-b"}}},
			{APIKey: "shared-key", BaseURL: "https://api.example", Prefix: "team-a", Models: []config.ClaudeModel{{Name: "model-a"}}},
		}}}
		auth := &coreauth.Auth{
			Prefix: "team-a",
			Attributes: map[string]string{
				coreauth.AttributeAPIKey:      "shared-key",
				"base_url":                    "https://api.example",
				coreauth.AttributeSource:      "config:claude[token-0]",
				coreauth.AttributeConfigIndex: "0",
			},
		}

		entry := service.resolveConfigClaudeKey(auth)
		if entry == nil || entry.Prefix != "team-a" || len(entry.Models) != 1 || entry.Models[0].Name != "model-a" {
			t.Fatalf("resolved Claude entry = %+v, want team-a/model-a", entry)
		}
	})

	t.Run("gemini duplicate credentials distinct proxy", func(t *testing.T) {
		service := &Service{cfg: &config.Config{GeminiKey: []config.GeminiKey{
			{APIKey: "shared-key", BaseURL: "https://gem.example", ProxyURL: "http://proxy-b:8080", ExcludedModels: []string{"excl-b"}},
			{APIKey: "shared-key", BaseURL: "https://gem.example", ProxyURL: "http://proxy-a:8080", ExcludedModels: []string{"excl-a"}},
		}}}
		auth := &coreauth.Auth{
			ProxyURL: "http://proxy-a:8080",
			Attributes: map[string]string{
				coreauth.AttributeAPIKey:      "shared-key",
				"base_url":                    "https://gem.example",
				coreauth.AttributeSource:      "config:gemini[token-0]",
				coreauth.AttributeConfigIndex: "0",
			},
		}

		entry := service.resolveConfigGeminiKey(auth)
		if entry == nil || entry.ProxyURL != "http://proxy-a:8080" || len(entry.ExcludedModels) != 1 || entry.ExcludedModels[0] != "excl-a" {
			t.Fatalf("resolved Gemini entry = %+v, want proxy-a/excl-a", entry)
		}
	})

	t.Run("openai compat cache duplicate name distinct prefix", func(t *testing.T) {
		cfg := &config.Config{OpenAICompatibility: []config.OpenAICompatibility{
			{Name: "shared", BaseURL: "https://shared.example/v1", Prefix: "team-b", Models: []config.OpenAICompatibilityModel{{Name: "model-b"}}},
			{Name: "shared", BaseURL: "https://shared.example/v1", Prefix: "team-a", Models: []config.OpenAICompatibilityModel{{Name: "model-a"}}},
		}}
		cache := newOpenAICompatibilityRegistrationCacheForConfig(cfg)
		auth := &coreauth.Auth{
			Prefix: "team-a",
			Attributes: map[string]string{
				coreauth.AttributeSource:      "config:shared[token-0]",
				coreauth.AttributeConfigIndex: "0",
				"compat_name":                 "shared",
				"base_url":                    "https://shared.example/v1",
			},
		}

		entry, ok := cache.lookup(auth, "shared")
		if !ok || entry == nil || len(entry.models) != 1 || entry.models[0].ID != "model-a" {
			t.Fatalf("cached compat entry = %+v, want model-a", entry)
		}
	})
}
