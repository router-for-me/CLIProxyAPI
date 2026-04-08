package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigOptional_APIKeysSupportsLegacyAndStructuredEntries(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	content := []byte(`
api-keys:
  - "legacy-key"
  - api-key: "structured-key"
    requests-per-second: 9
  - api-key: "default-rps"
`)
	if err := os.WriteFile(configPath, content, 0o600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	cfg, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}

	if len(cfg.APIKeys) != 3 {
		t.Fatalf("len(cfg.APIKeys) = %d, want 3", len(cfg.APIKeys))
	}
	if cfg.APIKeys[0].APIKey != "legacy-key" || cfg.APIKeys[0].RequestsPerSecond != DefaultAPIKeyRequestsPerSecond {
		t.Fatalf("legacy api key = %#v, want default rps", cfg.APIKeys[0])
	}
	if cfg.APIKeys[1].APIKey != "structured-key" || cfg.APIKeys[1].RequestsPerSecond != 9 {
		t.Fatalf("structured api key = %#v, want rps 9", cfg.APIKeys[1])
	}
	if cfg.APIKeys[2].APIKey != "default-rps" || cfg.APIKeys[2].RequestsPerSecond != DefaultAPIKeyRequestsPerSecond {
		t.Fatalf("default-rps api key = %#v, want default rps", cfg.APIKeys[2])
	}
}
