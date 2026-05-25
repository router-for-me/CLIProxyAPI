package synthesizer

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
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

func TestConfigSynthesizer_OpenAICompatDisabledProviderKeepsDisabledAuths(t *testing.T) {
	synth := NewConfigSynthesizer()
	ctx := &SynthesisContext{
		Config: &config.Config{
			OpenAICompatibility: []config.OpenAICompatibility{
				{
					Name:     "CustomProvider",
					BaseURL:  "https://custom.api.com",
					Disabled: true,
					APIKeyEntries: []config.OpenAICompatibilityAPIKey{
						{APIKey: "key-1"},
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
		t.Fatalf("expected disabled provider auths to be retained, got %d", len(auths))
	}
	for i, auth := range auths {
		if !auth.Disabled || auth.Status != coreauth.StatusDisabled {
			t.Fatalf("expected auth %d disabled, got disabled=%v status=%s", i, auth.Disabled, auth.Status)
		}
	}
}
