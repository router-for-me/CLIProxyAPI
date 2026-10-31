package kiro

import (
	"os"
	"path/filepath"
	"testing"
)

func TestKiroTokenStorageSaveAndLoad(t *testing.T) {
	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "kiro-test.json")

	storage := &KiroTokenStorage{
		Type:         "kiro",
		AccessToken:  "test-access-token",
		RefreshToken: "test-refresh-token",
		ProfileArn:   "arn:aws:codewhisperer:us-east-1:123456789012:profile/P123",
		ExpiresAt:    "2026-12-31T23:59:59Z",
		AuthMethod:   "builder-id",
		Provider:     "AWS",
		ClientID:     "client-123",
		ClientSecret: "secret-456",
		Email:        "user@example.com",
	}

	if err := storage.SaveTokenToFile(filePath); err != nil {
		t.Fatalf("SaveTokenToFile failed: %v", err)
	}

	loaded, err := LoadFromFile(filePath)
	if err != nil {
		t.Fatalf("LoadFromFile failed: %v", err)
	}

	if loaded.AccessToken != storage.AccessToken {
		t.Errorf("loaded.AccessToken = %q, want %q", loaded.AccessToken, storage.AccessToken)
	}
	if loaded.Email != storage.Email {
		t.Errorf("loaded.Email = %q, want %q", loaded.Email, storage.Email)
	}

	tokenData := loaded.ToTokenData()
	if tokenData.AccessToken != storage.AccessToken {
		t.Errorf("tokenData.AccessToken = %q, want %q", tokenData.AccessToken, storage.AccessToken)
	}

	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		t.Fatalf("file does not exist at %s", filePath)
	}
}
