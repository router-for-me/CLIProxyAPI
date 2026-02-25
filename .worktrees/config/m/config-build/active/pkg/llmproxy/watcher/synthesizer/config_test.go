package synthesizer

import (
	"github.com/router-for-me/CLIProxyAPI/v6/pkg/llmproxy/config"
	"os"
	"path/filepath"
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
			CodexKey:           []config.CodexKey{{APIKey: "co1"}},
			VertexCompatAPIKey: []config.VertexCompatKey{{APIKey: "vx1", BaseURL: "http://vx"}},
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

func TestConfigSynthesizer_SynthesizeKiroKeys_UsesRefreshTokenForIDWhenProfileArnMissing(t *testing.T) {
	s := NewConfigSynthesizer()
	ctx := &SynthesisContext{
		Config: &config.Config{
			KiroKey: []config.KiroKey{
				{AccessToken: "shared-access-token", RefreshToken: "refresh-one"},
				{AccessToken: "shared-access-token", RefreshToken: "refresh-two"},
			},
		},
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}

	auths, err := s.Synthesize(ctx)
	if err != nil {
		t.Fatalf("Synthesize failed: %v", err)
	}
	if len(auths) != 2 {
		t.Fatalf("expected 2 auth entries, got %d", len(auths))
	}
	if auths[0].ID == auths[1].ID {
		t.Fatalf("expected unique auth IDs for distinct refresh tokens, got %q", auths[0].ID)
	}
}

func TestConfigSynthesizer_SynthesizeCursorKeys_FromTokenFile(t *testing.T) {
	s := NewConfigSynthesizer()
	tokenDir := t.TempDir()
	tokenPath := filepath.Join(tokenDir, "cursor-token.txt")
	if err := os.WriteFile(tokenPath, []byte("sk-cursor-test"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}

	ctx := &SynthesisContext{
		Config: &config.Config{
			CursorKey: []config.CursorKey{
				{
					TokenFile:    tokenPath,
					CursorAPIURL: "http://127.0.0.1:3010/",
					ProxyURL:     "http://127.0.0.1:7890",
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
	if len(auths) != 1 {
		t.Fatalf("expected 1 auth entry, got %d", len(auths))
	}

	got := auths[0]
	if got.Provider != "cursor" {
		t.Fatalf("provider = %q, want %q", got.Provider, "cursor")
	}
	if got.Attributes["api_key"] != "sk-cursor-test" {
		t.Fatalf("api_key = %q, want %q", got.Attributes["api_key"], "sk-cursor-test")
	}
	if got.Attributes["base_url"] != "http://127.0.0.1:3010/v1" {
		t.Fatalf("base_url = %q, want %q", got.Attributes["base_url"], "http://127.0.0.1:3010/v1")
	}
	if got.ProxyURL != "http://127.0.0.1:7890" {
		t.Fatalf("proxy_url = %q, want %q", got.ProxyURL, "http://127.0.0.1:7890")
	}
}

func TestConfigSynthesizer_SynthesizeCursorKeys_InvalidTokenFileIsSkipped(t *testing.T) {
	s := NewConfigSynthesizer()
	tokenDir := t.TempDir()
	tokenPath := filepath.Join(tokenDir, "cursor-token.txt")
	if err := os.WriteFile(tokenPath, []byte("invalid-token"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}

	ctx := &SynthesisContext{
		Config: &config.Config{
			CursorKey: []config.CursorKey{
				{
					TokenFile: tokenPath,
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
	if len(auths) != 0 {
		t.Fatalf("expected invalid cursor token file to be skipped, got %d auth entries", len(auths))
	}
}
