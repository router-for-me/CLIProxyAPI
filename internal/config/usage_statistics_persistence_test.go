package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigOptional_UsageStatisticsPersistenceEnabledDefaultsToTrue(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("debug: false\n"), 0o600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	cfg, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}

	if !cfg.UsageStatisticsPersistenceEnabled {
		t.Fatal("UsageStatisticsPersistenceEnabled = false, want true")
	}
}

func TestLoadConfigOptional_UsageStatisticsPersistenceEnabledHonorsFalse(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("usage-statistics-persistence-enabled: false\n"), 0o600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	cfg, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}

	if cfg.UsageStatisticsPersistenceEnabled {
		t.Fatal("UsageStatisticsPersistenceEnabled = true, want false")
	}
}
