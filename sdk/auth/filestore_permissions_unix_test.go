//go:build unix

package auth

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
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
	stableModTime := time.Unix(1_700_000_000, 0)
	if errChtimes := os.Chtimes(path, stableModTime, stableModTime); errChtimes != nil {
		t.Fatalf("Chtimes() error = %v", errChtimes)
	}
	beforeInfo, errStat := os.Stat(path)
	if errStat != nil {
		t.Fatalf("Stat() before Save error = %v", errStat)
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
	if got, want := info.ModTime(), beforeInfo.ModTime(); !got.Equal(want) {
		t.Errorf("file mtime = %v, want unchanged %v", got, want)
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

func TestFileTokenStoreReadAntigravityProjectIDSecuresExistingFile(t *testing.T) {
	baseDir := t.TempDir()
	path := filepath.Join(baseDir, "antigravity.json")
	existing := []byte(`{"type":"antigravity","access_token":"test-token","email":"user@example.com"}`)
	if errWrite := os.WriteFile(path, existing, 0o664); errWrite != nil {
		t.Fatalf("WriteFile() error = %v", errWrite)
	}
	if errChmod := os.Chmod(path, 0o664); errChmod != nil {
		t.Fatalf("Chmod() error = %v", errChmod)
	}

	previousTransport := http.DefaultClient.Transport
	http.DefaultClient.Transport = testRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if got, want := req.URL.String(), "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist"; got != want {
			t.Fatalf("request URL = %q, want %q", got, want)
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"cloudaicompanionProject":"project-from-api"}`)),
			Request:    req,
		}, nil
	})
	t.Cleanup(func() {
		http.DefaultClient.Transport = previousTransport
	})

	store := NewFileTokenStore()
	store.SetBaseDir(baseDir)
	auth, errReadAuth := store.readAuthFile(path, baseDir)
	if errReadAuth != nil {
		t.Fatalf("readAuthFile() error = %v", errReadAuth)
	}
	if auth == nil {
		t.Fatal("readAuthFile() returned nil auth")
	}
	if got, want := auth.Metadata["project_id"], "project-from-api"; got != want {
		t.Errorf("project_id = %#v, want %q", got, want)
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
	expected := []byte(`{"type":"antigravity","access_token":"test-token","email":"user@example.com","project_id":"project-from-api"}`)
	if !jsonEqual(persisted, expected) {
		t.Errorf("saved auth file = %s, want JSON equal to %s", persisted, expected)
	}
}
