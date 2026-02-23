package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestMigrateOAuthModelAlias_SkipsIfNewFieldExists(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configFile := filepath.Join(dir, "config.yaml")

	content := `oauth-model-alias:
  gemini-cli:
    - name: "gemini-2.5-pro"
      alias: "g2.5p"
`
	if err := os.WriteFile(configFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	migrated, err := MigrateOAuthModelAlias(configFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if migrated {
		t.Fatal("expected no migration when oauth-model-alias already exists")
	}

	// Verify file unchanged
	data, _ := os.ReadFile(configFile)
	if !strings.Contains(string(data), "oauth-model-alias:") {
		t.Fatal("file should still contain oauth-model-alias")
	}
}

func TestMigrateOAuthModelAlias_MigratesOldField(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configFile := filepath.Join(dir, "config.yaml")

	content := `oauth-model-mappings:
  gemini-cli:
    - name: "gemini-2.5-pro"
      alias: "g2.5p"
      fork: true
`
	if err := os.WriteFile(configFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	migrated, err := MigrateOAuthModelAlias(configFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !migrated {
		t.Fatal("expected migration to occur")
	}

	// Verify new field exists and old field removed
	data, _ := os.ReadFile(configFile)
	if strings.Contains(string(data), "oauth-model-mappings:") {
		t.Fatal("old field should be removed")
	}
	if !strings.Contains(string(data), "oauth-model-alias:") {
		t.Fatal("new field should exist")
	}

	// Parse and verify structure
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
}

func TestMigrateOAuthModelAlias_ConvertsAntigravityModels(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configFile := filepath.Join(dir, "config.yaml")

	// Use old model names that should be converted
	content := `oauth-model-mappings:
  antigravity:
    - name: "gemini-2.5-computer-use-preview-10-2025"
      alias: "computer-use"
    - name: "gemini-3-pro-preview"
      alias: "g3p"
`
	if err := os.WriteFile(configFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	migrated, err := MigrateOAuthModelAlias(configFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !migrated {
		t.Fatal("expected migration to occur")
	}

	// Verify model names were converted
	data, _ := os.ReadFile(configFile)
	content = string(data)
	if !strings.Contains(content, "rev19-uic3-1p") {
		t.Fatal("expected gemini-2.5-computer-use-preview-10-2025 to be converted to rev19-uic3-1p")
	}
	if !strings.Contains(content, "gemini-3-pro-high") {
		t.Fatal("expected gemini-3-pro-preview to be converted to gemini-3-pro-high")
	}

	// Verify missing default aliases were supplemented
	if !strings.Contains(content, "gemini-3-pro-image") {
		t.Fatal("expected missing default alias gemini-3-pro-image to be added")
	}
	if !strings.Contains(content, "gemini-3-flash") {
		t.Fatal("expected missing default alias gemini-3-flash to be added")
	}
	if !strings.Contains(content, "claude-sonnet-4-5") {
		t.Fatal("expected missing default alias claude-sonnet-4-5 to be added")
	}
	if !strings.Contains(content, "claude-sonnet-4-5-thinking") {
		t.Fatal("expected missing default alias claude-sonnet-4-5-thinking to be added")
	}
	if !strings.Contains(content, "claude-opus-4-5-thinking") {
		t.Fatal("expected missing default alias claude-opus-4-5-thinking to be added")
	}
	if !strings.Contains(content, "claude-opus-4-6-thinking") {
		t.Fatal("expected missing default alias claude-opus-4-6-thinking to be added")
	}
}

func TestMigrateOAuthModelAlias_AddsDefaultIfNeitherExists(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configFile := filepath.Join(dir, "config.yaml")

	content := `debug: true
port: 8080
`
	if err := os.WriteFile(configFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	migrated, err := MigrateOAuthModelAlias(configFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !migrated {
		t.Fatal("expected migration to add default config")
	}

	// Verify default antigravity config was added
	data, _ := os.ReadFile(configFile)
	content = string(data)
	if !strings.Contains(content, "oauth-model-alias:") {
		t.Fatal("expected oauth-model-alias to be added")
	}
	if !strings.Contains(content, "antigravity:") {
		t.Fatal("expected antigravity channel to be added")
	}
	if !strings.Contains(content, "rev19-uic3-1p") {
		t.Fatal("expected default antigravity aliases to include rev19-uic3-1p")
	}
}

func TestMigrateOAuthModelAlias_PreservesOtherConfig(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configFile := filepath.Join(dir, "config.yaml")

	content := `debug: true
port: 8080
oauth-model-mappings:
  gemini-cli:
    - name: "test"
      alias: "t"
api-keys:
  - "key1"
  - "key2"
`
	if err := os.WriteFile(configFile, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	migrated, err := MigrateOAuthModelAlias(configFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !migrated {
		t.Fatal("expected migration to occur")
	}

	// Verify other config preserved
	data, _ := os.ReadFile(configFile)
	content = string(data)
	if !strings.Contains(content, "debug: true") {
		t.Fatal("expected debug field to be preserved")
	}
	if !strings.Contains(content, "port: 8080") {
		t.Fatal("expected port field to be preserved")
	}
	if !strings.Contains(content, "api-keys:") {
		t.Fatal("expected api-keys field to be preserved")
	}
}

func TestMigrateOAuthModelAlias_NonexistentFile(t *testing.T) {
	t.Parallel()

	migrated, err := MigrateOAuthModelAlias("/nonexistent/path/config.yaml")
	if err != nil {
		t.Fatalf("unexpected error for nonexistent file: %v", err)
	}
	if migrated {
		t.Fatal("expected no migration for nonexistent file")
	}
}

func TestMigrateOAuthModelAlias_EmptyFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	configFile := filepath.Join(dir, "config.yaml")

	if err := os.WriteFile(configFile, []byte(""), 0644); err != nil {
		t.Fatal(err)
	}

	migrated, err := MigrateOAuthModelAlias(configFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if migrated {
		t.Fatal("expected no migration for empty file")
	}
}
