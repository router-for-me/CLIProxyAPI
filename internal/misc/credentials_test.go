//go:build !windows

package misc

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteCredentialFileAtomicPreservesExistingFileWhenCreateTempFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.json")
	original := []byte(`{"refresh_token":"existing"}`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}

	errPermission := errors.New("permission denied")
	ops := credentialFileOps{
		createTemp: func(string, string) (*os.File, error) {
			return nil, errPermission
		},
		rename: os.Rename,
	}
	err := writeCredentialFileAtomic(path, map[string]any{"access_token": "replacement"}, ops)
	if !errors.Is(err, errPermission) {
		t.Fatalf("writeCredentialFileAtomic() error = %v, want permission error", err)
	}

	assertCredentialFileUnchanged(t, path, original)
}

func TestWriteCredentialFileAtomicPreservesExistingFileWhenRenameFails(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "credentials.json")
	original := []byte(`{"refresh_token":"existing"}`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatalf("os.WriteFile() error = %v", err)
	}

	errRename := errors.New("rename failed")
	ops := credentialFileOps{
		createTemp: os.CreateTemp,
		rename: func(tempPath, targetPath string) error {
			if targetPath != path {
				t.Fatalf("rename target = %q, want credential path", targetPath)
			}
			info, err := os.Stat(tempPath)
			if err != nil {
				t.Fatalf("os.Stat(temp) error = %v", err)
			}
			if got, want := info.Mode().Perm(), os.FileMode(0o600); got != want {
				t.Fatalf("temporary credential file mode = %04o, want %04o", got, want)
			}
			return errRename
		},
	}
	err := writeCredentialFileAtomic(path, map[string]any{"access_token": "replacement"}, ops)
	if !errors.Is(err, errRename) {
		t.Fatalf("writeCredentialFileAtomic() error = %v, want rename error", err)
	}

	assertCredentialFileUnchanged(t, path, original)
	matches, err := filepath.Glob(filepath.Join(dir, ".credentials.json.tmp-*"))
	if err != nil {
		t.Fatalf("filepath.Glob() error = %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary credential files remain after failed rename: %d", len(matches))
	}
}

func assertCredentialFileUnchanged(t *testing.T, path string, want []byte) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile() error = %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("credential file contents changed after failed save")
	}
}
