//go:build !windows

package codex

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestSaveTokenToFileCreatesPrivateFile(t *testing.T) {
	authFilePath := filepath.Join(t.TempDir(), "credentials.json")
	storage := &CodexTokenStorage{AccessToken: "access", RefreshToken: "refresh"}

	if err := storage.SaveTokenToFile(authFilePath); err != nil {
		t.Fatalf("SaveTokenToFile() error = %v", err)
	}

	assertPrivateFileMode(t, authFilePath)
}

func TestSaveTokenToFileTightensExistingFileMode(t *testing.T) {
	authFilePath := filepath.Join(t.TempDir(), "credentials.json")
	if err := os.WriteFile(authFilePath, []byte("{}"), 0o644); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}
	if err := os.Chmod(authFilePath, 0o644); err != nil {
		t.Fatalf("os.Chmod() error = %v", err)
	}

	storage := &CodexTokenStorage{AccessToken: "access", RefreshToken: "refresh"}
	if err := storage.SaveTokenToFile(authFilePath); err != nil {
		t.Fatalf("SaveTokenToFile() error = %v", err)
	}

	assertPrivateFileMode(t, authFilePath)
}

func TestSaveTokenToFilePreservesExistingFileWhenEncodingFails(t *testing.T) {
	dir := t.TempDir()
	authFilePath := filepath.Join(dir, "credentials.json")
	original := []byte(`{"refresh_token":"existing"}`)
	if err := os.WriteFile(authFilePath, original, 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}

	storage := &CodexTokenStorage{
		AccessToken:  "access",
		RefreshToken: "refresh",
		Metadata:     map[string]any{"unsupported": make(chan int)},
	}
	if err := storage.SaveTokenToFile(authFilePath); err == nil {
		t.Fatal("SaveTokenToFile() error = nil, want encoding error")
	}

	got, err := os.ReadFile(authFilePath)
	if err != nil {
		t.Fatalf("os.ReadFile() error = %v", err)
	}
	if !bytes.Equal(got, original) {
		t.Fatal("credential file contents changed after failed save")
	}
}

func assertPrivateFileMode(t *testing.T, path string) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("os.Stat() error = %v", err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0o600); got != want {
		t.Fatalf("credential file mode = %04o, want %04o", got, want)
	}
}
