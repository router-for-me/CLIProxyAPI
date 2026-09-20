package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSaveTokenToFile_PreservesCustomMetadata(t *testing.T) {
	tempDir := t.TempDir()
	authFilePath := filepath.Join(tempDir, "claude-test.json")

	storage := &ClaudeTokenStorage{
		Type:         "claude",
		Email:        "user@example.com",
		AccessToken:  "new-claude-access",
		RefreshToken: "new-claude-refresh",
		Expire:       "2026-12-31T23:59:59Z",
		LastRefresh:  "2026-04-14T12:00:00Z",
	}
	storage.SetMetadata(map[string]any{
		"disabled":  false,
		"prefix":    "claude-prefix",
		"note":      "claude custom note",
		"proxy_url": "http://proxy:8080",
		"weight":    float64(5),
	})

	if errSave := storage.SaveTokenToFile(authFilePath); errSave != nil {
		t.Fatalf("SaveTokenToFile() error = %v", errSave)
	}

	savedRaw, errRead := os.ReadFile(authFilePath)
	if errRead != nil {
		t.Fatalf("os.ReadFile error = %v", errRead)
	}

	var saved map[string]any
	if errUnmarshal := json.Unmarshal(savedRaw, &saved); errUnmarshal != nil {
		t.Fatalf("json.Unmarshal error = %v", errUnmarshal)
	}

	if saved["access_token"] != "new-claude-access" {
		t.Errorf("access_token = %v, want new-claude-access", saved["access_token"])
	}
	if saved["prefix"] != "claude-prefix" {
		t.Errorf("prefix = %v, want claude-prefix", saved["prefix"])
	}
	if saved["note"] != "claude custom note" {
		t.Errorf("note = %v, want claude custom note", saved["note"])
	}
	if saved["proxy_url"] != "http://proxy:8080" {
		t.Errorf("proxy_url = %v, want http://proxy:8080", saved["proxy_url"])
	}
	if saved["weight"] != float64(5) {
		t.Errorf("weight = %v, want 5", saved["weight"])
	}
}
