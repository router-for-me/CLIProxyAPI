package cline

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func newTestClineAuth(workosURL, apiURL string) *ClineAuth {
	a := NewClineAuth(&config.Config{})
	a.workosBaseURL = workosURL
	a.apiBaseURL = apiURL
	a.minPollInterval = time.Millisecond
	return a
}

func TestStartDeviceFlowPostsClientIDAndParsesResponse(t *testing.T) {
	var gotContentType string
	var gotForm url.Values
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotContentType = r.Header.Get("Content-Type")
		body, _ := io.ReadAll(r.Body)
		gotForm, _ = url.ParseQuery(string(body))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"device_code":"dc-123","user_code":"UC-42","verification_uri":"https://workos.example/device","verification_uri_complete":"https://workos.example/device?uc=UC-42","expires_in":300,"interval":5}`))
	}))
	defer server.Close()

	auth := newTestClineAuth(server.URL, "https://unused.example")
	device, err := auth.StartDeviceFlow(context.Background())
	if err != nil {
		t.Fatalf("StartDeviceFlow() error = %v", err)
	}

	if gotPath != "/user_management/authorize/device" {
		t.Fatalf("path = %q, want /user_management/authorize/device", gotPath)
	}
	if !strings.Contains(gotContentType, "application/x-www-form-urlencoded") {
		t.Fatalf("Content-Type = %q, want form-urlencoded", gotContentType)
	}
	if got := gotForm.Get("client_id"); got != WorkOSClientID {
		t.Fatalf("client_id = %q, want %q", got, WorkOSClientID)
	}
	if device.DeviceCode != "dc-123" || device.UserCode != "UC-42" || device.VerificationURIComplete == "" {
		t.Fatalf("unexpected device response: %+v", device)
	}
	if device.ExpiresIn != 300 || device.Interval != 5 {
		t.Fatalf("expires_in/interval = %d/%d, want 300/5", device.ExpiresIn, device.Interval)
	}
}

func TestPollDeviceFlowPollsUntilSuccess(t *testing.T) {
	var polls atomic.Int32
	var lastForm url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		lastForm, _ = url.ParseQuery(string(body))
		w.Header().Set("Content-Type", "application/json")
		if polls.Add(1) < 3 {
			_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
			return
		}
		_, _ = w.Write([]byte(`{"access_token":"wos-access","refresh_token":"wos-refresh","token_type":"Bearer"}`))
	}))
	defer server.Close()

	auth := newTestClineAuth(server.URL, "https://unused.example")
	device := &DeviceCodeResponse{DeviceCode: "dc-123", UserCode: "UC-42", ExpiresIn: 300, Interval: 2}

	start := time.Now()
	tokens, err := auth.PollDeviceFlow(context.Background(), device)
	if err != nil {
		t.Fatalf("PollDeviceFlow() error = %v", err)
	}

	if tokens.AccessToken != "wos-access" || tokens.RefreshToken != "wos-refresh" || tokens.TokenType != "Bearer" {
		t.Fatalf("unexpected tokens: %+v", tokens)
	}
	if got := polls.Load(); got != 3 {
		t.Fatalf("poll count = %d, want 3", got)
	}
	if got := lastForm.Get("grant_type"); got != DeviceCodeGrantType {
		t.Fatalf("grant_type = %q, want %q", got, DeviceCodeGrantType)
	}
	if got := lastForm.Get("device_code"); got != "dc-123" {
		t.Fatalf("device_code = %q, want dc-123", got)
	}
	if got := lastForm.Get("client_id"); got != WorkOSClientID {
		t.Fatalf("client_id = %q, want %q", got, WorkOSClientID)
	}
	// The advertised interval is a floor: two failed attempts must each wait at
	// least device.Interval seconds, so three polls cannot finish faster.
	if elapsed := time.Since(start); elapsed < 2*time.Second {
		t.Fatalf("3 polls completed in %v, want at least 2s of interval waits", elapsed)
	}
}

func TestPollDeviceFlowErrorsOnExpiryAndDenials(t *testing.T) {
	t.Run("expired device code", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"error":"authorization_pending"}`))
		}))
		defer server.Close()

		auth := newTestClineAuth(server.URL, "https://unused.example")
		device := &DeviceCodeResponse{DeviceCode: "dc-123", ExpiresIn: 1, Interval: 1}
		_, err := auth.PollDeviceFlow(context.Background(), device)
		if err == nil || !strings.Contains(err.Error(), "expired") {
			t.Fatalf("PollDeviceFlow() error = %v, want device code expired", err)
		}
	})

	t.Run("access denied", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"error":"access_denied"}`))
		}))
		defer server.Close()

		auth := newTestClineAuth(server.URL, "https://unused.example")
		device := &DeviceCodeResponse{DeviceCode: "dc-123", ExpiresIn: 300, Interval: 1}
		_, err := auth.PollDeviceFlow(context.Background(), device)
		if err == nil || !strings.Contains(err.Error(), "denied") {
			t.Fatalf("PollDeviceFlow() error = %v, want access denied", err)
		}
	})
}

func TestRegisterClineTokenExchangesWorkOSTokens(t *testing.T) {
	var gotPath, gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"success":true,"data":{"accessToken":"cline-access","refreshToken":"cline-refresh","tokenType":"Bearer","expiresAt":"2030-01-02T03:04:05Z","userInfo":{"subject":"sub-1","email":"user@example.com","name":"Test User","clineUserId":"cu-7"}}}`)
	}))
	defer server.Close()

	auth := newTestClineAuth("https://unused.example", server.URL+"/api/v1")
	record, err := auth.RegisterClineToken(context.Background(), "wos-access", "wos-refresh")
	if err != nil {
		t.Fatalf("RegisterClineToken() error = %v", err)
	}

	if gotPath != "/api/v1/auth/register" {
		t.Fatalf("path = %q, want /api/v1/auth/register", gotPath)
	}
	for _, fragment := range []string{`"accessToken":"wos-access"`, `"refreshToken":"wos-refresh"`} {
		if !strings.Contains(gotBody, fragment) {
			t.Fatalf("request body missing %s: %s", fragment, gotBody)
		}
	}
	if record.AccessToken != "cline-access" || record.RefreshToken != "cline-refresh" {
		t.Fatalf("unexpected record: %+v", record)
	}
	if record.ExpiresAt != "2030-01-02T03:04:05Z" {
		t.Fatalf("expiresAt = %q", record.ExpiresAt)
	}
	if record.UserInfo == nil || record.UserInfo.Email != "user@example.com" || record.UserInfo.ClineUserID != "cu-7" {
		t.Fatalf("unexpected userInfo: %+v", record.UserInfo)
	}

	storage := auth.CreateTokenStorage(&ClineAuthBundle{TokenRecord: record, LastRefresh: "2030-01-01T00:00:00Z"})
	if storage == nil {
		t.Fatal("CreateTokenStorage() returned nil")
	}
	if storage.Type != "cline" || storage.AccessToken != "cline-access" || storage.RefreshToken != "cline-refresh" {
		t.Fatalf("unexpected storage: %+v", storage)
	}
	if storage.Email != "user@example.com" || storage.BaseURL != DefaultAPIBaseURL || storage.Expired != "2030-01-02T03:04:05Z" {
		t.Fatalf("unexpected storage metadata: %+v", storage)
	}
	if got := CredentialFileName(storage.Email, storage.Subject); got != "cline-user@example.com.json" {
		t.Fatalf("CredentialFileName() = %q", got)
	}
}

