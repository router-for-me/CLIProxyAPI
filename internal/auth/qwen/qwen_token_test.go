package qwen

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/tidwall/gjson"
)

func TestQwenTokenStorageSaveTokenToFilePersistsCookieFields(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "qwen-test.json")
	storage := &QwenTokenStorage{
		AccessToken:  "access",
		RefreshToken: "refresh",
		Email:        "user@example.com",
		ResourceURL:  "portal.qwen.ai",
		TokenCookie:  "token-cookie",
		SessionCookies: map[string]string{
			"refresh_token": "session-refresh",
		},
	}

	if err := storage.SaveTokenToFile(path); err != nil {
		t.Fatalf("SaveTokenToFile() error = %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	if got := gjson.GetBytes(data, "token_cookie").String(); got != "token-cookie" {
		t.Fatalf("token_cookie = %q, want %q", got, "token-cookie")
	}
	if got := gjson.GetBytes(data, "session_cookies.refresh_token").String(); got != "session-refresh" {
		t.Fatalf("session_cookies.refresh_token = %q, want %q", got, "session-refresh")
	}
}

func TestQwenTokenStorageSaveTokenToFileSetsFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file mode enforcement is not reliable on Windows")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "qwen-perms.json")
	storage := &QwenTokenStorage{
		AccessToken:  "access",
		RefreshToken: "refresh",
	}

	if err := storage.SaveTokenToFile(path); err != nil {
		t.Fatalf("SaveTokenToFile() error = %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}

	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("file permissions = %o, want %o", got, 0o600)
	}
}

func TestQwenTokenStorageSaveTokenToFileRestrictsExistingFilePermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("file mode enforcement is not reliable on Windows")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "qwen-existing.json")
	if err := os.WriteFile(path, []byte("{}"), 0o666); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}

	storage := &QwenTokenStorage{
		AccessToken:  "access",
		RefreshToken: "refresh",
	}

	if err := storage.SaveTokenToFile(path); err != nil {
		t.Fatalf("SaveTokenToFile() error = %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}

	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("file permissions = %o, want %o", got, 0o600)
	}
}

func TestQwenTokenStorageSaveTokenToFileRestrictsDirectoryPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory mode enforcement is not reliable on Windows")
	}

	baseDir := t.TempDir()
	parent := filepath.Join(baseDir, "loose")
	if err := os.MkdirAll(parent, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.Chmod(parent, 0o755); err != nil {
		t.Fatalf("Chmod() error = %v", err)
	}

	path := filepath.Join(parent, "qwen-dir.json")
	storage := &QwenTokenStorage{}
	if err := storage.SaveTokenToFile(path); err != nil {
		t.Fatalf("SaveTokenToFile() error = %v", err)
	}

	info, err := os.Stat(parent)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}

	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("directory permissions = %o, want %o", got, 0o700)
	}
}
