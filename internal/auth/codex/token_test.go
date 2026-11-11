package codex

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSaveTokenToFile_PreservesCustomMetadata(t *testing.T) {
	tempDir := t.TempDir()
	authFilePath := filepath.Join(tempDir, "codex-test.json")

	storage := &CodexTokenStorage{
		Type:         "codex",
		Email:        "user@example.com",
		AccessToken:  "new-access-token",
		RefreshToken: "new-refresh-token",
		IDToken:      "new-id-token",
		AccountID:    "new-account",
		Expire:       "2026-12-31T23:59:59Z",
		LastRefresh:  "2026-04-14T12:00:00Z",
	}
	storage.SetMetadata(map[string]any{
		"disabled":   false,
		"prefix":     "my-prefix",
		"websockets": false,
		"note":       "my important note",
		"proxy_url":  "http://proxy:8080",
		"weight":     float64(42),
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

	// Verify updated OAuth token fields
	if saved["access_token"] != "new-access-token" {
		t.Errorf("access_token = %v, want new-access-token", saved["access_token"])
	}
	if saved["refresh_token"] != "new-refresh-token" {
		t.Errorf("refresh_token = %v, want new-refresh-token", saved["refresh_token"])
	}
	if saved["id_token"] != "new-id-token" {
		t.Errorf("id_token = %v, want new-id-token", saved["id_token"])
	}
	if saved["account_id"] != "new-account" {
		t.Errorf("account_id = %v, want new-account", saved["account_id"])
	}

	// Verify custom fields in metadata
	if saved["prefix"] != "my-prefix" {
		t.Errorf("prefix = %v, want my-prefix", saved["prefix"])
	}
	if saved["websockets"] != false {
		t.Errorf("websockets = %v, want false", saved["websockets"])
	}
	if saved["note"] != "my important note" {
		t.Errorf("note = %v, want my important note", saved["note"])
	}
	if saved["proxy_url"] != "http://proxy:8080" {
		t.Errorf("proxy_url = %v, want http://proxy:8080", saved["proxy_url"])
	}
	if saved["weight"] != float64(42) {
		t.Errorf("weight = %v, want 42", saved["weight"])
	}
}
