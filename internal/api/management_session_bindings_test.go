package api

import (
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
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

func TestManagementAuthFileSessionBindings_ReturnsCountsForStickySelector(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "test-management-password")

	gin.SetMode(gin.TestMode)

	tmpDir := t.TempDir()
	authDir := filepath.Join(tmpDir, "auth")
	if err := os.MkdirAll(authDir, 0o700); err != nil {
		t.Fatalf("failed to create auth dir: %v", err)
	}

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

	sticky := &coreauth.StickySelector{}
	authManager := coreauth.NewManager(nil, sticky, nil)
	accessManager := sdkaccess.NewManager()
	server := NewServer(cfg, authManager, accessManager, filepath.Join(tmpDir, "config.yaml"))

	// Seed two session bindings for the same auth entry.
	auths := []*coreauth.Auth{{ID: "auth-1", Provider: "codex", Status: coreauth.StatusActive}}
	headers1 := make(http.Header)
	headers1.Set("session_id", "s1")
	headers2 := make(http.Header)
	headers2.Set("session_id", "s2")
	opts1 := cliproxyexecutor.Options{Headers: headers1, OriginalRequest: []byte(`{}`)}
	opts2 := cliproxyexecutor.Options{Headers: headers2, OriginalRequest: []byte(`{}`)}
	_, _ = sticky.Pick(nil, "codex", "gpt-test", opts1, auths)
	_, _ = sticky.Pick(nil, "codex", "gpt-test", opts2, auths)

	req := httptest.NewRequest(http.MethodGet, "/v0/management/auth-files/session-bindings", nil)
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

	bindings, ok := payload["bindings"].([]any)
	if !ok {
		t.Fatalf("expected bindings array, got: %#v", payload["bindings"])
	}
	if len(bindings) != 1 {
		t.Fatalf("expected 1 auth entry in bindings, got %d: %#v", len(bindings), bindings)
	}
	first, ok := bindings[0].(map[string]any)
	if !ok {
		t.Fatalf("expected bindings[0] to be object, got %#v", bindings[0])
	}
	if first["auth_id"] != "auth-1" {
		t.Fatalf("expected auth_id=auth-1, got %#v", first["auth_id"])
	}
	if first["session_count"] != float64(2) {
		t.Fatalf("expected session_count=2, got %#v", first["session_count"])
	}
}
