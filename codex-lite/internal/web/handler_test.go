package web

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/codex-lite/internal/auth"
	"github.com/codex-lite/internal/manager"
	"github.com/gin-gonic/gin"
)

func TestHandleCallbackExpiredState(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tmpDir := t.TempDir()
	mgr := manager.NewManager(tmpDir)
	h := NewHandler(mgr, tmpDir, 1455)
	h.sessionTTL = 10 * time.Minute

	state := "expired-state"
	h.sessions[state] = &loginSession{
		State:     state,
		PKCE:      &auth.PKCECodes{CodeVerifier: "verifier"},
		CreatedAt: time.Now().Add(-11 * time.Minute),
	}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/auth/callback?code=dummy&state="+state, nil)

	h.HandleCallback(ctx)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}

	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to parse response body: %v", err)
	}
	if resp["error"] != "state expired" {
		t.Fatalf("expected error %q, got %q", "state expired", resp["error"])
	}

	h.sessionsMu.RLock()
	_, exists := h.sessions[state]
	h.sessionsMu.RUnlock()
	if exists {
		t.Fatalf("expected expired session to be deleted")
	}
}

func TestHandleCallbackValidState(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tmpDir := t.TempDir()
	mgr := manager.NewManager(tmpDir)
	h := NewHandler(mgr, tmpDir, 1455)
	h.sessionTTL = 10 * time.Minute
	h.exchangeCode = func(_ context.Context, _ string, _ *auth.PKCECodes) (*auth.TokenResponse, error) {
		return &auth.TokenResponse{
			AccessToken:  "access-token",
			RefreshToken: "refresh-token",
			IDToken:      "",
			ExpiresIn:    3600,
		}, nil
	}

	state := "valid-state"
	h.sessions[state] = &loginSession{
		State:     state,
		PKCE:      &auth.PKCECodes{CodeVerifier: "verifier"},
		CreatedAt: time.Now().Add(-1 * time.Minute),
	}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/auth/callback?code=dummy&state="+state, nil)

	h.HandleCallback(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	h.sessionsMu.RLock()
	_, exists := h.sessions[state]
	h.sessionsMu.RUnlock()
	if exists {
		t.Fatalf("expected valid session to be deleted after successful callback")
	}
}

func TestCleanupExpiredSessions(t *testing.T) {
	tmpDir := t.TempDir()
	mgr := manager.NewManager(tmpDir)
	h := NewHandler(mgr, tmpDir, 1455)
	h.sessionTTL = 10 * time.Minute

	now := time.Now()
	h.sessions["expired"] = &loginSession{CreatedAt: now.Add(-11 * time.Minute)}
	h.sessions["valid"] = &loginSession{CreatedAt: now.Add(-2 * time.Minute)}

	removed := h.cleanupExpiredSessions(now)
	if removed != 1 {
		t.Fatalf("expected removed count 1, got %d", removed)
	}

	h.sessionsMu.RLock()
	_, expiredExists := h.sessions["expired"]
	_, validExists := h.sessions["valid"]
	h.sessionsMu.RUnlock()

	if expiredExists {
		t.Fatalf("expected expired session to be removed")
	}
	if !validExists {
		t.Fatalf("expected valid session to remain")
	}
}
