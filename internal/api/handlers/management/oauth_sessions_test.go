package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestOAuthSessionStoreCompleteKeepsShortLivedSession(t *testing.T) {
	store := newOAuthSessionStore(time.Minute)
	store.Register("completed-state", "codex")

	store.Complete("completed-state")

	if _, ok := store.Get("completed-state"); !ok {
		t.Fatal("completed OAuth session was deleted instead of retained as a tombstone")
	}
	if store.IsPending("completed-state", "codex") {
		t.Fatal("completed OAuth session remained pending")
	}
}

func TestGetAuthStatusRejectsUnknownStateAndAcceptsCompletedState(t *testing.T) {
	store := newOAuthSessionStore(time.Minute)
	replaceOAuthSessionStoreForTest(t, store)

	handler := &Handler{}
	router := gin.New()
	router.GET("/status", handler.GetAuthStatus)

	unknown := performOAuthStatusRequest(t, router, "unknown-state")
	if unknown.Status != "error" || unknown.Error != "unknown or expired state" {
		t.Fatalf("unknown state response = %#v, want unknown/expired error", unknown)
	}

	store.Register("completed-state", "codex")
	store.Complete("completed-state")
	completed := performOAuthStatusRequest(t, router, "completed-state")
	if completed.Status != "ok" || completed.Error != "" {
		t.Fatalf("completed state response = %#v, want success", completed)
	}
}

func TestOAuthCallbackRejectsCompletedSession(t *testing.T) {
	store := newOAuthSessionStore(time.Minute)
	replaceOAuthSessionStoreForTest(t, store)
	store.Register("completed-state", "codex")
	store.Complete("completed-state")

	handler := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	router := gin.New()
	router.POST("/oauth-callback", handler.PostOAuthCallback)

	req := httptest.NewRequest(
		http.MethodPost,
		"/oauth-callback",
		strings.NewReader(`{"provider":"codex","state":"completed-state","code":"test-code"}`),
	)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusConflict {
		t.Fatalf("completed callback status = %d, want %d; body=%s", w.Code, http.StatusConflict, w.Body.String())
	}
}

type oauthStatusResponse struct {
	Status string `json:"status"`
	Error  string `json:"error"`
}

func performOAuthStatusRequest(t *testing.T, router http.Handler, state string) oauthStatusResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/status?state="+state, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status request returned %d, want %d; body=%s", w.Code, http.StatusOK, w.Body.String())
	}
	var response oauthStatusResponse
	if errDecode := json.Unmarshal(w.Body.Bytes(), &response); errDecode != nil {
		t.Fatalf("decode status response: %v", errDecode)
	}
	return response
}

func replaceOAuthSessionStoreForTest(t *testing.T, store *oauthSessionStore) {
	t.Helper()
	original := oauthSessions
	oauthSessions = store
	t.Cleanup(func() {
		oauthSessions = original
	})
}
