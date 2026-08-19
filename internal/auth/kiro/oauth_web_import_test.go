package kiro

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestOAuthWebImportAcceptsRawKiroIDETokenJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)

	authDir := t.TempDir()
	handler := NewOAuthWebHandler(&config.Config{AuthDir: authDir})
	router := gin.New()
	handler.RegisterRoutes(router)

	rawToken := `{
		"accessToken": "access-token",
		"refreshToken": "aorAAAAAGraw-token",
		"profileArn": "arn:aws:codewhisperer:us-west-2:123456789012:profile/abc",
		"expiresAt": "2027-01-02T03:04:05Z",
		"authMethod": "IdC",
		"provider": "Google",
		"clientId": "client-id",
		"clientSecret": "client-secret",
		"clientIdHash": "client-hash",
		"email": "user@example.com",
		"startUrl": "https://d-1234567890.awsapps.com/start",
		"region": "us-west-2"
	}`

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v0/oauth/kiro/import", strings.NewReader(rawToken))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to parse response: %v", err)
	}
	if body["success"] != true {
		t.Fatalf("success = %v, want true", body["success"])
	}
	if body["fileName"] != "kiro-idc-user-example-com.json" {
		t.Fatalf("fileName = %v", body["fileName"])
	}

	written, err := os.ReadFile(filepath.Join(authDir, "kiro-idc-user-example-com.json"))
	if err != nil {
		t.Fatalf("failed to read saved token: %v", err)
	}
	var saved KiroTokenStorage
	if err := json.Unmarshal(written, &saved); err != nil {
		t.Fatalf("failed to parse saved token: %v", err)
	}
	if saved.Type != "kiro" {
		t.Fatalf("type = %q, want kiro", saved.Type)
	}
	if saved.AccessToken != "access-token" || saved.RefreshToken != "aorAAAAAGraw-token" {
		t.Fatalf("saved token mismatch: access=%q refresh=%q", saved.AccessToken, saved.RefreshToken)
	}
	if saved.AuthMethod != "idc" {
		t.Fatalf("auth_method = %q, want idc", saved.AuthMethod)
	}
	if saved.ClientID != "client-id" || saved.ClientSecret != "client-secret" {
		t.Fatalf("client credentials not preserved: id=%q secret=%q", saved.ClientID, saved.ClientSecret)
	}
	if saved.Region != "us-west-2" || saved.StartURL != "https://d-1234567890.awsapps.com/start" {
		t.Fatalf("IDC metadata mismatch: region=%q startURL=%q", saved.Region, saved.StartURL)
	}
}

func TestOAuthWebImportLoadsIDCDeviceRegistrationFromClientIDHash(t *testing.T) {
	gin.SetMode(gin.TestMode)

	homeDir := t.TempDir()
	t.Setenv("HOME", homeDir)
	cacheDir := filepath.Join(homeDir, ".aws", "sso", "cache")
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		t.Fatalf("failed to create cache dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "client-hash.json"), []byte(`{"clientId":"loaded-client-id","clientSecret":"loaded-client-secret"}`), 0o600); err != nil {
		t.Fatalf("failed to write device registration: %v", err)
	}

	authDir := t.TempDir()
	handler := NewOAuthWebHandler(&config.Config{AuthDir: authDir})
	router := gin.New()
	handler.RegisterRoutes(router)

	rawToken := `{
		"accessToken": "access-token",
		"refreshToken": "aorAAAAAGraw-token",
		"authMethod": "IdC",
		"clientIdHash": "client-hash",
		"email": "user@example.com",
		"startUrl": "https://d-1234567890.awsapps.com/start"
	}`

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v0/oauth/kiro/import", strings.NewReader(rawToken))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	written, err := os.ReadFile(filepath.Join(authDir, "kiro-idc-user-example-com.json"))
	if err != nil {
		t.Fatalf("failed to read saved token: %v", err)
	}
	var saved KiroTokenStorage
	if err := json.Unmarshal(written, &saved); err != nil {
		t.Fatalf("failed to parse saved token: %v", err)
	}
	if saved.ClientID != "loaded-client-id" || saved.ClientSecret != "loaded-client-secret" {
		t.Fatalf("loaded credentials not preserved: id=%q secret=%q", saved.ClientID, saved.ClientSecret)
	}
	if saved.ClientIDHash != "client-hash" {
		t.Fatalf("client_id_hash = %q, want client-hash", saved.ClientIDHash)
	}
}

func TestOAuthWebImportRejectsIDCClientIDHashWithoutDeviceRegistration(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Setenv("HOME", t.TempDir())
	handler := NewOAuthWebHandler(&config.Config{AuthDir: t.TempDir()})
	router := gin.New()
	handler.RegisterRoutes(router)

	rawToken := `{
		"accessToken": "access-token",
		"refreshToken": "aorAAAAAGraw-token",
		"authMethod": "IdC",
		"clientIdHash": "missing-client-hash",
		"email": "user@example.com"
	}`

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v0/oauth/kiro/import", strings.NewReader(rawToken))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "device registration") {
		t.Fatalf("body = %s, want device registration error", rec.Body.String())
	}
}

func TestOAuthWebImportAcceptsPastedKiroIDETokenJSONInRefreshTokenField(t *testing.T) {
	gin.SetMode(gin.TestMode)

	authDir := t.TempDir()
	handler := NewOAuthWebHandler(&config.Config{AuthDir: authDir})
	router := gin.New()
	handler.RegisterRoutes(router)

	rawToken := `{"accessToken":"access-token","refreshToken":"aorAAAAAGraw-token","authMethod":"social","provider":"Github","email":"user@example.com","region":"us-east-1"}`
	wrappedBody, err := json.Marshal(map[string]string{"refreshToken": rawToken})
	if err != nil {
		t.Fatalf("failed to marshal wrapped body: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v0/oauth/kiro/import", strings.NewReader(string(wrappedBody)))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(authDir, "kiro-social-user-example-com.json")); err != nil {
		t.Fatalf("expected pasted token JSON to be saved: %v", err)
	}
}

func TestOAuthWebImportReportsSaveFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)

	authDirFile := filepath.Join(t.TempDir(), "auth-dir-file")
	if err := os.WriteFile(authDirFile, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("failed to create auth dir file: %v", err)
	}
	handler := NewOAuthWebHandler(&config.Config{AuthDir: authDirFile})
	callbackCalled := false
	handler.SetTokenCallback(func(*KiroTokenData) {
		callbackCalled = true
	})
	router := gin.New()
	handler.RegisterRoutes(router)

	rawToken := `{"accessToken":"access-token","refreshToken":"aorAAAAAGraw-token","authMethod":"social","provider":"Github","email":"user@example.com","region":"us-east-1"}`

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v0/oauth/kiro/import", strings.NewReader(rawToken))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Failed to save token") {
		t.Fatalf("body = %s, want save failure error", rec.Body.String())
	}
	if callbackCalled {
		t.Fatal("token callback was called despite save failure")
	}
}

func TestOAuthWebImportRejectsInvalidBareRefreshToken(t *testing.T) {
	gin.SetMode(gin.TestMode)

	handler := NewOAuthWebHandler(&config.Config{AuthDir: t.TempDir()})
	router := gin.New()
	handler.RegisterRoutes(router)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v0/oauth/kiro/import", strings.NewReader(`{"refreshToken":"not-a-token"}`))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body = %s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Invalid token format") {
		t.Fatalf("body = %s, want invalid token format error", rec.Body.String())
	}
}
