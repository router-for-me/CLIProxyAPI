package codex

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestCodexTokenStorageSaveTokenToFileReplacesExistingCredentialPrivately(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "codex.json")
	if err := os.WriteFile(path, []byte(`{"type":"codex","access_token":"old"}`), 0o600); err != nil {
		t.Fatalf("seed auth file: %v", err)
	}
	if err := os.Chmod(path, 0o444); err != nil {
		t.Fatalf("make auth file read-only: %v", err)
	}

	storage := &CodexTokenStorage{
		IDToken:      "id-token",
		AccessToken:  "access-token",
		RefreshToken: "refresh-token",
		AccountID:    "account-1",
		LastRefresh:  "2026-07-22T12:34:56Z",
		Metadata: map[string]any{
			"agent_private_key": "private-key",
		},
	}
	if err := storage.SaveTokenToFile(path); err != nil {
		t.Fatalf("SaveTokenToFile() error: %v", err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read auth file: %v", err)
	}
	if !bytes.HasSuffix(raw, []byte("\n")) {
		t.Fatal("Codex credential file must end with a newline")
	}
	var metadata map[string]any
	if err = json.Unmarshal(raw, &metadata); err != nil {
		t.Fatalf("decode auth file: %v", err)
	}
	if metadata["access_token"] != "access-token" || metadata["refresh_token"] != "refresh-token" || metadata["id_token"] != "id-token" {
		t.Fatalf("OAuth token bundle was not preserved: %#v", metadata)
	}
	if metadata["agent_private_key"] != "private-key" {
		t.Fatalf("agent_private_key = %#v", metadata["agent_private_key"])
	}
	assertCodexPrivateCredentialMode(t, path)
	assertNoCodexCredentialTempFiles(t, dir, "codex.json")
}

func TestCodexTokenStorageSaveTokenToFileTightensSameCredential(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "codex.json")
	storage := &CodexTokenStorage{
		IDToken:      "id-token",
		AccessToken:  "access-token",
		RefreshToken: "refresh-token",
		AccountID:    "account-1",
		LastRefresh:  "2026-07-22T12:34:56Z",
	}
	if err := storage.SaveTokenToFile(path); err != nil {
		t.Fatalf("first SaveTokenToFile() error: %v", err)
	}
	if err := os.Chmod(path, 0o666); err != nil {
		t.Fatalf("loosen auth file permissions: %v", err)
	}
	if err := storage.SaveTokenToFile(path); err != nil {
		t.Fatalf("second SaveTokenToFile() error: %v", err)
	}

	assertCodexPrivateCredentialMode(t, path)
	assertNoCodexCredentialTempFiles(t, dir, "codex.json")
}

func assertCodexPrivateCredentialMode(t *testing.T, path string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat credential file: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("credential mode = %o, want 600", got)
	}
}

func assertNoCodexCredentialTempFiles(t *testing.T, dir, name string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, "."+name+".tmp-*"))
	if err != nil {
		t.Fatalf("glob credential temp files: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("credential temp files were not removed: %v", matches)
	}
}
