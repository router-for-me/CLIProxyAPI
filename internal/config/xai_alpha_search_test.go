package config

import "testing"

func TestSanitizeXAIKeysClearsCodexAlphaSearchCapability(t *testing.T) {
	cfg := &Config{XAIKey: []XAIKey{{
		APIKey:      "xai-key",
		BaseURL:     "https://api.x.ai/v1",
		AlphaSearch: true,
	}}}

	cfg.SanitizeXAIKeys()

	if len(cfg.XAIKey) != 1 {
		t.Fatalf("XAI key count = %d, want 1", len(cfg.XAIKey))
	}
	if cfg.XAIKey[0].AlphaSearch {
		t.Fatal("SanitizeXAIKeys() retained the Codex-only alpha-search capability")
	}
}
