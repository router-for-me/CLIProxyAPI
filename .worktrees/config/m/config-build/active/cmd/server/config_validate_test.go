package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateConfigFileStrict_Success(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("port: 8317\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	if err := validateConfigFileStrict(configPath); err != nil {
		t.Fatalf("validateConfigFileStrict() unexpected error: %v", err)
	}
}

func TestValidateConfigFileStrict_UnknownField(t *testing.T) {
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("port: 8317\nws-authentication: true\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	err := validateConfigFileStrict(configPath)
	if err == nil {
		t.Fatal("expected error for unknown field, got nil")
	}
	if !strings.Contains(err.Error(), "strict schema validation failed") {
		t.Fatalf("unexpected error: %v", err)
	}
}
