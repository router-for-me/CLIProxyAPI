package synthesizer

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestConfigSynthesizer_OpenAICompatDisabledKey(t *testing.T) {
	synth := NewConfigSynthesizer()
	ctx := &SynthesisContext{
		Config: &config.Config{
			OpenAICompatibility: []config.OpenAICompatibility{
				{
					Name:    "CustomProvider",
					BaseURL: "https://custom.api.com",
					APIKeyEntries: []config.OpenAICompatibilityAPIKey{
						{APIKey: "key-1", Disabled: true},
						{APIKey: "key-2"},
					},
				},
			},
		},
		Now:         time.Now(),
		IDGenerator: NewStableIDGenerator(),
	}

	auths, err := synth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(auths) != 2 {
		t.Fatalf("expected 2 auths, got %d", len(auths))
	}
	if !auths[0].Disabled || auths[0].Status != coreauth.StatusDisabled {
		t.Fatalf("expected first auth disabled, got disabled=%v status=%s", auths[0].Disabled, auths[0].Status)
	}
	if auths[1].Disabled || auths[1].Status != coreauth.StatusActive {
		t.Fatalf("expected second auth active, got disabled=%v status=%s", auths[1].Disabled, auths[1].Status)
	}
}
