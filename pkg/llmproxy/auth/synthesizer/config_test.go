package synthesizer

import (
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/config"
	"testing"
	"time"
)

func TestConfigSynthesizer_Synthesize(t *testing.T) {
	s := NewConfigSynthesizer()
	ctx := &SynthesisContext{
		Config: &config.Config{
			ClaudeKey: []config.ClaudeKey{{APIKey: "k1", Prefix: "p1"}},
			GeminiKey: []config.GeminiKey{{APIKey: "g1"}},
		},
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}

	auths, err := s.Synthesize(ctx)
	if err != nil {
		t.Fatalf("Synthesize failed: %v", err)
	}

	if len(auths) != 2 {
		t.Errorf("expected 2 auth entries, got %d", len(auths))
	}

	foundClaude := false
	for _, a := range auths {
		if a.Provider == "claude" {
			foundClaude = true
			if a.Prefix != "p1" {
				t.Errorf("expected prefix p1, got %s", a.Prefix)
			}
			if a.Attributes["api_key"] != "k1" {
				t.Error("missing api_key attribute")
			}
		}
	}
	if !foundClaude {
		t.Error("claude auth not found")
	}
}

func TestConfigSynthesizer_SynthesizeOpenAICompat(t *testing.T) {
	s := NewConfigSynthesizer()
	ctx := &SynthesisContext{
		Config: &config.Config{
			OpenAICompatibility: []config.OpenAICompatibility{
				{
					Name:           "provider1",
					BaseURL:        "http://base",
					ModelsEndpoint: "/api/coding/paas/v4/models",
					APIKeyEntries:  []config.OpenAICompatibilityAPIKey{{APIKey: "k1"}},
				},
			},
		},
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}

	auths, err := s.Synthesize(ctx)
	if err != nil {
		t.Fatalf("Synthesize failed: %v", err)
	}

	if len(auths) != 1 || auths[0].Provider != "provider1" {
		t.Errorf("expected 1 auth for provider1, got %v", auths)
	}
	if got := auths[0].Attributes["models_endpoint"]; got != "/api/coding/paas/v4/models" {
		t.Fatalf("models_endpoint = %q, want %q", got, "/api/coding/paas/v4/models")
	}
}

func TestConfigSynthesizer_SynthesizeMore(t *testing.T) {
	s := NewConfigSynthesizer()
	ctx := &SynthesisContext{
		Config: &config.Config{
			CodexKey: []config.CodexKey{{APIKey: "co1"}},
			GeneratedConfig: config.GeneratedConfig{
				DeepSeekKey:    []config.DeepSeekKey{{APIKey: "ds1"}},
				GroqKey:        []config.GroqKey{{APIKey: "gr1"}},
				MistralKey:     []config.MistralKey{{APIKey: "mi1"}},
				SiliconFlowKey: []config.SiliconFlowKey{{APIKey: "sf1"}},
				OpenRouterKey:  []config.OpenRouterKey{{APIKey: "or1"}},
				TogetherKey:    []config.TogetherKey{{APIKey: "to1"}},
				FireworksKey:   []config.FireworksKey{{APIKey: "fw1"}},
				NovitaKey:      []config.NovitaKey{{APIKey: "no1"}},
				MiniMaxKey:     []config.MiniMaxKey{{APIKey: "mm1"}},
				RooKey:         []config.RooKey{{APIKey: "ro1"}},
				KiloKey:        []config.KiloKey{{APIKey: "ki1"}},
			},
			VertexCompatAPIKey: []config.VertexCompatKey{{APIKey: "vx1", BaseURL: "http://vx"}},
		},
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}

	auths, err := s.Synthesize(ctx)
	if err != nil {
		t.Fatalf("Synthesize failed: %v", err)
	}

	expectedProviders := map[string]bool{
		"codex":       true,
		"deepseek":    true,
		"groq":        true,
		"mistral":     true,
		"siliconflow": true,
		"openrouter":  true,
		"together":    true,
		"fireworks":   true,
		"novita":      true,
		"minimax":     true,
		"roo":         true,
		"kilo":        true,
		"vertex":      true,
	}

	for _, a := range auths {
		delete(expectedProviders, a.Provider)
	}

	if len(expectedProviders) > 0 {
		t.Errorf("missing providers in synthesis: %v", expectedProviders)
	}
}
