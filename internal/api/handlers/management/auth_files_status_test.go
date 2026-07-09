package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	fileauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestPatchAuthFileStatus_MergesDiskMetadataBeforePersist(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	authDir := t.TempDir()
	fileName := "codex.json"
	filePath := filepath.Join(authDir, fileName)
	original := `{"type":"codex","access_token":"access","refresh_token":"refresh","email":"u@example.com","nested":{"keep":true}}`
	if errWrite := os.WriteFile(filePath, []byte(original), 0o600); errWrite != nil {
		t.Fatalf("failed to write auth file: %v", errWrite)
	}

	store := fileauth.NewFileTokenStore()
	store.SetBaseDir(authDir)
	manager := coreauth.NewManager(store, nil, nil)
	record := &coreauth.Auth{
		ID:       fileName,
		FileName: fileName,
		Provider: "codex",
		Attributes: map[string]string{
			"path": filePath,
		},
		Metadata: map[string]any{
			"name": fileName,
		},
	}
	if _, errRegister := manager.Register(context.Background(), record); errRegister != nil {
		t.Fatalf("failed to register auth record: %v", errRegister)
	}
	if errWrite := os.WriteFile(filePath, []byte(original), 0o600); errWrite != nil {
		t.Fatalf("failed to restore full auth file after registration: %v", errWrite)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v0/management/auth-files?name="+fileName, strings.NewReader(`{"name":"codex.json","disabled":true}`))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req

	h.PatchAuthFileStatus(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	raw, errRead := os.ReadFile(filePath)
	if errRead != nil {
		t.Fatalf("failed to read auth file: %v", errRead)
	}
	var data map[string]any
	if errUnmarshal := json.Unmarshal(raw, &data); errUnmarshal != nil {
		t.Fatalf("failed to unmarshal auth file: %v; raw=%s", errUnmarshal, string(raw))
	}
	if got := data["type"]; got != "codex" {
		t.Fatalf("type = %#v, want codex; raw=%s", got, string(raw))
	}
	if got := data["access_token"]; got != "access" {
		t.Fatalf("access_token = %#v, want preserved access; raw=%s", got, string(raw))
	}
	if got := data["refresh_token"]; got != "refresh" {
		t.Fatalf("refresh_token = %#v, want preserved refresh; raw=%s", got, string(raw))
	}
	if got := data["disabled"]; got != true {
		t.Fatalf("disabled = %#v, want true; raw=%s", got, string(raw))
	}
}

func TestPatchAuthFileStatus_AcceptsMetadataAndTypeFromRequest(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	authDir := t.TempDir()
	fileName := "partial.json"
	filePath := filepath.Join(authDir, fileName)
	if errWrite := os.WriteFile(filePath, []byte(`{"name":"partial.json"}`), 0o600); errWrite != nil {
		t.Fatalf("failed to write auth file: %v", errWrite)
	}

	store := fileauth.NewFileTokenStore()
	store.SetBaseDir(authDir)
	manager := coreauth.NewManager(store, nil, nil)
	record := &coreauth.Auth{
		ID:       fileName,
		FileName: fileName,
		Provider: "unknown",
		Attributes: map[string]string{
			"path": filePath,
		},
		Metadata: map[string]any{
			"name": fileName,
		},
	}
	if _, errRegister := manager.Register(context.Background(), record); errRegister != nil {
		t.Fatalf("failed to register auth record: %v", errRegister)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	body := `{"name":"partial.json","disabled":false,"type":"codex","metadata":{"access_token":"access","refresh_token":"refresh","email":"u@example.com"}}`
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v0/management/auth-files?name="+fileName, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req

	h.PatchAuthFileStatus(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	raw, errRead := os.ReadFile(filePath)
	if errRead != nil {
		t.Fatalf("failed to read auth file: %v", errRead)
	}
	var data map[string]any
	if errUnmarshal := json.Unmarshal(raw, &data); errUnmarshal != nil {
		t.Fatalf("failed to unmarshal auth file: %v; raw=%s", errUnmarshal, string(raw))
	}
	if got := data["type"]; got != "codex" {
		t.Fatalf("type = %#v, want codex; raw=%s", got, string(raw))
	}
	if got := data["access_token"]; got != "access" {
		t.Fatalf("access_token = %#v, want request metadata access; raw=%s", got, string(raw))
	}
	if got := data["disabled"]; got != false {
		t.Fatalf("disabled = %#v, want false; raw=%s", got, string(raw))
	}

	updated, ok := manager.GetByID(fileName)
	if !ok || updated == nil {
		t.Fatalf("expected updated auth")
	}
	if got := updated.Provider; got != "codex" {
		t.Fatalf("updated.Provider = %q, want codex", got)
	}
}

// Regression: re-enabling an account whose in-memory record is partial must
// sync the runtime fields the executors/scheduler read (priority, proxy_url,
// custom headers, ...) from the metadata merged off disk, not only after a
// later file reload.
func TestPatchAuthFileStatus_SyncsRuntimeFieldsFromMergedMetadata(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	authDir := t.TempDir()
	fileName := "codex.json"
	filePath := filepath.Join(authDir, fileName)
	original := `{"type":"codex","access_token":"access","priority":5,"proxy_url":"http://proxy.local","headers":{"X-Old":"old"}}`
	if errWrite := os.WriteFile(filePath, []byte(original), 0o600); errWrite != nil {
		t.Fatalf("seed auth file: %v", errWrite)
	}

	store := fileauth.NewFileTokenStore()
	store.SetBaseDir(authDir)
	manager := coreauth.NewManager(store, nil, nil)
	// In-memory record is partial: no priority/proxy/headers in runtime fields.
	record := &coreauth.Auth{
		ID:         fileName,
		FileName:   fileName,
		Provider:   "codex",
		Attributes: map[string]string{"path": filePath},
		Metadata:   map[string]any{"name": fileName},
	}
	if _, errRegister := manager.Register(context.Background(), record); errRegister != nil {
		t.Fatalf("register auth record: %v", errRegister)
	}
	if errWrite := os.WriteFile(filePath, []byte(original), 0o600); errWrite != nil {
		t.Fatalf("restore seed after registration: %v", errWrite)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v0/management/auth-files?name="+fileName, strings.NewReader(`{"name":"codex.json","disabled":false}`))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req
	h.PatchAuthFileStatus(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	updated, ok := manager.GetByID(fileName)
	if !ok || updated == nil {
		t.Fatalf("expected updated auth")
	}
	if got := updated.Attributes["priority"]; got != "5" {
		t.Fatalf("Attributes[priority] = %q, want \"5\" (synced for scheduler)", got)
	}
	if got := updated.ProxyURL; got != "http://proxy.local" {
		t.Fatalf("ProxyURL = %q, want http://proxy.local (synced from metadata)", got)
	}
	if got := updated.Attributes["header:X-Old"]; got != "old" {
		t.Fatalf("Attributes[header:X-Old] = %q, want \"old\" (custom headers synced)", got)
	}
}
