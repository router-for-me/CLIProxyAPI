package management

import (
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

func TestPostOAuthCallback_WriteFailureMarksSessionError(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	blockedAuthDir := filepath.Join(t.TempDir(), "blocked-auth-dir")
	if err := os.WriteFile(blockedAuthDir, []byte("blocked"), 0o600); err != nil {
		t.Fatalf("failed to create blocking auth path: %v", err)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: blockedAuthDir}, coreauth.NewManager(nil, nil, nil))
	state := "codex-session-write-failure"
	RegisterOAuthSession(state, "codex")

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/v0/management/oauth/callback", strings.NewReader(`{"provider":"codex","state":"`+state+`","code":"auth-code"}`))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req

	h.PostOAuthCallback(ctx)

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusInternalServerError, recorder.Code, recorder.Body.String())
	}
	if IsOAuthSessionPending(state, "codex") {
		t.Fatal("expected failed callback write to end pending oauth session")
	}
	provider, status, ok := GetOAuthSession(state)
	if !ok {
		t.Fatal("expected oauth session to remain available with error status")
	}
	if provider != "codex" {
		t.Fatalf("expected provider codex, got %q", provider)
	}
	if strings.TrimSpace(status) == "" {
		t.Fatal("expected oauth session error status to be recorded")
	}
}
