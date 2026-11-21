package synthesizer

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func intPointer(value int) *int {
	return &value
}

// The per-credential inherited-quota cooldown cap must survive config -> Auth
// synthesis for every static api-key provider kind, so operators can bound a
// cascaded gateway's long upstream Retry-After regardless of provider.
func TestConfigSynthesizerPropagatesMaxQuotaCooldownSeconds(t *testing.T) {
	const capSeconds = 120
	tests := []struct {
		name string
		cfg  *config.Config
	}{
		{
			name: "claude",
			cfg: &config.Config{ClaudeKey: []config.ClaudeKey{{
				APIKey:                  "claude-key",
				MaxQuotaCooldownSeconds: intPointer(capSeconds),
			}}},
		},
		{
			name: "codex",
			cfg: &config.Config{CodexKey: []config.CodexKey{{
				APIKey:                  "codex-key",
				BaseURL:                 "https://codex.example.com",
				MaxQuotaCooldownSeconds: intPointer(capSeconds),
			}}},
		},
		{
			name: "gemini",
			cfg: &config.Config{GeminiKey: []config.GeminiKey{{
				APIKey:                  "gemini-key",
				MaxQuotaCooldownSeconds: intPointer(capSeconds),
			}}},
		},
		{
			name: "openai compatibility",
			cfg: &config.Config{OpenAICompatibility: []config.OpenAICompatibility{{
				Name:                    "compat",
				BaseURL:                 "https://compat.example.com",
				MaxQuotaCooldownSeconds: intPointer(capSeconds),
				APIKeyEntries:           []config.OpenAICompatibilityAPIKey{{APIKey: "compat-key"}},
			}}},
		},
		{
			name: "vertex",
			cfg: &config.Config{VertexCompatAPIKey: []config.VertexCompatKey{{
				APIKey:                  "vertex-key",
				BaseURL:                 "https://vertex.example.com",
				MaxQuotaCooldownSeconds: intPointer(capSeconds),
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
			got, ok := auths[0].MaxQuotaCooldownSeconds()
			if !ok || got != capSeconds {
				t.Fatalf("MaxQuotaCooldownSeconds() = %d, %t, want %d, true", got, ok, capSeconds)
			}
		})
	}
}

// When the cap is omitted it must not appear in metadata, preserving default behavior.
func TestConfigSynthesizerOmitsMaxQuotaCooldownWhenUnset(t *testing.T) {
	cfg := &config.Config{ClaudeKey: []config.ClaudeKey{{APIKey: "claude-key"}}}
	auths, errSynthesize := NewConfigSynthesizer().Synthesize(&SynthesisContext{
		Config:      cfg,
		Now:         time.Unix(100, 0).UTC(),
		IDGenerator: NewStableIDGenerator(),
	})
	if errSynthesize != nil {
		t.Fatalf("Synthesize() error = %v", errSynthesize)
	}
	if len(auths) != 1 {
		t.Fatalf("auth count = %d, want 1", len(auths))
	}
	if _, ok := auths[0].MaxQuotaCooldownSeconds(); ok {
		t.Fatal("expected no max_quota_cooldown_seconds metadata when field is unset")
	}
}
