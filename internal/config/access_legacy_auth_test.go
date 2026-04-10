package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadConfigOptional_MigratesLegacyAuthProviders(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	configYAML := []byte(`
auth:
  providers:
    - type: config-api-key
      api-keys:
        - legacy-key-1
        - legacy-key-2
`)
	if err := os.WriteFile(configPath, configYAML, 0o600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	cfg, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}

	want := []string{"legacy-key-1", "legacy-key-2"}
	if !reflect.DeepEqual(cfg.APIKeys, want) {
		t.Fatalf("APIKeys = %v, want %v", cfg.APIKeys, want)
	}
}

func TestLoadConfigOptional_MergesTopLevelAndLegacyAuthProviders(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	configYAML := []byte(`
api-keys:
  - current-key-1
auth:
  providers:
    - type: config-api-key
      api-keys:
        - current-key-1
        - legacy-key-2
`)
	if err := os.WriteFile(configPath, configYAML, 0o600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	cfg, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}

	want := []string{"current-key-1", "legacy-key-2"}
	if !reflect.DeepEqual(cfg.APIKeys, want) {
		t.Fatalf("APIKeys = %v, want %v", cfg.APIKeys, want)
	}
}
