package management

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestImportCodeBuddyAuthByPath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := t.TempDir()
	authPath := filepath.Join(root, "desktop.info")
	writeCodeBuddyTestSession(t, authPath)
	configPath := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(configPath, []byte("host: 127.0.0.1\nport: 8317\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg := &config.Config{AuthDir: root}
	h := NewHandler(cfg, configPath, nil)

	body, _ := json.Marshal(map[string]string{"auth-file": authPath})
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/codebuddy/import", bytes.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.ImportCodeBuddyAuth(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if len(cfg.OpenAICompatibility) != 1 {
		t.Fatalf("openai compatibility entries = %d, want 1", len(cfg.OpenAICompatibility))
	}
	entry := cfg.OpenAICompatibility[0]
	if entry.AuthType != "codebuddy" || entry.AuthFile != authPath {
		t.Fatalf("entry = %#v", entry)
	}
	if !strings.Contains(rec.Body.String(), "desktop.info") {
		t.Fatalf("response did not include imported path: %s", rec.Body.String())
	}
}

func TestImportCodeBuddyAuthByUpload(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root := t.TempDir()
	configPath := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(configPath, []byte("host: 127.0.0.1\nport: 8317\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	cfg := &config.Config{AuthDir: root}
	h := NewHandler(cfg, configPath, nil)

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "desktop.info")
	if err != nil {
		t.Fatalf("create form file: %v", err)
	}
	_, _ = part.Write([]byte(`{"auth":{"accessToken":"access","refreshToken":"refresh"},"account":{"uid":"user"}}`))
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/codebuddy/import", &body)
	ctx.Request.Header.Set("Content-Type", writer.FormDataContentType())
	h.ImportCodeBuddyAuth(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	expected := filepath.Join(root, "workbuddy-desktop.info")
	if _, err := os.Stat(expected); err != nil {
		t.Fatalf("uploaded session was not saved at %s: %v", expected, err)
	}
	if len(cfg.OpenAICompatibility) != 1 || cfg.OpenAICompatibility[0].AuthFile != expected {
		t.Fatalf("config entry = %#v", cfg.OpenAICompatibility)
	}
}

func writeCodeBuddyTestSession(t *testing.T, path string) {
	t.Helper()
	data := []byte(`{"auth":{"accessToken":"access","refreshToken":"refresh"},"account":{"uid":"user"}}`)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write session: %v", err)
	}
}
