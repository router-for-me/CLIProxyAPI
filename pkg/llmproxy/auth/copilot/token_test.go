package copilot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCopilotTokenStorage_SaveTokenToFile(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "copilot_test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer func() { _ = os.RemoveAll(tempDir) }()

	authFilePath := filepath.Join(tempDir, "token.json")

	ts := &CopilotTokenStorage{
		AccessToken: "access",
		Username:    "user",
	}

	if err := ts.SaveTokenToFile(authFilePath); err != nil {
		t.Fatalf("SaveTokenToFile failed: %v", err)
	}

	// Read back and verify
	data, err := os.ReadFile(authFilePath)
	if err != nil {
		t.Fatalf("failed to read token file: %v", err)
	}

	var tsLoaded CopilotTokenStorage
	if err := json.Unmarshal(data, &tsLoaded); err != nil {
		t.Fatalf("failed to unmarshal token: %v", err)
	}

	if tsLoaded.Type != "github-copilot" {
		t.Errorf("expected type github-copilot, got %s", tsLoaded.Type)
	}
}
