package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigOptional_RequestBudgetSecondsDefault(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("port: 8317\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}
	if cfg.RequestBudgetSeconds != 45 {
		t.Fatalf("request-budget-seconds = %d, want 45", cfg.RequestBudgetSeconds)
	}
}

func TestLoadConfigOptional_RequestBudgetSecondsNegativeSanitized(t *testing.T) {
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "config.yaml")
	content := []byte("request-budget-seconds: -1\n")
	if err := os.WriteFile(configPath, content, 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}
	if cfg.RequestBudgetSeconds != 0 {
		t.Fatalf("request-budget-seconds = %d, want 0", cfg.RequestBudgetSeconds)
	}
}
