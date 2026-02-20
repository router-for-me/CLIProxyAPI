package config

import (
	"os"
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
