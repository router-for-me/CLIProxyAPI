package config

import "testing"

func TestMetaConfigDropsUnusableKeys(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`meta-api-key:
  - {}
  - api-key: "   "
  - base-url: "https://api.meta.ai/v1"
  - headers: {X-Trace: placeholder}
  - api-key: " LLM|valid "
  - api-key: "dca:recoverable"
`))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.MetaKey) != 2 {
		t.Fatalf("got %d keys, want only the two usable credentials", len(cfg.MetaKey))
	}
	if cfg.MetaKey[0].APIKey != "LLM|valid" || cfg.MetaKey[0].BaseURL != "https://api.meta.ai/v1" {
		t.Fatalf("valid key not normalized: %#v", cfg.MetaKey[0])
	}
}
