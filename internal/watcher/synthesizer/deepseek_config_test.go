package synthesizer

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestConfigSynthesizerDeepSeekKeys(t *testing.T) {
	synth := NewConfigSynthesizer()
	ctx := &SynthesisContext{
		Config: &config.Config{
			DeepSeekKey: []config.DeepSeekKey{{
				APIKey:         "token-1",
				Prefix:         "team-a",
				BaseURL:        "https://deepseek.example",
				ProxyURL:       "http://proxy.example",
				Priority:       7,
				DisableCooling: true,
				Headers:        map[string]string{"X-Test": "value"},
				ExcludedModels: []string{"deepseek-v4-pro"},
				Models:         []config.DeepSeekModel{{Name: "deepseek-v4-pro", Alias: "ds-pro"}},
			}},
		},
		Now:         time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		IDGenerator: NewStableIDGenerator(),
	}
	auths, err := synth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	if len(auths) != 1 {
		t.Fatalf("len(auths) = %d, want 1", len(auths))
	}
	auth := auths[0]
	if auth.Provider != "deepseek" {
		t.Fatalf("Provider = %q", auth.Provider)
	}
	if auth.Status != coreauth.StatusActive {
		t.Fatalf("Status = %q", auth.Status)
	}
	if auth.Attributes["api_key"] != "token-1" {
		t.Fatalf("api_key = %q", auth.Attributes["api_key"])
	}
	if auth.Attributes["base_url"] != "https://deepseek.example" {
		t.Fatalf("base_url = %q", auth.Attributes["base_url"])
	}
	if auth.Attributes["priority"] != "7" {
		t.Fatalf("priority = %q", auth.Attributes["priority"])
	}
	if auth.Attributes["header:X-Test"] != "value" {
		t.Fatalf("header = %q", auth.Attributes["header:X-Test"])
	}
	if auth.Attributes["models_hash"] == "" {
		t.Fatal("models_hash is empty")
	}
	if auth.ProxyURL != "http://proxy.example" {
		t.Fatalf("ProxyURL = %q", auth.ProxyURL)
	}
	if disabled, _ := auth.Metadata["disable_cooling"].(bool); !disabled {
		t.Fatalf("disable_cooling = %v", auth.Metadata["disable_cooling"])
	}
}
