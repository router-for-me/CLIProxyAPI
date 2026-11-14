package synthesizer

import (
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func boolPointer(value bool) *bool {
	return &value
}

func modelPipelineCompatModel(routeChannel, modelID string) config.OpenAICompatibilityModel {
	return config.OpenAICompatibilityModel{
		Name:                   modelID,
		Alias:                  modelID,
		CatalogProviderID:      routeChannel,
		CatalogModelID:         modelID,
		CatalogRouteProviderID: routeChannel,
		CatalogRouteModelID:    modelID,
		Protocols:              []string{"openai_chat"},
	}
}

func modelPipelineOpenAICompatibility(name, baseURL string, apiKeys ...string) config.OpenAICompatibility {
	canonicalName := strings.ToLower(strings.TrimSpace(name))
	routeChannel := "openai-compatible-" + canonicalName
	entries := make([]config.OpenAICompatibilityAPIKey, len(apiKeys))
	for index, apiKey := range apiKeys {
		entries[index] = config.OpenAICompatibilityAPIKey{APIKey: apiKey, QuotaDomain: routeChannel}
	}
	return config.OpenAICompatibility{
		Name:          name,
		BaseURL:       baseURL,
		RouteChannel:  routeChannel,
		APIKeyEntries: entries,
		Models:        []config.OpenAICompatibilityModel{modelPipelineCompatModel(routeChannel, canonicalName+"-model")},
	}
}

func TestConfigSynthesizerPreservesExplicitFalseCoolingOverrides(t *testing.T) {
	disableCooling := false
	compat := modelPipelineOpenAICompatibility("compat", "https://compat.example.com", "compat-key")
	compat.DisableCooling = &disableCooling
	tests := []struct {
		name string
		cfg  *config.Config
	}{
		{
			name: "gemini",
			cfg: &config.Config{GeminiKey: []config.GeminiKey{{
				APIKey:         "gemini-key",
				DisableCooling: &disableCooling,
			}}},
		},
		{
			name: "interactions",
			cfg: &config.Config{InteractionsKey: []config.GeminiKey{{
				APIKey:         "interactions-key",
				DisableCooling: &disableCooling,
			}}},
		},
		{
			name: "claude",
			cfg: &config.Config{ClaudeKey: []config.ClaudeKey{{
				APIKey:         "claude-key",
				DisableCooling: &disableCooling,
			}}},
		},
		{
			name: "codex",
			cfg: &config.Config{CodexKey: []config.CodexKey{{
				APIKey:         "codex-key",
				BaseURL:        "https://codex.example.com",
				DisableCooling: &disableCooling,
			}}},
		},
		{
			name: "xai",
			cfg: &config.Config{XAIKey: []config.XAIKey{{
				APIKey:         "xai-key",
				BaseURL:        "https://api.x.ai/v1",
				DisableCooling: &disableCooling,
			}}},
		},
		{
			name: "openai compatibility",
			cfg:  &config.Config{OpenAICompatibility: []config.OpenAICompatibility{compat}},
		},
		{
			name: "vertex",
			cfg: &config.Config{VertexCompatAPIKey: []config.VertexCompatKey{{
				APIKey:         "vertex-key",
				BaseURL:        "https://vertex.example.com",
				DisableCooling: &disableCooling,
			}}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			auths, errSynthesize := NewConfigSynthesizer().Synthesize(&SynthesisContext{
				Config:      tc.cfg,
				Now:         time.Unix(100, 0).UTC(),
				IDGenerator: NewStableIDGenerator(),
			})
			if errSynthesize != nil {
				t.Fatalf("Synthesize() error = %v", errSynthesize)
			}
			if len(auths) != 1 {
				t.Fatalf("auth count = %d, want 1", len(auths))
			}
			disabled, present := auths[0].DisableCoolingOverride()
			if !present || disabled {
				t.Fatalf("DisableCoolingOverride() = %t, %t, want false, true", disabled, present)
			}
		})
	}
}
