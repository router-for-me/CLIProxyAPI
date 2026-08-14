package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	sdkAuth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestSaveTokenRecord_PreservesExistingAuthFileSettings(t *testing.T) {
	authDir := t.TempDir()
	fileName := "codex-user@example.com.json"
	filePath := filepath.Join(authDir, fileName)

	// User configured fields on existing OAuth account
	initialContent := map[string]any{
		"type":          "codex",
		"email":         "user@example.com",
		"access_token":  "old-access",
		"refresh_token": "old-refresh",
		"prefix":        "custom-prefix",
		"websockets":    false,
		"note":          "my important account",
		"proxy_url":     "http://127.0.0.1:8080",
		"weight":        float64(5),
		"headers":       map[string]any{"User-Agent": "Custom"},
		"models":        []any{"o3-mini"},
		"thinking":      map[string]any{"enabled": true},
		"priority":      float64(2),
	}
	raw, errMarshal := json.Marshal(initialContent)
	if errMarshal != nil {
		t.Fatalf("marshal initial error: %v", errMarshal)
	}
	if errWrite := os.WriteFile(filePath, raw, 0o600); errWrite != nil {
		t.Fatalf("write initial file error: %v", errWrite)
	}

	cfg := &config.Config{
		AuthDir: authDir,
	}
	h := NewHandler(cfg, "", nil)

	// Re-login arrives with new OAuth tokens
	tokenStorage := &codex.CodexTokenStorage{
		Type:         "codex",
		Email:        "user@example.com",
		AccessToken:  "new-access-token",
		RefreshToken: "new-refresh-token",
		IDToken:      "new-id-token",
		AccountID:    "act-123",
		Expire:       "2026-12-31T23:59:59Z",
	}
	newRecord := &coreauth.Auth{
		ID:       fileName,
		Provider: "codex",
		FileName: fileName,
		Storage:  tokenStorage,
		Metadata: map[string]any{
			"email":      tokenStorage.Email,
			"account_id": tokenStorage.AccountID,
		},
	}

	savedPath, errSave := h.saveTokenRecord(context.Background(), newRecord)
	if errSave != nil {
		t.Fatalf("saveTokenRecord error: %v", errSave)
	}
	if savedPath != filePath {
		t.Fatalf("savedPath = %s, want %s", savedPath, filePath)
	}

	savedRaw, errRead := os.ReadFile(filePath)
	if errRead != nil {
		t.Fatalf("ReadFile error: %v", errRead)
	}
	var saved map[string]any
	if errUnmarshal := json.Unmarshal(savedRaw, &saved); errUnmarshal != nil {
		t.Fatalf("Unmarshal error: %v", errUnmarshal)
	}

	// Verify new OAuth token data was updated
	if saved["access_token"] != "new-access-token" {
		t.Errorf("access_token = %v, want new-access-token", saved["access_token"])
	}
	if saved["refresh_token"] != "new-refresh-token" {
		t.Errorf("refresh_token = %v, want new-refresh-token", saved["refresh_token"])
	}

	// Verify user-configured fields were preserved
	if saved["prefix"] != "custom-prefix" {
		t.Errorf("prefix = %v, want custom-prefix", saved["prefix"])
	}
	if saved["websockets"] != false {
		t.Errorf("websockets = %v, want false", saved["websockets"])
	}
	if saved["note"] != "my important account" {
		t.Errorf("note = %v, want my important account", saved["note"])
	}
	if saved["proxy_url"] != "http://127.0.0.1:8080" {
		t.Errorf("proxy_url = %v, want http://127.0.0.1:8080", saved["proxy_url"])
	}
	if saved["weight"] != float64(5) {
		t.Errorf("weight = %v, want 5", saved["weight"])
	}
	if !reflect.DeepEqual(saved["headers"], map[string]any{"User-Agent": "Custom"}) {
		t.Errorf("headers = %#v, want map[User-Agent:Custom]", saved["headers"])
	}
	if !reflect.DeepEqual(saved["models"], []any{"o3-mini"}) {
		t.Errorf("models = %#v, want [o3-mini]", saved["models"])
	}
	if !reflect.DeepEqual(saved["thinking"], map[string]any{"enabled": true}) {
		t.Errorf("thinking = %#v, want map[enabled:true]", saved["thinking"])
	}
	if saved["priority"] != float64(2) {
		t.Errorf("priority = %v, want 2", saved["priority"])
	}
}

func TestPatchAuthFileFields_DeletesPluginFields(t *testing.T) {
	gin.SetMode(gin.TestMode)
	authDir := t.TempDir()
	fileName := "plugin-auth.json"
	filePath := filepath.Join(authDir, fileName)

	initialContent := map[string]any{
		"type":    "demo-plugin",
		"token":   "tok-123",
		"weight":  float64(10),
		"headers": map[string]any{"X-Header": "val"},
	}
	raw, errMarshal := json.Marshal(initialContent)
	if errMarshal != nil {
		t.Fatalf("marshal error: %v", errMarshal)
	}
	if errWrite := os.WriteFile(filePath, raw, 0o600); errWrite != nil {
		t.Fatalf("write error: %v", errWrite)
	}

	store := sdkAuth.NewFileTokenStore()
	store.SetBaseDir(authDir)
	manager := coreauth.NewManager(store, nil, nil)
	record := &coreauth.Auth{
		ID:       fileName,
		FileName: fileName,
		Provider: "demo-plugin",
		Metadata: map[string]any{
			"type":    "demo-plugin",
			"token":   "tok-123",
			"weight":  float64(10),
			"headers": map[string]any{"X-Header": "val"},
		},
	}
	if _, errRegister := manager.Register(context.Background(), record); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}

	cfg := &config.Config{AuthDir: authDir}
	h := NewHandlerWithoutConfigFilePath(cfg, manager)

	// Patch weight: null to delete weight
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	body := `{"name":"plugin-auth.json","weight":null}`
	c.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/auth-files/fields", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	h.PatchAuthFileFields(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("PatchAuthFileFields status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	savedRaw, errRead := os.ReadFile(filePath)
	if errRead != nil {
		t.Fatalf("ReadFile error: %v", errRead)
	}
	var saved map[string]any
	if errUnmarshal := json.Unmarshal(savedRaw, &saved); errUnmarshal != nil {
		t.Fatalf("Unmarshal error: %v", errUnmarshal)
	}

	if _, exists := saved["weight"]; exists {
		t.Errorf("weight still exists in file after delete: %#v", saved["weight"])
	}
	if saved["token"] != "tok-123" {
		t.Errorf("token = %v, want tok-123", saved["token"])
	}
}
