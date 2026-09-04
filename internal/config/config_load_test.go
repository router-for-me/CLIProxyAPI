package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfigOptional_MissingFile(t *testing.T) {
	nonExistentPath := filepath.Join(t.TempDir(), "non-existent-config.yaml")

	// When optional is true, missing file returns empty config with defaults.
	cfg, err := LoadConfigOptional(nonExistentPath, true)
	if err != nil {
		t.Fatalf("LoadConfigOptional with optional=true on missing file failed: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}

	// When optional is false, missing file returns error.
	_, errRequired := LoadConfigOptional(nonExistentPath, false)
	if errRequired == nil {
		t.Fatal("expected error for LoadConfigOptional with optional=false on missing file, got nil")
	}
}

func TestLoadConfigOptional_MalformedYAMLIsFatal(t *testing.T) {
	tempDir := t.TempDir()
	badConfigPath := filepath.Join(tempDir, "bad-config.yaml")
	if err := os.WriteFile(badConfigPath, []byte("port: [unclosed list\n  auth-dir: /custom\n"), 0644); err != nil {
		t.Fatalf("failed to write bad config file: %v", err)
	}

	// Malformed YAML must be fatal even when optional is true.
	_, err := LoadConfigOptional(badConfigPath, true)
	if err == nil {
		t.Fatal("expected fatal error for malformed YAML with optional=true, got nil")
	}
}

func TestLoadConfigOptional_InvalidWeightIsFatal(t *testing.T) {
	tempDir := t.TempDir()
	invalidWeightPath := filepath.Join(tempDir, "invalid-weight-config.yaml")
	content := `
meta-api-key:
  - api-key: "test-key"
    weight: 2000000
`
	if err := os.WriteFile(invalidWeightPath, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write invalid weight config file: %v", err)
	}

	// Invalid weight must be fatal even when optional is true.
	_, err := LoadConfigOptional(invalidWeightPath, true)
	if err == nil {
		t.Fatal("expected fatal error for invalid weight with optional=true, got nil")
	}
}
