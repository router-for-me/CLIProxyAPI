//go:build unix

package auth

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"golang.org/x/sys/unix"
)

func TestFileTokenStoreSaveMetadataSecuresExistingFile(t *testing.T) {
	baseDir := t.TempDir()
	path := filepath.Join(baseDir, "auth.json")
	if errWrite := os.WriteFile(path, []byte(`{"type":"demo","email":"old@example.com","disabled":false}`), 0o664); errWrite != nil {
		t.Fatalf("WriteFile() error = %v", errWrite)
	}
	if errChmod := os.Chmod(path, 0o664); errChmod != nil {
		t.Fatalf("Chmod() error = %v", errChmod)
	}

	store := NewFileTokenStore()
	store.SetBaseDir(baseDir)
	auth := &cliproxyauth.Auth{
		ID:       "auth.json",
		FileName: "auth.json",
		Metadata: map[string]any{
			"type":  "demo",
			"email": "new@example.com",
		},
	}

	if _, errSave := store.Save(context.Background(), auth); errSave != nil {
		t.Fatalf("Save() error = %v", errSave)
	}

	info, errStat := os.Stat(path)
	if errStat != nil {
		t.Fatalf("Stat() error = %v", errStat)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("file mode = %o, want 0600", got)
	}
	persisted, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("ReadFile() error = %v", errRead)
	}
	expected := []byte(`{"type":"demo","email":"new@example.com","disabled":false}`)
	if !jsonEqual(persisted, expected) {
		t.Errorf("saved auth file = %s, want JSON equal to %s", persisted, expected)
	}
}

func TestFileTokenStoreSaveMetadataSecuresEquivalentExistingFile(t *testing.T) {
	baseDir := t.TempDir()
	path := filepath.Join(baseDir, "auth.json")
	existing := []byte(`{"type":"demo","email":"same@example.com","disabled":false}`)
	if errWrite := os.WriteFile(path, existing, 0o664); errWrite != nil {
		t.Fatalf("WriteFile() error = %v", errWrite)
	}
	if errChmod := os.Chmod(path, 0o664); errChmod != nil {
		t.Fatalf("Chmod() error = %v", errChmod)
	}

	store := NewFileTokenStore()
	store.SetBaseDir(baseDir)
	auth := &cliproxyauth.Auth{
		ID:       "auth.json",
		FileName: "auth.json",
		Metadata: map[string]any{
			"type":  "demo",
			"email": "same@example.com",
		},
	}

	if _, errSave := store.Save(context.Background(), auth); errSave != nil {
		t.Fatalf("Save() error = %v", errSave)
	}

	info, errStat := os.Stat(path)
	if errStat != nil {
		t.Fatalf("Stat() error = %v", errStat)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("file mode = %o, want 0600", got)
	}
	persisted, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("ReadFile() error = %v", errRead)
	}
	if got, want := string(persisted), string(existing); got != want {
		t.Errorf("saved auth file = %q, want unchanged %q", got, want)
	}
}

func TestFileTokenStoreSaveMetadataCreatesFileWithSecureMode(t *testing.T) {
	previousUmask := unix.Umask(0o002)
	defer unix.Umask(previousUmask)

	baseDir := t.TempDir()
	path := filepath.Join(baseDir, "auth.json")
	store := NewFileTokenStore()
	store.SetBaseDir(baseDir)
	auth := &cliproxyauth.Auth{
		ID:       "auth.json",
		FileName: "auth.json",
		Metadata: map[string]any{
			"type":  "demo",
			"email": "new@example.com",
		},
	}

	if _, errSave := store.Save(context.Background(), auth); errSave != nil {
		t.Fatalf("Save() error = %v", errSave)
	}

	info, errStat := os.Stat(path)
	if errStat != nil {
		t.Fatalf("Stat() error = %v", errStat)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("file mode = %o, want 0600", got)
	}
	persisted, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("ReadFile() error = %v", errRead)
	}
	expected := []byte(`{"type":"demo","email":"new@example.com","disabled":false}`)
	if !jsonEqual(persisted, expected) {
		t.Errorf("saved auth file = %s, want JSON equal to %s", persisted, expected)
	}
}
