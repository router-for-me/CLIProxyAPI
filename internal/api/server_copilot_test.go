package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	managementHandlers "github.com/router-for-me/CLIProxyAPI/v6/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	sdkaccess "github.com/router-for-me/CLIProxyAPI/v6/sdk/access"
)

func TestCopilotCallback_WritesStateFile(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	tmpDir := t.TempDir()
	cfg := &config.Config{Port: 53355, AuthDir: tmpDir}
	srv := NewServer(cfg, nil, &sdkaccess.Manager{}, filepath.Join(tmpDir, "config.yaml"))

	// Fire callback
	req := httptest.NewRequest(http.MethodGet, "/copilot/callback?code=abc&state=xyz&error=", nil)
	w := httptest.NewRecorder()
	srv.engine.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", w.Code)
	}

	// Verify state file created
	stateFile := filepath.Join(tmpDir, ".oauth-copilot-xyz.oauth")
	if _, err := os.Stat(stateFile); err != nil {
		t.Fatalf("expected state file created, got error: %v", err)
	}
}

func TestManagement_CopilotAuthURL_NotImplemented(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	h := &managementHandlers.Handler{}
	r := gin.New()
	r.GET("/v0/management/copilot-auth-url", h.RequestCopilotToken)

	req := httptest.NewRequest(http.MethodGet, "/v0/management/copilot-auth-url", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusNotImplemented {
		t.Fatalf("expected 501 Not Implemented, got %d", w.Code)
	}

	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if body["status"] != "error" {
		t.Fatalf("expected status=error, got %v", body["status"])
	}
}
