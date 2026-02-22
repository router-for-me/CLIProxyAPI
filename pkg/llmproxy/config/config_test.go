package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	tmpFile, err := os.CreateTemp("", "config*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(tmpFile.Name()) }()

	content := `
port: 8080
auth-dir: ./auth
debug: true
`
	if _, err := tmpFile.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := tmpFile.Close(); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(tmpFile.Name())
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	if cfg.Port != 8080 {
		t.Errorf("expected port 8080, got %d", cfg.Port)
	}

	if cfg.AuthDir != "./auth" {
		t.Errorf("expected auth-dir ./auth, got %s", cfg.AuthDir)
	}

	if !cfg.Debug {
		t.Errorf("expected debug true, got false")
	}
}

func TestConfig_Validate(t *testing.T) {
	cfg := &Config{
		Port: 8080,
	}
	if cfg.Port != 8080 {
		t.Errorf("expected port 8080, got %d", cfg.Port)
	}
}

func TestLoadConfigOptional_DirectoryPath(t *testing.T) {
	tmpDir := t.TempDir()
	dirPath := filepath.Join(tmpDir, "config-dir")
	if err := os.MkdirAll(dirPath, 0o755); err != nil {
		t.Fatalf("failed to create temp config dir: %v", err)
	}

	_, err := LoadConfigOptional(dirPath, false)
	if err == nil {
		t.Fatal("expected error for directory config path when optional=false")
	}
	if !strings.Contains(err.Error(), "is a directory") {
		t.Fatalf("expected directory error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "pass a YAML file path") {
		t.Fatalf("expected remediation hint in error, got: %v", err)
	}

	cfg, err := LoadConfigOptional(dirPath, true)
	if err != nil {
		t.Fatalf("expected nil error for optional directory config path, got: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config for optional directory config path")
	}
}
