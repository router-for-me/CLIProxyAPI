package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigOptional_FilesDir(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	configYAML := []byte("\nauth-dir: ~/.cli-proxy-api\nfiles-dir: ~/custom-uploads\n")
	if err := os.WriteFile(configPath, configYAML, 0o600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	cfg, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}

	if got := cfg.AuthDir; got != "~/.cli-proxy-api" {
		t.Fatalf("AuthDir = %q, want %q", got, "~/.cli-proxy-api")
	}
	if got := cfg.FilesDir; got != "~/custom-uploads" {
		t.Fatalf("FilesDir = %q, want %q", got, "~/custom-uploads")
	}
}
