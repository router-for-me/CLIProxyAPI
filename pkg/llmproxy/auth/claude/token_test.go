package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestClaudeTokenStorage_SaveTokenToFile(t *testing.T) {
	tempDir := t.TempDir()
	authFilePath := filepath.Join(tempDir, "token.json")

	ts := &ClaudeTokenStorage{
		IDToken:      "id_token",
		AccessToken:  "access_token",
		RefreshToken: "refresh_token",
		Email:        "test@example.com",
	}

	if err := ts.SaveTokenToFile(authFilePath); err != nil {
		t.Fatalf("SaveTokenToFile failed: %v", err)
	}

	data, err := os.ReadFile(authFilePath)
	if err != nil {
		t.Fatalf("failed to read token file: %v", err)
	}

	var loaded ClaudeTokenStorage
	if err := json.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("failed to unmarshal token: %v", err)
	}
	if loaded.Type != "claude" {
		t.Fatalf("expected type claude, got %q", loaded.Type)
	}
}

func TestClaudeTokenStorage_SaveTokenToFileRejectsParentTraversal(t *testing.T) {
	ts := &ClaudeTokenStorage{}
	err := ts.SaveTokenToFile("../token.json")
	if err == nil {
		t.Fatal("expected error for parent traversal path")
	}
}
