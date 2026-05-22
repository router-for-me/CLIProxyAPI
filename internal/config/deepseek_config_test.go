package config

import "testing"

func TestParseConfigBytesDeepSeekKeys(t *testing.T) {
	cfg, err := ParseConfigBytes([]byte(`
deepseek-api-key:
  - api-key: " token-1 "
    prefix: " team-a "
    base-url: " https://deepseek.example "
    proxy-url: " http://proxy.example "
    headers:
      X-Test: " value "
    excluded-models:
      - " DeepSeek-V4-Pro "
    models:
      - name: deepseek-v4-pro
        alias: ds-pro
  - api-key: "token-1"
    base-url: "https://deepseek.example"
  - api-key: " "
`))
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if len(cfg.DeepSeekKey) != 1 {
		t.Fatalf("len(DeepSeekKey) = %d, want 1", len(cfg.DeepSeekKey))
	}
	entry := cfg.DeepSeekKey[0]
	if entry.APIKey != "token-1" {
		t.Fatalf("APIKey = %q", entry.APIKey)
	}
	if entry.Prefix != "team-a" {
		t.Fatalf("Prefix = %q", entry.Prefix)
	}
	if entry.BaseURL != "https://deepseek.example" {
		t.Fatalf("BaseURL = %q", entry.BaseURL)
	}
	if entry.Headers["X-Test"] != "value" {
		t.Fatalf("header = %q", entry.Headers["X-Test"])
	}
	if len(entry.ExcludedModels) != 1 || entry.ExcludedModels[0] != "deepseek-v4-pro" {
		t.Fatalf("ExcludedModels = %#v", entry.ExcludedModels)
	}
	if entry.Models[0].GetAlias() != "ds-pro" || entry.Models[0].GetName() != "deepseek-v4-pro" {
		t.Fatalf("model alias = %#v", entry.Models[0])
	}
}
