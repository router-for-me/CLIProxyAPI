package vertex

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/MoeForge/moego/json"
)

func TestSaveTokenToFileWritesJSONAndPermissions(t *testing.T) {
	t.Parallel()

	tmpDir := t.TempDir()
	authFile := filepath.Join(tmpDir, "nested", "vertex-sa.json")

	storage := &VertexCredentialStorage{
		ServiceAccount: map[string]any{
			"type":         "service_account",
			"project_id":   "demo-project",
			"client_email": "demo@demo-project.iam.gserviceaccount.com",
			"private_key":  "-----BEGIN PRIVATE KEY-----\nTEST\n-----END PRIVATE KEY-----\n",
		},
		ProjectID: "demo-project",
		Email:     "demo@demo-project.iam.gserviceaccount.com",
		Location:  "us-central1",
	}

	if err := storage.SaveTokenToFile(authFile); err != nil {
		t.Fatalf("SaveTokenToFile() returned unexpected error: %v", err)
	}

	raw, err := os.ReadFile(authFile)
	if err != nil {
		t.Fatalf("ReadFile() failed: %v", err)
	}

	var decoded VertexCredentialStorage
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() failed: %v", err)
	}
	if decoded.Type != "vertex" {
		t.Fatalf("decoded.Type = %q, want %q", decoded.Type, "vertex")
	}
	if decoded.ProjectID != storage.ProjectID {
		t.Fatalf("decoded.ProjectID = %q, want %q", decoded.ProjectID, storage.ProjectID)
	}
	if decoded.Email != storage.Email {
		t.Fatalf("decoded.Email = %q, want %q", decoded.Email, storage.Email)
	}
	if decoded.ServiceAccount["client_email"] != storage.ServiceAccount["client_email"] {
		t.Fatalf("decoded.ServiceAccount[client_email] = %v, want %v", decoded.ServiceAccount["client_email"], storage.ServiceAccount["client_email"])
	}

	if runtime.GOOS != "windows" {
		info, errStat := os.Stat(authFile)
		if errStat != nil {
			t.Fatalf("Stat() failed: %v", errStat)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("file permissions = %04o, want 0600", got)
		}

		// Verify overwriting an existing file with looser permissions tightens it to 0600.
		if errChmod := os.Chmod(authFile, 0o644); errChmod != nil {
			t.Fatalf("Chmod(0644) failed: %v", errChmod)
		}
		if errSave := storage.SaveTokenToFile(authFile); errSave != nil {
			t.Fatalf("second SaveTokenToFile() failed: %v", errSave)
		}
		infoAfter, errStatAfter := os.Stat(authFile)
		if errStatAfter != nil {
			t.Fatalf("Stat() after overwrite failed: %v", errStatAfter)
		}
		if got := infoAfter.Mode().Perm(); got != 0o600 {
			t.Fatalf("file permissions after overwrite = %04o, want 0600", got)
		}
	}
}
