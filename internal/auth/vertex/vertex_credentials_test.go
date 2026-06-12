package vertex

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSaveTokenToFileUsesPrivatePermissions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vertex.json")
	if err := os.WriteFile(path, []byte("{}"), 0o644); err != nil {
		t.Fatalf("precreate credential file: %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("chmod credential file: %v", err)
	}
	storage := &VertexCredentialStorage{
		ServiceAccount: map[string]any{"project_id": "test"},
	}

	if err := storage.SaveTokenToFile(path); err != nil {
		t.Fatalf("SaveTokenToFile: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat token file: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %v, want 0600", got)
	}
}
