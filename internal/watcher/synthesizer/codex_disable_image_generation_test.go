package synthesizer

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestSynthesizeCodexKeys_DisableImageGeneration(t *testing.T) {
	s := NewConfigSynthesizer()
	cfg := &config.Config{
		CodexKey: []config.CodexKey{
			{APIKey: "sk-test-image-off", DisableImageGeneration: true},
			{APIKey: "sk-test-image-default"},
		},
	}
	auths, err := s.Synthesize(&SynthesisContext{Config: cfg, Now: time.Now(), IDGenerator: NewStableIDGenerator()})
	if err != nil {
		t.Fatalf("Synthesize error: %v", err)
	}
	var foundDisabled, foundDefault bool
	for _, a := range auths {
		if a == nil || a.Provider != "codex" {
			continue
		}
		key := a.Attributes["api_key"]
		switch key {
		case "sk-test-image-off":
			foundDisabled = true
			if a.Metadata == nil || a.Metadata["disable_image_generation"] != true {
				t.Fatalf("expected disable_image_generation=true metadata, got %#v", a.Metadata)
			}
			if !a.DisableImageGenerationOverride() {
				t.Fatal("expected DisableImageGenerationOverride() true")
			}
		case "sk-test-image-default":
			foundDefault = true
			if a.Metadata != nil {
				if _, ok := a.Metadata["disable_image_generation"]; ok {
					t.Fatalf("default key must not set disable_image_generation, got %#v", a.Metadata)
				}
			}
			if a.DisableImageGenerationOverride() {
				t.Fatal("default key override must be false")
			}
		}
	}
	if !foundDisabled || !foundDefault {
		t.Fatalf("missing auths disabled=%v default=%v total=%d", foundDisabled, foundDefault, len(auths))
	}
}
