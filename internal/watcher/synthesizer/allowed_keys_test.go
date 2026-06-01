package synthesizer

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// TestSynthesizeOpenAICompat_AllowedKeysAttribute verifies that a provider's allowed-keys are
// stamped onto the synthesized auth's "allowed_keys" attribute, and that public providers
// (no allowed-keys) carry no such attribute.
func TestSynthesizeOpenAICompat_AllowedKeysAttribute(t *testing.T) {
	cfg := &config.Config{}
	cfg.OpenAICompatibility = []config.OpenAICompatibility{
		{
			Name:          "linkapi",
			BaseURL:       "https://api.linkapi.ai/v1",
			AllowedKeys:   []string{"team-a", "team-b"},
			APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "sk-linkapi"}},
			Models:        []config.OpenAICompatibilityModel{{Name: "gpt-5.5", Alias: "gpt-5.5"}},
		},
		{
			Name:          "public",
			BaseURL:       "https://example.com/v1",
			APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "sk-pub"}},
			Models:        []config.OpenAICompatibilityModel{{Name: "m1", Alias: "m1"}},
		},
	}

	synth := NewConfigSynthesizer()
	ctx := &SynthesisContext{Config: cfg, Now: time.Now(), IDGenerator: NewStableIDGenerator()}
	auths, err := synth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("synthesize: %v", err)
	}

	byName := func(name string) *coreauth.Auth {
		for _, a := range auths {
			if a != nil && a.Attributes["compat_name"] == name {
				return a
			}
		}
		return nil
	}

	private := byName("linkapi")
	if private == nil {
		t.Fatal("expected synthesized auth for linkapi")
	}
	if got := private.Attributes["allowed_keys"]; got != "team-a,team-b" {
		t.Fatalf("linkapi allowed_keys attribute = %q, want %q", got, "team-a,team-b")
	}

	public := byName("public")
	if public == nil {
		t.Fatal("expected synthesized auth for public provider")
	}
	if got, ok := public.Attributes["allowed_keys"]; ok {
		t.Fatalf("public provider should have no allowed_keys attribute, got %q", got)
	}
}
