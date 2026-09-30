package config

import "testing"

// TestSanitizeNeuralwattKeysClearsCodexAlphaSearchCapability mirrors the
// xai/meta sanitize behavior: Neuralwatt credentials must never carry the
// Codex-only alpha-search capability, even when set explicitly.
func TestSanitizeNeuralwattKeysClearsCodexAlphaSearchCapability(t *testing.T) {
	cfg := &Config{NeuralwattKey: []NeuralwattKey{{
		APIKey:      "neuralwatt-key",
		BaseURL:     "https://api.neuralwatt.com/v1",
		AlphaSearch: true,
	}}}

	cfg.SanitizeNeuralwattKeys()

	if len(cfg.NeuralwattKey) != 1 {
		t.Fatalf("neuralwatt-api-key count = %d, want 1", len(cfg.NeuralwattKey))
	}
	if cfg.NeuralwattKey[0].AlphaSearch {
		t.Fatal("SanitizeNeuralwattKeys() retained the Codex-only alpha-search capability")
	}
}

// TestParseConfigBytesNeuralwattAPIKeyMatchesCodexShape mirrors the
// xai-api-key parse/sanitize test: trailing-whitespace base URLs are
// trimmed, headers/excluded-models are normalized, and entries with
// empty base URLs are dropped — matching how SanitizeCodexKeyEntries
// normalizes the underlying CodexKey structure.
func TestParseConfigBytesNeuralwattAPIKeyMatchesCodexShape(t *testing.T) {
	cfg, errParse := ParseConfigBytes([]byte(`neuralwatt-api-key:
  - api-key: " neuralwatt-key "
    priority: 7
    weight: 4
    prefix: " team-nw "
    base-url: " https://api.neuralwatt.com/v1 "
    websockets: true
    proxy-url: " http://proxy.local "
    headers:
      X-Custom: value
    models:
      - name: muse-1
        alias: muse-latest
        display-name: Muse Latest
        force-mapping: true
    excluded-models:
      - " muse-0-* "
    disable-cooling: true
  - api-key: dropped
    base-url: " "
`))
	if errParse != nil {
		t.Fatalf("ParseConfigBytes() error = %v", errParse)
	}
	if len(cfg.NeuralwattKey) != 1 {
		t.Fatalf("neuralwatt-api-key count = %d, want 1", len(cfg.NeuralwattKey))
	}
	entry := cfg.NeuralwattKey[0]
	if entry.APIKey != " neuralwatt-key " {
		t.Fatalf("api-key = %q, want original Codex-compatible value", entry.APIKey)
	}
	if entry.Priority != 7 {
		t.Fatalf("priority = %d, want 7", entry.Priority)
	}
	if entry.Weight == nil || *entry.Weight != 4 {
		t.Fatalf("weight = %v, want 4", entry.Weight)
	}
	if entry.Prefix != "team-nw" {
		t.Fatalf("prefix = %q, want team-nw", entry.Prefix)
	}
	if entry.BaseURL != "https://api.neuralwatt.com/v1" {
		t.Fatalf("base-url = %q, want https://api.neuralwatt.com/v1", entry.BaseURL)
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
	if model.Name != "muse-1" || model.Alias != "muse-latest" || model.DisplayName != "Muse Latest" || !model.ForceMapping {
		t.Fatalf("unexpected model mapping: %+v", model)
	}
	if len(entry.ExcludedModels) != 1 || entry.ExcludedModels[0] != "muse-0-*" {
		t.Fatalf("excluded-models = %#v, want [muse-0-*]", entry.ExcludedModels)
	}
}

// TestSanitizeNeuralwattKeysDropsEntriesWithoutBaseURL ensures entries
// whose base-url normalizes to empty are removed, mirroring how
// SanitizeMetaKeys / SanitizeCodexKeys reject unconfigured endpoints.
func TestSanitizeNeuralwattKeysDropsEntriesWithoutBaseURL(t *testing.T) {
	cfg := &Config{NeuralwattKey: []NeuralwattKey{
		{APIKey: "keep", BaseURL: "  https://api.neuralwatt.com/v1  "},
		{APIKey: "drop", BaseURL: "   "},
		{APIKey: "drop-empty", BaseURL: ""},
	}}

	cfg.SanitizeNeuralwattKeys()

	if len(cfg.NeuralwattKey) != 1 {
		t.Fatalf("neuralwatt-api-key count = %d, want 1", len(cfg.NeuralwattKey))
	}
	if cfg.NeuralwattKey[0].APIKey != "keep" {
		t.Fatalf("retained api-key = %q, want keep", cfg.NeuralwattKey[0].APIKey)
	}
	if cfg.NeuralwattKey[0].BaseURL != "https://api.neuralwatt.com/v1" {
		t.Fatalf("base-url = %q, want trimmed https://api.neuralwatt.com/v1", cfg.NeuralwattKey[0].BaseURL)
	}
}

// TestSanitizeNeuralwattKeysOnNilConfig ensures the nil-receiver guard
// matches the rest of the sanitize family.
func TestSanitizeNeuralwattKeysOnNilConfig(t *testing.T) {
	var cfg *Config
	cfg.SanitizeNeuralwattKeys() // must not panic
}
