package config

import "testing"

func TestParseConfigBytesXAIAPIKeyMatchesCodexShape(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte(`xai-api-key:
  - api-key: " xai-key "
    priority: 3
    prefix: " team-xai "
    base-url: " https://api.x.ai/v1 "
    websockets: true
    proxy-url: " http://proxy.local "
    headers:
      X-Custom: value
    models:
      - name: grok-4.5
        alias: grok-latest
        display-name: Grok Latest
        force-mapping: true
    excluded-models:
      - " grok-3-* "
    disable-cooling: true
  - api-key: dropped
    base-url: " "
`))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if len(cfg.XAIKey) != 1 {
		t.Fatalf("xai-api-key count = %d, want 1", len(cfg.XAIKey))
	}
	entry := cfg.XAIKey[0]
	if entry.APIKey != " xai-key " {
		t.Fatalf("api-key = %q, want original Codex-compatible value", entry.APIKey)
	}
	if entry.Priority != 3 {
		t.Fatalf("priority = %d, want 3", entry.Priority)
	}
	if entry.Prefix != "team-xai" {
		t.Fatalf("prefix = %q, want team-xai", entry.Prefix)
	}
	if entry.BaseURL != "https://api.x.ai/v1" {
		t.Fatalf("base-url = %q, want https://api.x.ai/v1", entry.BaseURL)
	}
	if !entry.Websockets {
		t.Fatal("websockets = false, want true")
	}
	if entry.ProxyURL != " http://proxy.local " {
		t.Fatalf("proxy-url = %q, want original Codex-compatible value", entry.ProxyURL)
	}
	if !entry.DisableCooling {
		t.Fatal("disable-cooling = false, want true")
	}
	if entry.Headers["X-Custom"] != "value" {
		t.Fatalf("X-Custom header = %q, want value", entry.Headers["X-Custom"])
	}
	if len(entry.Models) != 1 {
		t.Fatalf("model count = %d, want 1", len(entry.Models))
	}
	model := entry.Models[0]
	if model.Name != "grok-4.5" || model.Alias != "grok-latest" || model.DisplayName != "Grok Latest" || !model.ForceMapping {
		t.Fatalf("unexpected model mapping: %+v", model)
	}
	if len(entry.ExcludedModels) != 1 || entry.ExcludedModels[0] != "grok-3-*" {
		t.Fatalf("excluded-models = %#v, want [grok-3-*]", entry.ExcludedModels)
	}
}
