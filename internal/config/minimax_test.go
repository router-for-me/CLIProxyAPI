package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSanitizeMiniMaxKeys(t *testing.T) {
	cfg := &Config{
		MiniMaxKey: []MiniMaxKey{
			{APIKey: "  key1  ", BaseURL: "  https://api.minimax.io/v1  "},
			{APIKey: ""}, // should be removed
			{APIKey: "key2", Prefix: "  myprefix  "},
		},
	}
	cfg.SanitizeMiniMaxKeys()
	if len(cfg.MiniMaxKey) != 2 {
		t.Fatalf("expected 2 keys after sanitize, got %d", len(cfg.MiniMaxKey))
	}
	if cfg.MiniMaxKey[0].APIKey != "key1" {
		t.Errorf("expected trimmed APIKey, got %q", cfg.MiniMaxKey[0].APIKey)
	}
	if cfg.MiniMaxKey[0].BaseURL != "https://api.minimax.io/v1" {
		t.Errorf("expected trimmed BaseURL, got %q", cfg.MiniMaxKey[0].BaseURL)
	}
	if cfg.MiniMaxKey[1].Prefix != "myprefix" {
		t.Errorf("expected trimmed prefix, got %q", cfg.MiniMaxKey[1].Prefix)
	}
}

func TestLoadConfigOptional_MiniMaxAPIKey(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	configYAML := []byte(`
minimax-api-key:
  - api-key: "test-minimax-key"
    base-url: "https://api.minimax.io/v1"
    models:
      - name: "MiniMax-M2.7"
        alias: "minimax-latest"
`)
	if err := os.WriteFile(configPath, configYAML, 0o600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	cfg, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}
	if len(cfg.MiniMaxKey) != 1 {
		t.Fatalf("expected 1 MiniMax key, got %d", len(cfg.MiniMaxKey))
	}
	k := cfg.MiniMaxKey[0]
	if k.APIKey != "test-minimax-key" {
		t.Errorf("expected api-key=test-minimax-key, got %q", k.APIKey)
	}
	if k.BaseURL != "https://api.minimax.io/v1" {
		t.Errorf("expected base-url=https://api.minimax.io/v1, got %q", k.BaseURL)
	}
	if len(k.Models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(k.Models))
	}
	if k.Models[0].Name != "MiniMax-M2.7" {
		t.Errorf("expected model name=MiniMax-M2.7, got %q", k.Models[0].Name)
	}
	if k.Models[0].Alias != "minimax-latest" {
		t.Errorf("expected model alias=minimax-latest, got %q", k.Models[0].Alias)
	}
}
