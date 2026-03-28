package files

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNewStore_DefaultsToAuthDirFiles(t *testing.T) {
	authDir := filepath.Join(t.TempDir(), "auth")
	store, err := NewStoreWithDir(authDir, "")
	if err != nil {
		t.Fatalf("NewStoreWithDir: %v", err)
	}

	meta, err := store.Create("note.txt", "assistants", "text/plain", []byte("hello"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	contentPath := filepath.Join(authDir, "files", meta.ID+filepath.Ext(meta.Filename))
	if _, err := os.Stat(contentPath); err != nil {
		t.Fatalf("expected content in default files dir %s: %v", contentPath, err)
	}
	metaPath := filepath.Join(authDir, "files", meta.ID+metadataExt)
	if _, err := os.Stat(metaPath); err != nil {
		t.Fatalf("expected metadata in default files dir %s: %v", metaPath, err)
	}
}

func TestNewStore_UsesConfiguredFilesDir(t *testing.T) {
	tmpDir := t.TempDir()
	authDir := filepath.Join(tmpDir, "auth")
	filesDir := filepath.Join(tmpDir, "uploads")
	store, err := NewStoreWithDir(authDir, filesDir)
	if err != nil {
		t.Fatalf("NewStoreWithDir: %v", err)
	}

	meta, err := store.Create("note.txt", "assistants", "text/plain", []byte("hello"))
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	contentPath := filepath.Join(filesDir, meta.ID+filepath.Ext(meta.Filename))
	if _, err := os.Stat(contentPath); err != nil {
		t.Fatalf("expected content in configured files dir %s: %v", contentPath, err)
	}
	metaPath := filepath.Join(filesDir, meta.ID+metadataExt)
	if _, err := os.Stat(metaPath); err != nil {
		t.Fatalf("expected metadata in configured files dir %s: %v", metaPath, err)
	}
	defaultPath := filepath.Join(authDir, "files", meta.ID+filepath.Ext(meta.Filename))
	if _, err := os.Stat(defaultPath); !os.IsNotExist(err) {
		t.Fatalf("expected no content in default files dir %s, err=%v", defaultPath, err)
	}
}
