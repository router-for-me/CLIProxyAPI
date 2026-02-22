package kiro

import (
	"os"
	"path/filepath"
	"testing"
)

func TestKiroTokenStorage_SaveAndLoad(t *testing.T) {
	tempDir := t.TempDir()
	path := filepath.Join(tempDir, "kiro-token.json")

	ts := &KiroTokenStorage{
		Type:        "kiro",
		AccessToken: "access",
		Email:       "test@example.com",
	}

	if err := ts.SaveTokenToFile(path); err != nil {
		t.Fatalf("SaveTokenToFile failed: %v", err)
	}

	loaded, err := LoadFromFile(path)
	if err != nil {
		t.Fatalf("LoadFromFile failed: %v", err)
	}

	if loaded.AccessToken != ts.AccessToken || loaded.Email != ts.Email {
		t.Errorf("loaded data mismatch: %+v", loaded)
	}

	// Test ToTokenData
	td := ts.ToTokenData()
	if td.AccessToken != ts.AccessToken || td.Email != ts.Email {
		t.Errorf("ToTokenData failed: %+v", td)
	}
}

func TestLoadFromFile_Errors(t *testing.T) {
	_, err := LoadFromFile("non-existent")
	if err == nil {
		t.Error("expected error for non-existent file")
	}

	tempFile, _ := os.CreateTemp("", "invalid-json")
	defer func() { _ = os.Remove(tempFile.Name()) }()
	_ = os.WriteFile(tempFile.Name(), []byte("invalid"), 0600)

	_, err = LoadFromFile(tempFile.Name())
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}
