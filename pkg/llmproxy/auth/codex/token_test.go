package codex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCodexTokenStorage_SaveTokenToFile(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "codex_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tempDir) }()

	authFilePath := filepath.Join(tempDir, "token.json")

	ts := &CodexTokenStorage{
		IDToken:      "id_token",
		AccessToken:  "access_token",
		RefreshToken: "refresh_token",
		AccountID:    "acc_123",
		Email:        "test@example.com",
	}

	if err := ts.SaveTokenToFile(authFilePath); err != nil {
		t.Fatalf("SaveTokenToFile failed: %v", err)
	}

	// Read back and verify
	data, err := os.ReadFile(authFilePath)
	if err != nil {
		t.Fatalf("failed to read token file: %v", err)
	}

	var tsLoaded CodexTokenStorage
	if err := json.Unmarshal(data, &tsLoaded); err != nil {
		t.Fatalf("failed to unmarshal token: %v", err)
	}

	if tsLoaded.Type != "codex" {
		t.Errorf("expected type codex, got %s", tsLoaded.Type)
	}
	if tsLoaded.Email != ts.Email {
		t.Errorf("expected email %s, got %s", ts.Email, tsLoaded.Email)
	}
}

func TestSaveTokenToFile_MkdirFail(t *testing.T) {
	// Use a path that's impossible to create (like a file as a directory)
	tempFile, _ := os.CreateTemp("", "mkdir_fail")
	defer func() { _ = os.Remove(tempFile.Name()) }()

	authFilePath := filepath.Join(tempFile.Name(), "token.json")
	ts := &CodexTokenStorage{}
	err := ts.SaveTokenToFile(authFilePath)
	if err == nil {
		t.Error("expected error for invalid directory path")
	}
}
