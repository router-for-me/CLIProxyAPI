package codebuddy

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestTokenStorageSaveTokenToFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "codebuddy-test.json")

	storage := &TokenStorage{
		AccessToken:   "access-1",
		RefreshToken:  "refresh-1",
		TokenType:     "Bearer",
		UID:           "u-1",
		Nickname:      "tester",
		Expired:       "2030-01-01T00:00:00Z",
		EnabledModels: []string{"hy3", "glm-5.2"},
		Metadata: map[string]any{
			"extra": "value",
		},
	}
	if err := storage.SaveTokenToFile(path); err != nil {
		t.Fatalf("SaveTokenToFile returned error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("failed to read saved token file: %v", err)
	}
	var saved map[string]any
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatalf("saved file is not valid JSON: %v", err)
	}
	if saved["type"] != "codebuddy-cn" {
		t.Errorf("type = %v, want codebuddy", saved["type"])
	}
	if saved["access_token"] != "access-1" || saved["refresh_token"] != "refresh-1" {
		t.Errorf("tokens not persisted: %v", saved)
	}
	if saved["extra"] != "value" {
		t.Errorf("metadata not merged: %v", saved)
	}
	enabled, ok := saved["enabled_models"].([]any)
	if !ok || len(enabled) != 2 {
		t.Errorf("enabled_models not persisted: %v", saved["enabled_models"])
	}
}

func TestTokenStorageIsExpired(t *testing.T) {
	storage := &TokenStorage{}
	if storage.IsExpired() {
		t.Error("empty expiry must not be reported as expired")
	}
	storage.Expired = "not-a-time"
	if !storage.IsExpired() {
		t.Error("unparsable expiry must be reported as expired")
	}
	storage.Expired = "2000-01-01T00:00:00Z"
	if !storage.IsExpired() {
		t.Error("past expiry must be reported as expired")
	}
	storage.Expired = "2100-01-01T00:00:00Z"
	if storage.IsExpired() {
		t.Error("future expiry must not be reported as expired")
	}
}

func TestBuildTokenStorage(t *testing.T) {
	token := &TokenResponse{
		AccessToken:  "a",
		RefreshToken: "r",
		TokenType:    "",
		ExpiresIn:    3600,
		Domain:       "d",
	}
	models := []ModelInfo{{ID: "hy3", Name: "HY3"}, {ID: "glm-5.2", Name: "GLM 5.2"}}
	storage := BuildTokenStorage(token, &AccountInfo{UID: "u", Nickname: "n"}, models)

	if storage.TokenType != "Bearer" {
		t.Errorf("default token type = %q, want Bearer", storage.TokenType)
	}
	if storage.UID != "u" || storage.Nickname != "n" || storage.Domain != "d" {
		t.Errorf("account fields not populated: %+v", storage)
	}
	if len(storage.EnabledModels) != 2 || storage.EnabledModels[0] != "hy3" {
		t.Errorf("enabled models mismatch: %v", storage.EnabledModels)
	}
	var meta []ModelInfo
	if err := json.Unmarshal([]byte(storage.ModelsMeta), &meta); err != nil {
		t.Fatalf("models_meta is not valid JSON: %v", err)
	}
	if len(meta) != 2 || meta[0].ID != "hy3" {
		t.Errorf("models_meta content mismatch: %s", storage.ModelsMeta)
	}
	if storage.Expired == "" {
		t.Error("expired timestamp should be derived from expiresIn")
	}
}

func TestTokenResponseExpiresAtFallback(t *testing.T) {
	token := &TokenResponse{}
	if token.ExpiresAt() <= 0 {
		t.Error("missing expiresIn must fall back to a future expiry")
	}
}

func TestBuildTokenStorageFiltersPlaceholderModels(t *testing.T) {
	token := &TokenResponse{AccessToken: "a", RefreshToken: "r"}
	models := []ModelInfo{
		{ID: "glm-5.2", Name: "GLM 5.2"},
		{ID: "auto", Name: "Auto"},
		{ID: "hy3", Name: "HY3"},
	}
	storage := BuildTokenStorage(token, nil, models)

	for _, id := range storage.EnabledModels {
		if id == "auto" {
			t.Errorf("placeholder 'auto' should not appear in enabled_models: %v", storage.EnabledModels)
		}
	}
	if len(storage.EnabledModels) != 2 {
		t.Errorf("expected 2 routable models, got %d: %v", len(storage.EnabledModels), storage.EnabledModels)
	}
	var meta []ModelInfo
	if err := json.Unmarshal([]byte(storage.ModelsMeta), &meta); err != nil {
		t.Fatalf("models_meta is not valid JSON: %v", err)
	}
	for _, m := range meta {
		if m.ID == "auto" {
			t.Errorf("placeholder 'auto' should not appear in models_meta")
		}
	}
}
