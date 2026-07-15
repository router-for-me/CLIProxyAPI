//go:build !windows

package claude

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveTokenToFileCreatesPrivateFile(t *testing.T) {
	authFilePath := filepath.Join(t.TempDir(), "credentials.json")
	storage := &ClaudeTokenStorage{AccessToken: "access", RefreshToken: "refresh"}

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

	storage := &ClaudeTokenStorage{AccessToken: "access", RefreshToken: "refresh"}
	if err := storage.SaveTokenToFile(authFilePath); err != nil {
		t.Fatalf("SaveTokenToFile() error = %v", err)
	}

	assertPrivateFileMode(t, authFilePath)
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