func TestRefreshClineTokenRotatesTokens(t *testing.T) {
	var gotPath, gotBody string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		gotBody = string(body)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"success":true,"data":{"accessToken":"rotated-access","refreshToken":"rotated-refresh","tokenType":"Bearer","expiresAt":"2030-05-06T07:08:09Z"}}`)
	}))
	defer server.Close()

	auth := newTestClineAuth("https://unused.example", server.URL+"/api/v1")
	record, err := auth.RefreshClineToken(context.Background(), "old-refresh")
	if err != nil {
		t.Fatalf("RefreshClineToken() error = %v", err)
	}

	if gotPath != "/api/v1/auth/refresh" {
		t.Fatalf("path = %q, want /api/v1/auth/refresh", gotPath)
	}
	for _, fragment := range []string{`"refreshToken":"old-refresh"`, `"grantType":"refresh_token"`} {
		if !strings.Contains(gotBody, fragment) {
			t.Fatalf("request body missing %s: %s", fragment, gotBody)
		}
	}
	if record.AccessToken != "rotated-access" || record.RefreshToken != "rotated-refresh" {
		t.Fatalf("rotation not returned: %+v", record)
	}
}

func TestClineTokenStorageSaveAndNeedsRefresh(t *testing.T) {
	dir := t.TempDir()
	storage := &ClineTokenStorage{
		Type:         "cline",
		AccessToken:  "cline-access",
		RefreshToken: "cline-refresh",
		Expired:      time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		Email:        "user@example.com",
	}
	path := filepath.Join(dir, "cline-user@example.com.json")
	if err := storage.SaveTokenToFile(path); err != nil {
		t.Fatalf("SaveTokenToFile() error = %v", err)
	}
	raw, errRead := os.ReadFile(path)
	if errRead != nil {
		t.Fatalf("read token file: %v", errRead)
	}

	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("token file is not valid JSON: %v\n%s", err, raw)
	}
	want := map[string]string{
		"type":          "cline",
		"access_token":  "cline-access",
		"refresh_token": "cline-refresh",
		"email":         "user@example.com",
		"base_url":      DefaultAPIBaseURL,
	}
	for key, value := range want {
		if got, _ := decoded[key].(string); got != value {
			t.Fatalf("token file %s = %q, want %q", key, got, value)
		}
	}

	if storage.NeedsRefresh() {
		t.Fatal("NeedsRefresh() = true for a fresh token")
	}
	storage.Expired = time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)
	if !storage.NeedsRefresh() {
		t.Fatal("NeedsRefresh() = false for an expired token")
	}
}
