package codebuddy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestManagerGetHeaders(t *testing.T) {
	path := writeTestSession(t, map[string]any{
		"accessToken":  "access-token",
		"refreshToken": "refresh-token",
		"expiresAt":    time.Now().Add(time.Hour).UnixMilli(),
		"domain":       "tenant.example.com",
	})

	manager, err := NewCredentialManager(path)
	if err != nil {
		t.Fatalf("NewCredentialManager() error = %v", err)
	}
	headers, err := manager.GetHeaders(context.Background(), nil)
	if err != nil {
		t.Fatalf("GetHeaders() error = %v", err)
	}

	want := map[string]string{
		"Authorization":   "Bearer access-token",
		"X-User-Id":       "user-1",
		"X-Enterprise-Id": "enterprise-1",
		"X-Tenant-Id":     "enterprise-1",
		"X-Domain":        "tenant.example.com",
		"User-Agent":      UserAgent,
	}
	for key, value := range want {
		if got := headers.Get(key); got != value {
			t.Errorf("header %s = %q, want %q", key, got, value)
		}
	}
	if got := headers.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
}

func TestFindAuthFile(t *testing.T) {
	dir := t.TempDir()
	writeTestSessionAt(t, filepath.Join(dir, "z-account.info"), map[string]any{
		"accessToken": "z",
	})
	writeTestSessionAt(t, filepath.Join(dir, "a-account.info"), map[string]any{
		"accessToken": "a",
	})
	if err := os.WriteFile(filepath.Join(dir, "ignored.json"), []byte("{}"), 0o600); err != nil {
		t.Fatalf("write ignored file: %v", err)
	}

	got, err := FindAuthFile(dir, "")
	if err != nil {
		t.Fatalf("FindAuthFile() error = %v", err)
	}
	if want := filepath.Join(dir, "a-account.info"); got != want {
		t.Fatalf("FindAuthFile() = %q, want %q", got, want)
	}

	exact := filepath.Join(dir, "z-account.info")
	got, err = FindAuthFile(dir, exact)
	if err != nil {
		t.Fatalf("FindAuthFile(exact) error = %v", err)
	}
	if got != exact {
		t.Fatalf("FindAuthFile(exact) = %q, want %q", got, exact)
	}
}

func TestManagerRefreshesAndWritesSession(t *testing.T) {
	path := writeTestSession(t, map[string]any{
		"accessToken":  "old-access",
		"refreshToken": "old-refresh",
		"expiresAt":    time.Now().Add(-time.Minute).UnixMilli(),
		"domain":       "tenant.example.com",
	})

	var refreshRequestHeaders http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/plugin/auth/token/refresh" {
			t.Errorf("refresh path = %q", r.URL.Path)
		}
		refreshRequestHeaders = r.Header.Clone()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"data":{"accessToken":"new-access","refreshToken":"new-refresh","expiresAt":4102444800000,"domain":"refreshed.example.com"}}`))
	}))
	defer server.Close()

	manager, err := NewCredentialManagerWithRefreshURL(path, server.URL+"/v2/plugin/auth/token/refresh")
	if err != nil {
		t.Fatalf("NewCredentialManagerWithRefreshURL() error = %v", err)
	}
	headers, err := manager.GetHeaders(context.Background(), server.Client())
	if err != nil {
		t.Fatalf("GetHeaders() error = %v", err)
	}
	if got := headers.Get("Authorization"); got != "Bearer new-access" {
		t.Fatalf("Authorization = %q, want refreshed token", got)
	}
	if got := headers.Get("X-Domain"); got != "refreshed.example.com" {
		t.Fatalf("X-Domain = %q, want refreshed domain", got)
	}
	if got := refreshRequestHeaders.Get("X-Refresh-Token"); got != "old-refresh" {
		t.Fatalf("X-Refresh-Token = %q, want old-refresh", got)
	}
	if got := refreshRequestHeaders.Get("X-Auth-Refresh-Source"); got != "plugin" {
		t.Fatalf("X-Auth-Refresh-Source = %q, want plugin", got)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read refreshed auth file: %v", err)
	}
	var session map[string]any
	if err := json.Unmarshal(raw, &session); err != nil {
		t.Fatalf("decode refreshed auth file: %v", err)
	}
	auth, ok := session["auth"].(map[string]any)
	if !ok {
		t.Fatalf("refreshed auth object missing: %s", raw)
	}
	if got, _ := auth["accessToken"].(string); got != "new-access" {
		t.Fatalf("written accessToken = %q, want new-access", got)
	}
}

func TestRefreshURLForBaseURL(t *testing.T) {
	tests := []struct {
		baseURL string
		want    string
	}{
		{baseURL: "https://copilot.tencent.com/v2", want: "https://copilot.tencent.com/v2/plugin/auth/token/refresh"},
		{baseURL: "https://gateway.example/api/v1/", want: "https://gateway.example/api/v1/plugin/auth/token/refresh"},
	}
	for _, tt := range tests {
		if got := RefreshURLForBaseURL(tt.baseURL); got != tt.want {
			t.Errorf("RefreshURLForBaseURL(%q) = %q, want %q", tt.baseURL, got, tt.want)
		}
	}
}

func writeTestSession(t *testing.T, auth map[string]any) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "account.info")
	writeTestSessionAt(t, path, auth)
	return path
}

func writeTestSessionAt(t *testing.T, path string, auth map[string]any) {
	t.Helper()
	session := map[string]any{
		"account": map[string]any{
			"uid":          "user-1",
			"enterpriseId": "enterprise-1",
			"nickname":     "Test User",
		},
		"auth": auth,
	}
	raw, err := json.Marshal(session)
	if err != nil {
		t.Fatalf("marshal test session: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write test session: %v", err)
	}
	if !strings.HasSuffix(path, ".info") {
		t.Fatalf("test session path must use .info: %s", path)
	}
}
