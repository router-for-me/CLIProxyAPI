package synthesizer

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func boolPointer(value bool) *bool {
	return &value
}

func TestConfigSynthesizerPreservesExplicitFalseCoolingOverrides(t *testing.T) {
	disableCooling := false
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
			cfg: &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{
				Name:           "compat",
				BaseURL:        "https://compat.example.com",
				DisableCooling: &disableCooling,
				APIKeyEntries:  []config.OpenAICompatibilityAPIKey{{APIKey: "compat-key"}},
			}}},
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
