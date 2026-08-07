//go:build unix

package management

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestWriteAuthFileSecuresExistingFile(t *testing.T) {
	authDir := t.TempDir()
	path := filepath.Join(authDir, "auth.json")
	if errWrite := os.WriteFile(path, []byte(`{"type":"demo","email":"old@example.com"}`), 0o664); errWrite != nil {
		t.Fatalf("WriteFile() error = %v", errWrite)
	}
	if errChmod := os.Chmod(path, 0o664); errChmod != nil {
		t.Fatalf("Chmod() error = %v", errChmod)
	}

	handler := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, coreauth.NewManager(nil, nil, nil))
	replacement := []byte(`{"type":"demo","email":"new@example.com"}`)
	if errWrite := handler.writeAuthFile(context.Background(), "auth.json", replacement); errWrite != nil {
		t.Fatalf("writeAuthFile() error = %v", errWrite)
	}

	assertAuthFileMode(t, path)
	persisted, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("ReadFile() error = %v", errRead)
	}
	if got, want := string(persisted), string(replacement); got != want {
		t.Errorf("saved auth file = %q, want %q", got, want)
	}
}

func TestSetSourceAuthFileDisabledSecuresExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "source.json")
	initial := []byte(`{"type":"gemini-cli","email":"user@example.com","disabled":false}`)
	if errWrite := os.WriteFile(path, initial, 0o664); errWrite != nil {
		t.Fatalf("WriteFile() error = %v", errWrite)
	}
	if errChmod := os.Chmod(path, 0o664); errChmod != nil {
		t.Fatalf("Chmod() error = %v", errChmod)
	}

	if errWrite := setSourceAuthFileDisabled(path, true); errWrite != nil {
		t.Fatalf("setSourceAuthFileDisabled() error = %v", errWrite)
	}

	assertAuthFileMode(t, path)
	persisted, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("ReadFile() error = %v", errRead)
	}
	var metadata map[string]any
	if errUnmarshal := json.Unmarshal(persisted, &metadata); errUnmarshal != nil {
		t.Fatalf("Unmarshal() error = %v", errUnmarshal)
	}
	if metadata["type"] != "gemini-cli" || metadata["email"] != "user@example.com" || metadata["disabled"] != true {
		t.Errorf("saved metadata = %#v, want original fields with disabled=true", metadata)
	}
}

func assertAuthFileMode(t *testing.T, path string) {
	t.Helper()
	info, errStat := os.Stat(path)
	if errStat != nil {
		t.Fatalf("Stat() error = %v", errStat)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("file mode = %o, want 0600", got)
	}
}
