package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigOptional_RemoteManagementExternalBaseURL(t *testing.T) {
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	configYAML := []byte(`
remote-management:
  external-base-url: "  https://example.com/base/  "
`)
	if err := os.WriteFile(configPath, configYAML, 0o600); err != nil {
		t.Fatalf("failed to write config: %v", err)
	}

	cfg, err := LoadConfigOptional(configPath, false)
	if err != nil {
		t.Fatalf("LoadConfigOptional() error = %v", err)
	}

	if got := cfg.RemoteManagement.ExternalBaseURL; got != "https://example.com/base/" {
		t.Fatalf("ExternalBaseURL = %q, want %q", got, "https://example.com/base/")
	}
}
