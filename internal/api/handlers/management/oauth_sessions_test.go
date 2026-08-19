package management

import (
	"encoding/json"
	"errors"
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

func TestOAuthSessionStoreCompleteDoesNotExtendCompletedSession(t *testing.T) {
	store := newOAuthSessionStore(time.Minute)
	store.Register("completed-state", "codex")
	store.Complete("completed-state")
	before, ok := store.Get("completed-state")
	if !ok {
		t.Fatal("completed OAuth session tombstone is missing")
	}

	store.completedTTL = 2 * time.Minute
	store.Complete("completed-state")
	after, ok := store.Get("completed-state")
	if !ok {
		t.Fatal("completed OAuth session tombstone is missing after repeated completion")
	}
	if !after.ExpiresAt.Equal(before.ExpiresAt) {
		t.Fatalf("repeated completion extended expiry from %s to %s", before.ExpiresAt, after.ExpiresAt)
	}
}

func TestOAuthSessionStoreCompleteProviderSkipsCompletedSessions(t *testing.T) {
	store := newOAuthSessionStore(time.Minute)
	store.Register("completed-state", "codex")
	store.Register("pending-state", "codex")
	store.Complete("completed-state")
	completedBefore, ok := store.Get("completed-state")
	if !ok {
		t.Fatal("completed OAuth session tombstone is missing")
	}

	store.completedTTL = 2 * time.Minute
	if got := store.CompleteProvider("codex", oauthSessionSourceBuiltin); got != 1 {
		t.Fatalf("CompleteProvider() = %d, want 1 newly completed session", got)
	}
	completedAfter, ok := store.Get("completed-state")
	if !ok {
		t.Fatal("completed OAuth session tombstone is missing after provider completion")
	}
	if !completedAfter.ExpiresAt.Equal(completedBefore.ExpiresAt) {
		t.Fatalf("provider completion extended existing tombstone from %s to %s", completedBefore.ExpiresAt, completedAfter.ExpiresAt)
	}
	pendingAfter, ok := store.Get("pending-state")
	if !ok || !pendingAfter.Completed {
		t.Fatalf("pending session completed/ok = %t/%t, want true/true", pendingAfter.Completed, ok)
	}
}

func TestGetOAuthSessionHidesCompletedSession(t *testing.T) {
	store := newOAuthSessionStore(time.Minute)
	replaceOAuthSessionStoreForTest(t, store)
	store.Register("completed-state", "codex")
	store.Complete("completed-state")

	provider, status, ok := GetOAuthSession("completed-state")
	if ok {
		t.Fatalf("GetOAuthSession() = (%q, %q, true), want completed session hidden", provider, status)
	}

	_, _, _, _, completed, detailsOK := GetOAuthSessionDetails("completed-state")
	if !detailsOK || !completed {
		t.Fatalf("GetOAuthSessionDetails() completed/ok = %t/%t, want true/true", completed, detailsOK)
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

func TestOAuthSessionStoreCancelRemovesPendingSession(t *testing.T) {
	store := newOAuthSessionStore(time.Minute)
	store.Register("pending-state", "xai")

	if !store.Cancel("pending-state") {
		t.Fatal("Cancel() = false, want true for pending session")
	}
	if store.IsPending("pending-state", "xai") {
		t.Fatal("cancelled session remained pending")
	}
	if _, ok := store.Get("pending-state"); ok {
		t.Fatal("cancelled session still present in store")
	}
	if store.Cancel("pending-state") {
		t.Fatal("second Cancel() = true, want false")
	}
}

func TestOAuthSessionStoreCancelIgnoresCompletedAndUnknown(t *testing.T) {
	store := newOAuthSessionStore(time.Minute)
	store.Register("completed-state", "codex")
	store.Complete("completed-state")

	if store.Cancel("completed-state") {
		t.Fatal("Cancel() completed session = true, want false")
	}
	if _, ok := store.Get("completed-state"); !ok {
		t.Fatal("completed tombstone was removed by Cancel")
	}
	if store.Cancel("missing-state") {
		t.Fatal("Cancel() unknown session = true, want false")
	}
}

func TestOAuthSessionStoreCancelIgnoresErrorSession(t *testing.T) {
	store := newOAuthSessionStore(time.Minute)
	store.Register("error-state", "kimi")
	store.SetError("error-state", "Authentication failed")

	if store.IsPending("error-state", "kimi") {
		t.Fatal("error session should not be pending")
	}
	if store.Cancel("error-state") {
		t.Fatal("Cancel() error session = true, want false")
	}
}

func TestCancelOAuthSessionAndCallbackRejectAfterCancel(t *testing.T) {
	store := newOAuthSessionStore(time.Minute)
	replaceOAuthSessionStoreForTest(t, store)
	store.Register("callback-state", "anthropic")

	if !CancelOAuthSession("callback-state") {
		t.Fatal("CancelOAuthSession() = false, want true")
	}
	if IsOAuthSessionPending("callback-state", "anthropic") {
		t.Fatal("session still pending after cancel")
	}

	_, errWrite := WriteOAuthCallbackFileForPendingSession(t.TempDir(), "anthropic", "callback-state", "code", "")
	if errWrite == nil {
		t.Fatal("expected callback write to fail after cancel")
	}
	if !errors.Is(errWrite, errOAuthSessionNotPending) {
		t.Fatalf("callback write error = %v, want %v", errWrite, errOAuthSessionNotPending)
	}
}

func TestBeginOAuthSessionSaveCoversBuiltinAndPluginProviders(t *testing.T) {
	store := newOAuthSessionStore(time.Minute)
	replaceOAuthSessionStoreForTest(t, store)

	providers := []string{
		"anthropic",
		"codex",
		"gitlab",
		"antigravity",
		"xai",
		"qoder",
		"kimi",
		"iflow",
		"github-copilot",
		"kiro",
		"kilo",
		"cursor",
	}
	for _, provider := range providers {
		state := provider + "-begin-save"
		store.Register(state, provider)

		if errBegin := beginOAuthSessionSave(state, provider); errBegin != nil {
			t.Fatalf("%s begin save error = %v, want nil", provider, errBegin)
		}
		if CancelOAuthSession(state) {
			t.Fatalf("%s CancelOAuthSession() = true after save began, want false", provider)
		}
		if errBegin := beginOAuthSessionSave(state, provider); !errors.Is(errBegin, errOAuthSessionSaving) {
			t.Fatalf("%s repeated begin error = %v, want %v", provider, errBegin, errOAuthSessionSaving)
		}
	}

	const pluginState = "plugin-begin-save"
	if errRegister := store.RegisterPlugin(pluginState, "gemini-cli", nil); errRegister != nil {
		t.Fatalf("RegisterPlugin() error = %v", errRegister)
	}
	if errBegin := beginOAuthSessionSave(pluginState, "gemini-cli"); errBegin != nil {
		t.Fatalf("plugin begin save error = %v, want nil", errBegin)
	}
	if CancelOAuthSession(pluginState) {
		t.Fatal("plugin CancelOAuthSession() = true after save began, want false")
	}
}

func TestBeginOAuthSessionSaveRejectsCancelledCompletedErroredAndMismatchedSessions(t *testing.T) {
	store := newOAuthSessionStore(time.Minute)
	replaceOAuthSessionStoreForTest(t, store)

	store.Register("cancelled-save", "anthropic")
	if !CancelOAuthSession("cancelled-save") {
		t.Fatal("CancelOAuthSession() = false, want true")
	}
	if errBegin := beginOAuthSessionSave("cancelled-save", "anthropic"); !errors.Is(errBegin, errOAuthSessionNotPending) {
		t.Fatalf("cancelled begin error = %v, want %v", errBegin, errOAuthSessionNotPending)
	}

	store.Register("completed-save", "codex")
	store.Complete("completed-save")
	if errBegin := beginOAuthSessionSave("completed-save", "codex"); !errors.Is(errBegin, errOAuthSessionNotPending) {
		t.Fatalf("completed begin error = %v, want %v", errBegin, errOAuthSessionNotPending)
	}

	store.Register("error-save", "anthropic")
	store.SetError("error-save", "Authentication failed")
	if errBegin := beginOAuthSessionSave("error-save", "anthropic"); !errors.Is(errBegin, errOAuthSessionNotPending) {
		t.Fatalf("error begin error = %v, want %v", errBegin, errOAuthSessionNotPending)
	}

	store.Register("provider-mismatch", "codex")
	if errBegin := beginOAuthSessionSave("provider-mismatch", "anthropic"); !errors.Is(errBegin, errOAuthSessionNotPending) {
		t.Fatalf("provider mismatch begin error = %v, want %v", errBegin, errOAuthSessionNotPending)
	}
}

func TestOAuthSessionStoreKiroPresentationStatusesRemainCancellableAndSaveable(t *testing.T) {
	for _, status := range []string{
		"device_code|https://example.test/verify|ABCD-EFGH",
		"auth_url|https://example.test/oauth",
	} {
		cancelStore := newOAuthSessionStore(time.Minute)
		cancelStore.Register("kiro-cancel", "kiro")
		cancelStore.SetError("kiro-cancel", status)
		if !cancelStore.IsPending("kiro-cancel", "kiro") {
			t.Fatalf("Kiro status %q should remain pending", status)
		}
		if !cancelStore.Cancel("kiro-cancel") {
			t.Fatalf("Cancel() = false for Kiro status %q, want true", status)
		}

		saveStore := newOAuthSessionStore(time.Minute)
		saveStore.Register("kiro-save", "kiro")
		saveStore.SetError("kiro-save", status)
		if errBegin := saveStore.BeginSave("kiro-save", "kiro"); errBegin != nil {
			t.Fatalf("BeginSave() error = %v for Kiro status %q, want nil", errBegin, status)
		}
		if saveStore.Cancel("kiro-save") {
			t.Fatalf("Cancel() = true after Kiro save began for status %q, want false", status)
		}
	}
}

func TestOAuthSessionStoreCancelAndSaveHaveOneWinner(t *testing.T) {
	for iteration := 0; iteration < 100; iteration++ {
		store := newOAuthSessionStore(time.Minute)
		store.Register("racing-state", "xai")

		start := make(chan struct{})
		cancelled := make(chan bool, 1)
		saveBegan := make(chan bool, 1)
		go func() {
			<-start
			cancelled <- store.Cancel("racing-state")
		}()
		go func() {
			<-start
			saveBegan <- store.BeginSave("racing-state", "xai") == nil
		}()
		close(start)

		cancelWon := <-cancelled
		saveWon := <-saveBegan
		if cancelWon == saveWon {
			t.Fatalf("iteration %d cancel/save winners = %t/%t, want exactly one winner", iteration, cancelWon, saveWon)
		}
		if cancelWon && store.IsPending("racing-state", "xai") {
			t.Fatalf("iteration %d cancelled session remained pending", iteration)
		}
		if saveWon && store.Cancel("racing-state") {
			t.Fatalf("iteration %d cancellation succeeded after save won", iteration)
		}
	}
}

func TestCancelAuthSessionHandler(t *testing.T) {
	store := newOAuthSessionStore(time.Minute)
	replaceOAuthSessionStoreForTest(t, store)
	store.Register("device-state", "xai")

	handler := &Handler{}
	router := gin.New()
	router.DELETE("/oauth-session", handler.CancelAuthSession)

	missing := performOAuthCancelRequest(t, router, "")
	if missing.status != http.StatusBadRequest {
		t.Fatalf("missing state status = %d, want %d", missing.status, http.StatusBadRequest)
	}

	invalid := performOAuthCancelRequest(t, router, "bad/state")
	if invalid.status != http.StatusBadRequest {
		t.Fatalf("invalid state status = %d, want %d", invalid.status, http.StatusBadRequest)
	}

	cancelled := performOAuthCancelRequest(t, router, "device-state")
	if cancelled.status != http.StatusOK || !cancelled.cancelled || cancelled.bodyStatus != "ok" {
		t.Fatalf("cancel pending response = %#v, want ok/cancelled", cancelled)
	}
	if IsOAuthSessionPending("device-state", "xai") {
		t.Fatal("device session still pending after cancel API")
	}

	repeat := performOAuthCancelRequest(t, router, "device-state")
	if repeat.status != http.StatusOK || repeat.cancelled {
		t.Fatalf("repeat cancel response = %#v, want ok with cancelled=false", repeat)
	}

	// Status after cancel should not report success.
	statusRouter := gin.New()
	statusRouter.GET("/status", handler.GetAuthStatus)
	unknown := performOAuthStatusRequest(t, statusRouter, "device-state")
	if unknown.Status != "error" || unknown.Error != "unknown or expired state" {
		t.Fatalf("status after cancel = %#v, want unknown/expired error", unknown)
	}
}

type oauthCancelResponse struct {
	status     int
	bodyStatus string
	cancelled  bool
}

func performOAuthCancelRequest(t *testing.T, router http.Handler, state string) oauthCancelResponse {
	t.Helper()
	path := "/oauth-session"
	if state != "" {
		path += "?state=" + state
	}
	req := httptest.NewRequest(http.MethodDelete, path, nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	var body struct {
		Status    string `json:"status"`
		Cancelled bool   `json:"cancelled"`
		Error     string `json:"error"`
	}
	if w.Body.Len() > 0 {
		if errDecode := json.Unmarshal(w.Body.Bytes(), &body); errDecode != nil {
			t.Fatalf("decode cancel response: %v body=%s", errDecode, w.Body.String())
		}
	}
	return oauthCancelResponse{
		status:     w.Code,
		bodyStatus: body.Status,
		cancelled:  body.Cancelled,
	}
}

func replaceOAuthSessionStoreForTest(t *testing.T, store *oauthSessionStore) {
	t.Helper()
	original := oauthSessions
	oauthSessions = store
	t.Cleanup(func() {
		oauthSessions = original
	})
}
