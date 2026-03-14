package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestPostOAuthCallback_UsesSessionAuthDir(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	tempDir := t.TempDir()
	configAuthDir := filepath.Join(tempDir, "config-auth")
	sessionAuthDir := filepath.Join(tempDir, "session-auth")
	if err := os.MkdirAll(configAuthDir, 0o700); err != nil {
		t.Fatalf("failed to create config auth dir: %v", err)
	}
	if err := os.MkdirAll(sessionAuthDir, 0o700); err != nil {
		t.Fatalf("failed to create session auth dir: %v", err)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: configAuthDir}, coreauth.NewManager(nil, nil, nil))
	state := "codex-session-bound-state"
	RegisterOAuthSession(state, "codex", sessionAuthDir)

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/v0/management/oauth/callback", strings.NewReader(`{"provider":"codex","state":"`+state+`","code":"auth-code"}`))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req

	h.PostOAuthCallback(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, recorder.Code, recorder.Body.String())
	}

	sessionPath := filepath.Join(sessionAuthDir, ".oauth-codex-"+state+".oauth")
	configPath := filepath.Join(configAuthDir, ".oauth-codex-"+state+".oauth")
	if _, err := os.Stat(sessionPath); err != nil {
		t.Fatalf("expected callback file in session auth dir, stat err: %v", err)
	}
	if _, err := os.Stat(configPath); !os.IsNotExist(err) {
		t.Fatalf("expected no callback file in config auth dir, stat err: %v", err)
	}

	var payload map[string]string
	raw, err := os.ReadFile(sessionPath)
	if err != nil {
		t.Fatalf("failed to read callback file: %v", err)
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("failed to decode callback file: %v", err)
	}
	if payload["code"] != "auth-code" {
		t.Fatalf("expected code to be persisted, got %q", payload["code"])
	}
}
