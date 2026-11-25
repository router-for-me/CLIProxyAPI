package config

import "testing"

func TestV8ProviderRetryCapAndGeminiGroupName(t *testing.T) {
	raw := []byte(`config-version: 8
routing:
  retry:
    max-retry-credentials-per-provider: 2
api-keys:
  gemini:
    - name: "pool-a"
      keys:
        - api-key: "key-a"
        - api-key: "key-b"
      models:
        - name: "gemini-4-hydrogen"
`)
	cfg, err := ParseConfigBytes(raw)
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if cfg.MaxRetryCredentialsPerProvider != 2 {
		t.Fatalf("MaxRetryCredentialsPerProvider = %d, want 2", cfg.MaxRetryCredentialsPerProvider)
	}
	if len(cfg.GeminiKey) != 2 {
		t.Fatalf("GeminiKey count = %d, want 2", len(cfg.GeminiKey))
	}
	for i, key := range cfg.GeminiKey {
		if key.Name != "pool-a" {
			t.Fatalf("GeminiKey[%d].Name = %q, want pool-a", i, key.Name)
		}
	}

	migrated, _, err := NormalizeConfigLayout(raw, true)
	if err != nil {
		t.Fatalf("NormalizeConfigLayout() error = %v", err)
	}
	roundTripped, err := ParseConfigBytes(migrated)
	if err != nil {
		t.Fatalf("ParseConfigBytes(round-trip) error = %v", err)
	}
	if roundTripped.MaxRetryCredentialsPerProvider != 2 {
		t.Fatalf("round-trip cap = %d, want 2", roundTripped.MaxRetryCredentialsPerProvider)
	}
	for i, key := range roundTripped.GeminiKey {
		if key.Name != "pool-a" {
			t.Fatalf("round-trip GeminiKey[%d].Name = %q, want pool-a", i, key.Name)
		}
	}
}

func TestV8ProviderRetryCapNegativeIsDisabled(t *testing.T) {
	raw := []byte(`config-version: 8
routing:
  retry:
    max-retry-credentials-per-provider: -3
`)
	cfg, err := ParseConfigBytes(raw)
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if cfg.MaxRetryCredentialsPerProvider != 0 {
		t.Fatalf("MaxRetryCredentialsPerProvider = %d, want 0", cfg.MaxRetryCredentialsPerProvider)
	}
}

func TestV8GroupNameWinsOverPerKeyName(t *testing.T) {
	raw := []byte(`config-version: 8
api-keys:
  gemini:
    - name: "pool-a"
      keys:
        - api-key: "key-a"
          name: "sibling-a"
        - api-key: "key-b"
          name: "sibling-b"
      models:
        - name: "gemini-4-hydrogen"
`)
	cfg, err := ParseConfigBytes(raw)
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if len(cfg.GeminiKey) != 2 {
		t.Fatalf("GeminiKey count = %d, want 2", len(cfg.GeminiKey))
	}
	for i, key := range cfg.GeminiKey {
		if key.Name != "pool-a" {
			t.Fatalf("GeminiKey[%d].Name = %q, want the group name pool-a", i, key.Name)
		}
	}
}

func TestV8GeneratedGroupLabelAvoidsExplicitNames(t *testing.T) {
	raw := []byte(`config-version: 8
api-keys:
  gemini:
    - name: "gemini-unnamed-2"
      keys:
        - api-key: "key-explicit"
      models:
        - name: "gemini-4-hydrogen"
    - keys:
        - api-key: "key-unnamed"
      models:
        - name: "gemini-4-hydrogen"
`)
	cfg, err := ParseConfigBytes(raw)
	if err != nil {
		t.Fatalf("ParseConfigBytes() error = %v", err)
	}
	if len(cfg.GeminiKey) != 2 {
		t.Fatalf("GeminiKey count = %d, want 2", len(cfg.GeminiKey))
	}
	first, second := cfg.GeminiKey[0].Name, cfg.GeminiKey[1].Name
	if first != "gemini-unnamed-2" {
		t.Fatalf("explicit group name = %q, want it preserved", first)
	}
	if second == first {
		t.Fatalf("unnamed group reused the explicit label %q", second)
	}
	if second == "" {
		t.Fatalf("unnamed group has no label")
	}
}
