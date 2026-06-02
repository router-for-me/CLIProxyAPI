package synthesizer

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestSynthesizeCodexKeys_CompactResolution(t *testing.T) {
	cfg := &config.Config{
		CompactDefault: "deny",
		CodexKey: []config.CodexKey{
			{APIKey: "sk-on", Compact: "force_on"},
			{APIKey: "sk-off", Compact: "force_off"},
			{APIKey: "sk-auto"}, // auto -> follows deny
		},
	}
	synth := NewConfigSynthesizer()
	auths := synth.synthesizeCodexKeys(&SynthesisContext{
		Config: cfg, Now: time.Now(), IDGenerator: NewStableIDGenerator(),
	})
	got := map[string]string{}
	for _, a := range auths {
		got[a.Attributes["api_key"]] = a.Attributes["compact_allowed"]
	}
	if got["sk-on"] != "true" || got["sk-off"] != "false" || got["sk-auto"] != "false" {
		t.Fatalf("compact_allowed = %#v", got)
	}
}

func TestSynthesizeOpenAICompat_CompactResolution(t *testing.T) {
	cfg := &config.Config{
		CompactDefault: "allow",
		OpenAICompatibility: []config.OpenAICompatibility{
			{
				Name: "kimi", BaseURL: "https://example.com", Compact: "force_off",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{{APIKey: "k1"}},
			},
		},
	}
	synth := NewConfigSynthesizer()
	auths := synth.synthesizeOpenAICompat(&SynthesisContext{
		Config: cfg, Now: time.Now(), IDGenerator: NewStableIDGenerator(),
	})
	if len(auths) != 1 {
		t.Fatalf("want 1 auth, got %d", len(auths))
	}
	if auths[0].Attributes["compact_allowed"] != "false" {
		t.Fatalf("compact_allowed = %q, want false", auths[0].Attributes["compact_allowed"])
	}
}
