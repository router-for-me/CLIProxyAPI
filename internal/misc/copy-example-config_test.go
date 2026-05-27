package misc

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCopyConfigTemplate_Success(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "config.example.yaml")
	dst := filepath.Join(dir, "subdir", "config.yaml")

	content := []byte("host: 0.0.0.0\nport: 8317\n")
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatalf("failed to write source file: %v", err)
	}

	if err := CopyConfigTemplate(src, dst); err != nil {
		t.Fatalf("CopyConfigTemplate() error = %v", err)
	}

	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatalf("failed to read destination file: %v", err)
	}
	if string(got) != string(content) {
		t.Fatalf("destination content = %q, want %q", string(got), string(content))
	}

	info, err := os.Stat(dst)
	if err != nil {
		t.Fatalf("failed to stat destination file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("destination permissions = %o, want 0o600", info.Mode().Perm())
	}
}

func TestCopyConfigTemplate_SourceNotFound(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "nonexistent.yaml")
	dst := filepath.Join(dir, "config.yaml")

	err := CopyConfigTemplate(src, dst)
	if err == nil {
		t.Fatal("CopyConfigTemplate() error = nil, want error for missing source")
	}
}

func TestCopyConfigTemplate_CreatesParentDirs(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "config.example.yaml")
	dst := filepath.Join(dir, "a", "b", "c", "config.yaml")

	content := []byte("port: 9999\n")
	if err := os.WriteFile(src, content, 0o644); err != nil {
		t.Fatalf("failed to write source file: %v", err)
	}

	if err := CopyConfigTemplate(src, dst); err != nil {
		t.Fatalf("CopyConfigTemplate() error = %v", err)
	}

	parentInfo, err := os.Stat(filepath.Join(dir, "a", "b", "c"))
	if err != nil {
		t.Fatalf("parent directory not created: %v", err)
	}
	if !parentInfo.IsDir() {
		t.Fatal("parent path is not a directory")
	}
}

func TestFindExampleConfig_Found(t *testing.T) {
	dir := t.TempDir()
	expected := filepath.Join(dir, "config.example.yaml")
	if err := os.WriteFile(expected, []byte("port: 8317\n"), 0o644); err != nil {
		t.Fatalf("failed to write example config: %v", err)
	}

	got, err := FindExampleConfig(dir)
	if err != nil {
		t.Fatalf("FindExampleConfig() error = %v", err)
	}
	if got != expected {
		t.Fatalf("FindExampleConfig() = %q, want %q", got, expected)
	}
}

func TestFindExampleConfig_NotFound(t *testing.T) {
	dir := t.TempDir()

	_, err := FindExampleConfig(dir)
	if err == nil {
		t.Fatal("FindExampleConfig() error = nil, want error for missing file")
	}
}

func TestFindExampleConfig_IsDirectory(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "config.example.yaml"), 0o755); err != nil {
		t.Fatalf("failed to create directory: %v", err)
	}

	_, err := FindExampleConfig(dir)
	if err == nil {
		t.Fatal("FindExampleConfig() error = nil, want error for directory")
	}
}
