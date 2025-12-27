package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	gin "github.com/gin-gonic/gin"
	proxyconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v6/sdk/access"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func TestManagementRefreshAntigravityQuota(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "test-management-password")

	gin.SetMode(gin.TestMode)

	tmpDir := t.TempDir()
	authDir := filepath.Join(tmpDir, "auth")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatalf("failed to create auth dir: %v", err)
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		if r.URL.Path != "/v1internal:fetchAvailableModels" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-access-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":{"gemini-3-pro-high":{"quotaInfo":{"remainingFraction":0.73,"resetTime":"2025-01-01T00:00:00Z"}},"claude-sonnet-4-5":{"quotaInfo":{"remainingFraction":0.12,"resetTime":"2025-01-02T00:00:00Z"}}}}`))
	}))
	t.Cleanup(upstream.Close)

	cfg := &proxyconfig.Config{
		SDKConfig: sdkconfig.SDKConfig{
			APIKeys: []string{"test-key"},
		},
		Port:                   0,
		AuthDir:                authDir,
		Debug:                  true,
		LoggingToFile:          false,
		UsageStatisticsEnabled: false,
	}

	authManager := coreauth.NewManager(nil, nil, nil)
	accessManager := sdkaccess.NewManager()

	configPath := filepath.Join(tmpDir, "config.yaml")
	server := NewServer(cfg, authManager, accessManager, configPath)

	_, _ = authManager.Register(nil, &coreauth.Auth{
		ID:       "ag-1",
		Provider: "antigravity",
		Attributes: map[string]string{
			"path": "does-not-exist.json",
		},
		Metadata: map[string]any{
			"access_token": "test-access-token",
			"expired":      time.Now().Add(6 * time.Hour).Format(time.RFC3339),
			"base_url":     upstream.URL,
			"project_id":   "test-project",
		},
	})

	reqBody := []byte(`{"id":"ag-1"}`)
	req := httptest.NewRequest(http.MethodPost, "/v0/management/auth-files/antigravity-quota", bytes.NewReader(reqBody))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer test-management-password")

	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected status=200, got %d: %s", rr.Code, rr.Body.String())
	}

	var payload map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
		t.Fatalf("expected json response, got error: %v; body=%s", err, rr.Body.String())
	}
	authObj, ok := payload["auth"].(map[string]any)
	if !ok || authObj == nil {
		t.Fatalf("expected auth object, got: %#v", payload["auth"])
	}
	if authObj["antigravity_quota"] == nil {
		t.Fatalf("expected antigravity_quota in response auth, got: %#v", authObj)
	}
}

