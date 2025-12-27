package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	gin "github.com/gin-gonic/gin"
	proxyconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v6/sdk/access"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func TestManagementRefreshCodexQuota(t *testing.T) {
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
		if r.URL.Path != "/backend-api/codex/responses" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-access-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("x-codex-primary-used-percent", "12.5")
		w.Header().Set("x-codex-primary-reset-after-seconds", "123")
		w.Header().Set("x-codex-plan-type", "team")
		w.WriteHeader(http.StatusOK)
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
		ID:       "codex-1",
		Provider: "codex",
		Attributes: map[string]string{
			"path":     "does-not-exist.json",
			"base_url": upstream.URL + "/backend-api/codex",
		},
		Metadata: map[string]any{
			"access_token": "test-access-token",
		},
	})

	reqBody := []byte(`{"id":"codex-1","model":"gpt-5.2"}`)
	req := httptest.NewRequest(http.MethodPost, "/v0/management/auth-files/codex-quota", bytes.NewReader(reqBody))
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
	if authObj["codex_quota"] == nil {
		t.Fatalf("expected codex_quota in response auth, got: %#v", authObj)
	}
}

