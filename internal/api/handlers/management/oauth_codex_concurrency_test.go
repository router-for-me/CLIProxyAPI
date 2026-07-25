package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/codex"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
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
			AccountID:    "account-" + code,
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

func TestRequestCodexTokenRuntimeSyncIncludesTokens(t *testing.T) {
	originalNewCodexOAuthService := newCodexOAuthService
	newCodexOAuthService = func(cfg *config.Config) codexOAuthService {
		return &fakeCodexOAuthService{}
	}
	defer func() {
		newCodexOAuthService = originalNewCodexOAuthService
	}()

	authDir := filepath.Join(t.TempDir(), "auths")
	handler := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)
	persisted := make(chan *coreauth.Auth, 1)
	handler.SetPostAuthPersistHook(func(_ context.Context, auth *coreauth.Auth) error {
		persisted <- auth
		return nil
	})
	router := gin.New()
	router.GET("/codex-auth-url", handler.RequestCodexToken)

	state := requestCodexTokenState(t, router)
	defer CompleteOAuthSession(state)
	if _, errWrite := WriteOAuthCallbackFileForPendingSession(authDir, "codex", state, "runtime-sync", ""); errWrite != nil {
		t.Fatalf("write codex callback file: %v", errWrite)
	}

	waitForOAuthSessionDone(t, state)
	var synced *coreauth.Auth
	select {
	case synced = <-persisted:
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for persisted auth sync")
	}

	wantMetadata := map[string]string{
		"type":          "codex",
		"id_token":      "invalid-test-id-token",
		"access_token":  "access-runtime-sync",
		"refresh_token": "refresh-runtime-sync",
		"account_id":    "account-runtime-sync",
		"email":         "codex-runtime-sync@example.test",
	}
	for key, want := range wantMetadata {
		if got, _ := synced.Metadata[key].(string); got != want {
			t.Fatalf("expected metadata %s %q, got %q", key, want, got)
		}
	}
	if got, _ := synced.Metadata["expired"].(string); got == "" {
		t.Fatal("expected runtime auth metadata to include expiry")
	}
	if got, _ := synced.Metadata["last_refresh"].(string); got == "" {
		t.Fatal("expected runtime auth metadata to include last refresh time")
	}
	if got := tokenValueForAuth(synced); got != "access-runtime-sync" {
		t.Fatalf("expected API call token %q, got %q", "access-runtime-sync", got)
	}
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
