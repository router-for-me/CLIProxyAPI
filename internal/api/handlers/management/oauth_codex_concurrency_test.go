package management

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

type fakeCodexOAuthService struct{}

func (f *fakeCodexOAuthService) GenerateAuthURL(state string, pkceCodes *codex.PKCECodes) (string, error) {
	return "https://auth.example.test/oauth?state=" + state, nil
}

func (f *fakeCodexOAuthService) ExchangeCodeForTokens(ctx context.Context, code string, pkceCodes *codex.PKCECodes) (*codex.CodexAuthBundle, error) {
	now := time.Now()
	return &codex.CodexAuthBundle{
		TokenData: codex.CodexTokenData{
			IDToken:      "invalid-test-id-token",
			AccessToken:  "access-" + code,
			RefreshToken: "refresh-" + code,
			Email:        "codex-" + code + "@example.test",
			Expire:       now.Add(time.Hour).Format(time.RFC3339),
		},
		LastRefresh: now.Format(time.RFC3339),
	}, nil
}

func (f *fakeCodexOAuthService) CreateTokenStorage(bundle *codex.CodexAuthBundle) *codex.CodexTokenStorage {
	return &codex.CodexTokenStorage{
		IDToken:      bundle.TokenData.IDToken,
		AccessToken:  bundle.TokenData.AccessToken,
		RefreshToken: bundle.TokenData.RefreshToken,
		AccountID:    bundle.TokenData.AccountID,
		LastRefresh:  bundle.LastRefresh,
		Email:        bundle.TokenData.Email,
		Expire:       bundle.TokenData.Expire,
	}
}

func TestRequestCodexTokenCompletionKeepsConcurrentSessionPending(t *testing.T) {
	originalNewCodexOAuthService := newCodexOAuthService
	newCodexOAuthService = func(cfg *config.Config) codexOAuthService {
		return &fakeCodexOAuthService{}
	}
	defer func() {
		newCodexOAuthService = originalNewCodexOAuthService
	}()

	authDir := filepath.Join(t.TempDir(), "auths")
	handler := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)
	router := gin.New()
	router.GET("/codex-auth-url", handler.RequestCodexToken)

	firstState := requestCodexTokenState(t, router)
	secondState := requestCodexTokenState(t, router)
	defer CompleteOAuthSession(firstState)
	defer CompleteOAuthSession(secondState)

	if _, errWrite := WriteOAuthCallbackFileForPendingSession(authDir, "codex", firstState, "first-code", ""); errWrite != nil {
		t.Fatalf("write first callback file: %v", errWrite)
	}

	waitForOAuthSessionDone(t, firstState)
	if !IsOAuthSessionPending(secondState, "codex") {
		t.Fatalf("expected concurrent codex session %s to remain pending after %s completed", secondState, firstState)
	}
}

func TestRequestCodexTokenWebUIAllowsManualCallbackWhenForwarderIsUnavailable(t *testing.T) {
	originalNewCodexOAuthService := newCodexOAuthService
	originalStartWebUICallbackForwarder := startWebUICallbackForwarder
	newCodexOAuthService = func(cfg *config.Config) codexOAuthService {
		return &fakeCodexOAuthService{}
	}
	startWebUICallbackForwarder = func(int, string, string) (*callbackForwarder, error) {
		return nil, errors.New("listen tcp 0.0.0.0:1455: bind: address already in use")
	}
	defer func() {
		newCodexOAuthService = originalNewCodexOAuthService
		startWebUICallbackForwarder = originalStartWebUICallbackForwarder
	}()

	authDir := filepath.Join(t.TempDir(), "auths")
	handler := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir, Port: 18317}, nil)
	router := gin.New()
	router.GET("/codex-auth-url", handler.RequestCodexToken)

	req := httptest.NewRequest(http.MethodGet, "/codex-auth-url?is_webui=true", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, w.Code, w.Body.String())
	}

	var payload struct {
		State string `json:"state"`
		URL   string `json:"url"`
	}
	if errDecode := json.Unmarshal(w.Body.Bytes(), &payload); errDecode != nil {
		t.Fatalf("decode codex auth URL response: %v", errDecode)
	}
	if payload.State == "" || payload.URL == "" {
		t.Fatalf("expected OAuth URL and state, got %+v", payload)
	}
	CompleteOAuthSession(payload.State)
}

func requestCodexTokenState(t *testing.T, router http.Handler) string {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, "/codex-auth-url", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, w.Code, w.Body.String())
	}

	var payload struct {
		State string `json:"state"`
	}
	if errDecode := json.Unmarshal(w.Body.Bytes(), &payload); errDecode != nil {
		t.Fatalf("decode codex auth URL response: %v", errDecode)
	}
	if payload.State == "" {
		t.Fatalf("expected codex auth URL response to include state")
	}
	return payload.State
}

func waitForOAuthSessionDone(t *testing.T, state string) {
	t.Helper()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if !IsOAuthSessionPending(state, "codex") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for codex session %s to complete", state)
}
