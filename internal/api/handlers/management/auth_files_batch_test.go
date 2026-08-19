package management

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestUploadAuthFile_BatchMultipart(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	authDir := t.TempDir()
	manager := coreauth.NewManager(nil, nil, nil)
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)

	files := []struct {
		name    string
		content string
	}{
		{name: "alpha.json", content: `{"type":"codex","email":"alpha@example.com"}`},
		{name: "beta.json", content: `{"type":"claude","email":"beta@example.com"}`},
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for _, file := range files {
		part, err := writer.CreateFormFile("file", file.name)
		if err != nil {
			t.Fatalf("failed to create multipart file: %v", err)
		}
		if _, err = part.Write([]byte(file.content)); err != nil {
			t.Fatalf("failed to write multipart content: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
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
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if got, ok := payload["uploaded"].(float64); !ok || int(got) != len(files) {
		t.Fatalf("expected uploaded=%d, got %#v", len(files), payload["uploaded"])
	}

	for _, file := range files {
		fullPath := filepath.Join(authDir, file.name)
		data, err := os.ReadFile(fullPath)
		if err != nil {
			t.Fatalf("expected uploaded file %s to exist: %v", file.name, err)
		}
		if string(data) != file.content {
			t.Fatalf("expected file %s content %q, got %q", file.name, file.content, string(data))
		}
	}

	auths := manager.List()
	if len(auths) != len(files) {
		t.Fatalf("expected %d auth entries, got %d", len(files), len(auths))
	}
}

func TestUploadAuthFile_BatchMultipart_InvalidJSONDoesNotOverwriteExistingFile(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	authDir := t.TempDir()
	manager := coreauth.NewManager(nil, nil, nil)
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)

	existingName := "alpha.json"
	existingContent := `{"type":"codex","email":"alpha@example.com"}`
	if err := os.WriteFile(filepath.Join(authDir, existingName), []byte(existingContent), 0o600); err != nil {
		t.Fatalf("failed to seed existing auth file: %v", err)
	}

	files := []struct {
		name    string
		content string
	}{
		{name: existingName, content: `{"type":"codex"`},
		{name: "beta.json", content: `{"type":"claude","email":"beta@example.com"}`},
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for _, file := range files {
		part, err := writer.CreateFormFile("file", file.name)
		if err != nil {
			t.Fatalf("failed to create multipart file: %v", err)
		}
		if _, err = part.Write([]byte(file.content)); err != nil {
			t.Fatalf("failed to write multipart content: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("failed to close multipart writer: %v", err)
	}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v0/management/auth-files", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	ctx.Request = req

	h.UploadAuthFile(ctx)

	if rec.Code != http.StatusMultiStatus {
		t.Fatalf("expected upload status %d, got %d with body %s", http.StatusMultiStatus, rec.Code, rec.Body.String())
	}

	data, err := os.ReadFile(filepath.Join(authDir, existingName))
	if err != nil {
		t.Fatalf("expected existing auth file to remain readable: %v", err)
	}
	if string(data) != existingContent {
		t.Fatalf("expected existing auth file to remain %q, got %q", existingContent, string(data))
	}

	betaData, err := os.ReadFile(filepath.Join(authDir, "beta.json"))
	if err != nil {
		t.Fatalf("expected valid auth file to be created: %v", err)
	}
	if string(betaData) != files[1].content {
		t.Fatalf("expected beta auth file content %q, got %q", files[1].content, string(betaData))
	}
}

func TestBuildAuthFromFileData_NormalizesKiroIDETokenJSON(t *testing.T) {
	authDir := t.TempDir()
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)
	path := filepath.Join(authDir, "kiro-auth-token.json")
	data := []byte(`{
		"accessToken":"access-token",
		"refreshToken":"refresh-token",
		"profileArn":"arn:aws:codewhisperer:us-east-1:123456789012:profile/test",
		"expiresAt":"2026-06-19T12:00:00Z",
		"authMethod":"Google",
		"provider":"Google",
		"clientId":"client-id",
		"clientSecret":"client-secret",
		"clientIdHash":"client-hash",
		"email":"user@example.com",
		"startUrl":"https://example.awsapps.com/start",
		"region":"us-east-1"
	}`)

	auth, err := h.buildAuthFromFileData(path, data)
	if err != nil {
		t.Fatalf("buildAuthFromFileData() error = %v", err)
	}
	if auth.Provider != "kiro" {
		t.Fatalf("Provider = %q, want kiro", auth.Provider)
	}
	if auth.Label != "user@example.com" {
		t.Fatalf("Label = %q, want user@example.com", auth.Label)
	}

	wantMetadata := map[string]string{
		"type":           "kiro",
		"access_token":   "access-token",
		"refresh_token":  "refresh-token",
		"profile_arn":    "arn:aws:codewhisperer:us-east-1:123456789012:profile/test",
		"expires_at":     "2026-06-19T12:00:00Z",
		"auth_method":    "google",
		"provider":       "Google",
		"client_id":      "client-id",
		"client_secret":  "client-secret",
		"client_id_hash": "client-hash",
		"email":          "user@example.com",
		"start_url":      "https://example.awsapps.com/start",
		"region":         "us-east-1",
	}
	for key, want := range wantMetadata {
		got, _ := auth.Metadata[key].(string)
		if got != want {
			t.Fatalf("metadata[%q] = %q, want %q", key, got, want)
		}
	}
}

func TestBuildAuthFromFileData_DoesNotNormalizeTypedAuthJSON(t *testing.T) {
	authDir := t.TempDir()
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)
	path := filepath.Join(authDir, "codex.json")
	data := []byte(`{"type":"codex","accessToken":"looks-like-token","email":"codex@example.com"}`)

	auth, err := h.buildAuthFromFileData(path, data)
	if err != nil {
		t.Fatalf("buildAuthFromFileData() error = %v", err)
	}
	if auth.Provider != "codex" {
		t.Fatalf("Provider = %q, want codex", auth.Provider)
	}
	if _, exists := auth.Metadata["access_token"]; exists {
		t.Fatalf("typed auth JSON was unexpectedly normalized: %#v", auth.Metadata)
	}
	if got, _ := auth.Metadata["accessToken"].(string); got != "looks-like-token" {
		t.Fatalf("metadata accessToken = %q, want original camelCase value", got)
	}
}

func TestBuildAuthFromFileData_DoesNotNormalizeUntypedNonKiroOAuthJSON(t *testing.T) {
	authDir := t.TempDir()
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)
	path := filepath.Join(authDir, "oauth-token.json")
	data := []byte(`{
		"accessToken":"access-token",
		"refreshToken":"refresh-token",
		"provider":"generic-oauth",
		"clientId":"client-id",
		"clientSecret":"client-secret",
		"email":"oauth@example.com"
	}`)

	auth, err := h.buildAuthFromFileData(path, data)
	if err != nil {
		t.Fatalf("buildAuthFromFileData() error = %v", err)
	}
	if auth.Provider != "unknown" {
		t.Fatalf("Provider = %q, want unknown", auth.Provider)
	}
	if _, exists := auth.Metadata["access_token"]; exists {
		t.Fatalf("untyped generic OAuth JSON was unexpectedly normalized: %#v", auth.Metadata)
	}
	if got, _ := auth.Metadata["accessToken"].(string); got != "access-token" {
		t.Fatalf("metadata accessToken = %q, want original camelCase value", got)
	}
}

func TestWriteAuthFile_PersistsNormalizedKiroIDETokenJSON(t *testing.T) {
	authDir := t.TempDir()
	manager := coreauth.NewManager(nil, nil, nil)
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	data := []byte(`{
		"accessToken":"access-token",
		"refreshToken":"refresh-token",
		"profileArn":"arn:aws:codewhisperer:us-east-1:123456789012:profile/test",
		"expiresAt":"2026-06-19T12:00:00Z",
		"authMethod":"Google",
		"provider":"Google",
		"email":"user@example.com"
	}`)

	if err := h.writeAuthFile(context.Background(), "kiro-auth-token.json", data); err != nil {
		t.Fatalf("writeAuthFile() error = %v", err)
	}

	written, err := os.ReadFile(filepath.Join(authDir, "kiro-auth-token.json"))
	if err != nil {
		t.Fatalf("read written auth file: %v", err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(written, &metadata); err != nil {
		t.Fatalf("unmarshal written auth file: %v", err)
	}
	if got, _ := metadata["type"].(string); got != "kiro" {
		t.Fatalf("written metadata type = %q, want kiro", got)
	}
	if got, _ := metadata["access_token"].(string); got != "access-token" {
		t.Fatalf("written access_token = %q, want access-token", got)
	}
	if _, exists := metadata["accessToken"]; exists {
		t.Fatalf("written metadata still contains raw accessToken: %#v", metadata)
	}

	auth, ok := manager.GetByID("kiro-auth-token.json")
	if !ok {
		t.Fatal("expected auth manager to contain uploaded Kiro auth")
	}
	if auth.Provider != "kiro" {
		t.Fatalf("manager auth provider = %q, want kiro", auth.Provider)
	}
}

func TestDeleteAuthFile_BatchQuery(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	authDir := t.TempDir()
	files := []string{"alpha.json", "beta.json"}
	for _, name := range files {
		if err := os.WriteFile(filepath.Join(authDir, name), []byte(`{"type":"codex"}`), 0o600); err != nil {
			t.Fatalf("failed to write auth file %s: %v", name, err)
		}
	}

	manager := coreauth.NewManager(nil, nil, nil)
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, manager)
	h.tokenStore = &memoryAuthStore{}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(
		http.MethodDelete,
		"/v0/management/auth-files?name="+url.QueryEscape(files[0])+"&name="+url.QueryEscape(files[1]),
		nil,
	)
	ctx.Request = req

	h.DeleteAuthFile(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected delete status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if got, ok := payload["deleted"].(float64); !ok || int(got) != len(files) {
		t.Fatalf("expected deleted=%d, got %#v", len(files), payload["deleted"])
	}

	for _, name := range files {
		if _, err := os.Stat(filepath.Join(authDir, name)); !os.IsNotExist(err) {
			t.Fatalf("expected auth file %s to be removed, stat err: %v", name, err)
		}
	}
}
