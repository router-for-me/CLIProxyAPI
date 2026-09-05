package config

import "testing"

func TestMetaConfigDropsUnusableKeys(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`meta-api-key:
  - {}
  - api-key: "   "
  - base-url: "https://api.meta.ai/v1"
  - headers: {X-Trace: placeholder}
  - api-key: " LLM|valid "
  - api-key: "dca:requires-oauth-storage"
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.MetaKey) != 1 {
		t.Fatalf("got %d keys, want only the 1 valid API key (DCA tokens require OAuth storage)", len(cfg.MetaKey))
	}
	if cfg.MetaKey[0].APIKey != "LLM|valid" || cfg.MetaKey[0].BaseURL != "https://api.meta.ai/v1" {
		t.Fatalf("valid key not normalized: %#v", cfg.MetaKey[0])
	}
}
