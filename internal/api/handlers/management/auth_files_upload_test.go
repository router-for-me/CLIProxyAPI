package management

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestUploadAuthFile_PreservesPriorityAttributes(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	authDir := t.TempDir()
	manager := coreauth.NewManager(nil, nil, nil)
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)

	content := `{"type":"codex","email":"midai0530@gmail.com","priority":98}`

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "codex-midai0530@gmail.com-plus.json")
	if err != nil {
		t.Fatalf("failed to create multipart file: %v", err)
	}
	if _, err = part.Write([]byte(content)); err != nil {
		t.Fatalf("failed to write multipart content: %v", err)
	}
	if err = writer.Close(); err != nil {
		t.Fatalf("failed to close multipart writer: %v", err)
	}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v0/management/auth-files", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	ctx.Request = req

	h.UploadAuthFile(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected upload status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err = json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if status, _ := payload["status"].(string); status != "ok" {
		t.Fatalf("expected status ok, got %#v", payload["status"])
	}

	auth, ok := manager.GetByID("codex-midai0530@gmail.com-plus.json")
	if !ok || auth == nil {
		t.Fatalf("expected uploaded auth record to exist")
	}
	if got := auth.Attributes["priority"]; got != "98" {
		t.Fatalf("priority attribute = %q, want %q", got, "98")
	}
	if got := auth.Metadata["priority"]; got != float64(98) {
		t.Fatalf("priority metadata = %#v, want 98", got)
	}
}

func TestUploadAuthFile_ConvertsXAIGrokCLIAuthJSON(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	grokDir := filepath.Join(homeDir, ".grok")
	if err := os.MkdirAll(grokDir, 0o700); err != nil {
		t.Fatalf("failed to create grok dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(grokDir, "version.json"), []byte(`{"version":"0.2.93"}`), 0o600); err != nil {
		t.Fatalf("failed to write grok version: %v", err)
	}
	if err := os.WriteFile(filepath.Join(grokDir, "agent_id"), []byte("agent-test\n"), 0o600); err != nil {
		t.Fatalf("failed to write grok agent id: %v", err)
	}

	authDir := t.TempDir()
	manager := coreauth.NewManager(nil, nil, nil)
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)

	content := `{
  "https://auth.x.ai::b1a00492-073a-47ea-816f-4c329264a828": {
    "key": "session-token",
    "email": "free@example.com",
    "expires_at": "2026-07-09T22:10:24.480457024Z",
    "oidc_issuer": "https://auth.x.ai",
    "oidc_client_id": "b1a00492-073a-47ea-816f-4c329264a828",
    "principal_id": "principal-test"
  }
}`

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "auth.json")
	if err != nil {
		t.Fatalf("failed to create multipart file: %v", err)
	}
	if _, err = part.Write([]byte(content)); err != nil {
		t.Fatalf("failed to write multipart content: %v", err)
	}
	if err = writer.Close(); err != nil {
		t.Fatalf("failed to close multipart writer: %v", err)
	}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v0/management/auth-files", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	ctx.Request = req

	h.UploadAuthFile(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected upload status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	fileName := "xai-free@example.com.json"
	raw, err := os.ReadFile(filepath.Join(authDir, fileName))
	if err != nil {
		t.Fatalf("failed to read converted auth file: %v", err)
	}
	var saved map[string]any
	if err = json.Unmarshal(raw, &saved); err != nil {
		t.Fatalf("failed to decode converted auth file: %v", err)
	}
	if got := saved["type"]; got != "xai" {
		t.Fatalf("type = %#v, want xai", got)
	}
	if got := saved["auth_kind"]; got != "grok_cli_session" {
		t.Fatalf("auth_kind = %#v, want grok_cli_session", got)
	}
	if got := saved["access_token"]; got != "session-token" {
		t.Fatalf("access_token = %#v, want session-token", got)
	}
	if got := saved["base_url"]; got != "https://cli-chat-proxy.grok.com/v1" {
		t.Fatalf("base_url = %#v, want cli proxy", got)
	}
	if got := saved["grok_cli_version"]; got != "0.2.93" {
		t.Fatalf("grok_cli_version = %#v, want 0.2.93", got)
	}
	if got := saved["priority"]; got != float64(100) {
		t.Fatalf("priority = %#v, want 100", got)
	}
	if got := saved["grok_client_identifier"]; got != "agent-test" {
		t.Fatalf("grok_client_identifier = %#v, want agent-test", got)
	}

	auth, ok := manager.GetByID(fileName)
	if !ok || auth == nil {
		t.Fatalf("expected converted xai auth record to exist")
	}
	if auth.Provider != "xai" {
		t.Fatalf("provider = %q, want xai", auth.Provider)
	}
	if got := auth.Attributes["auth_kind"]; got != "grok_cli_session" {
		t.Fatalf("runtime auth_kind = %q, want grok_cli_session", got)
	}
	if got := auth.Attributes["priority"]; got != "100" {
		t.Fatalf("runtime priority = %q, want 100", got)
	}
}
