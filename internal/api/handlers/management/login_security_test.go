package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

func newLoginTestHandler(set store.LoginSecuritySettings) *Handler {
	h := &Handler{
		cfg:            &config.Config{},
		failedAttempts: make(map[string]*attemptInfo),
		envSecret:      "test-secret",
	}
	h.loginSettings = set
	h.loginSettingsAt = time.Now()
	return h
}

func TestAuthenticateManagementKey_DefaultsToFiveFailures(t *testing.T) {
	h := newLoginTestHandler(store.DefaultLoginSecuritySettings())
	for i := 0; i < 5; i++ {
		allowed, status, msg := h.AuthenticateManagementKey("127.0.0.1", true, "wrong")
		if allowed || status != http.StatusUnauthorized || msg != "invalid management key" {
			t.Fatalf("attempt %d: allowed=%v status=%d msg=%q", i+1, allowed, status, msg)
		}
	}
	allowed, status, msg := h.AuthenticateManagementKey("127.0.0.1", true, "test-secret")
	if allowed || status != http.StatusForbidden || msg[:3] != "IP " {
		t.Fatalf("expected ban after 5 failures: allowed=%v status=%d msg=%q", allowed, status, msg)
	}
}

func TestAuthenticateManagementKey_ConfigurableThreshold(t *testing.T) {
	set := store.DefaultLoginSecuritySettings()
	set.MaxFailedAttempts = 3
	h := newLoginTestHandler(set)
	for i := 0; i < 3; i++ {
		h.AuthenticateManagementKey("127.0.0.1", true, "wrong")
	}
	if allowed, _, _ := h.AuthenticateManagementKey("127.0.0.1", true, "test-secret"); allowed {
		t.Fatal("expected ban after configured threshold of 3")
	}
}

func TestAuthenticateManagementKey_BanDisabled(t *testing.T) {
	set := store.DefaultLoginSecuritySettings()
	set.Enabled = false
	h := newLoginTestHandler(set)
	for i := 0; i < 20; i++ {
		h.AuthenticateManagementKey("127.0.0.1", true, "wrong")
	}
	if allowed, _, _ := h.AuthenticateManagementKey("127.0.0.1", true, "test-secret"); !allowed {
		t.Fatal("correct key must still work when banning is disabled")
	}
}

func TestApplyManagementFailure_TumblingWindow(t *testing.T) {
	set := store.DefaultLoginSecuritySettings()
	set.MaxFailedAttempts = 3
	set.FailureWindowSeconds = 1
	h := newLoginTestHandler(set)

	h.applyManagementFailure("9.9.9.9", store.LoginOutcomeInvalidKey)
	h.applyManagementFailure("9.9.9.9", store.LoginOutcomeInvalidKey)
	h.attemptsMu.Lock()
	h.failedAttempts["9.9.9.9"].windowStart = time.Now().Add(-2 * time.Second)
	h.attemptsMu.Unlock()
	// The old window expired, so this failure starts a fresh window and must
	// not ban.
	outcome, _ := h.applyManagementFailure("9.9.9.9", store.LoginOutcomeInvalidKey)
	if outcome == store.LoginOutcomeBanStarted {
		t.Fatal("tumbling window should have reset the counter instead of banning")
	}
}

type fakeLoginStore struct {
	settings store.LoginSecuritySettings
}

func (f *fakeLoginStore) GetLoginSettings(ctx context.Context) (store.LoginSecuritySettings, error) {
	return f.settings, nil
}

func (f *fakeLoginStore) UpsertLoginSettings(ctx context.Context, s store.LoginSecuritySettings) (store.LoginSecuritySettings, error) {
	f.settings = store.ClampLoginSecuritySettings(s)
	return f.settings, nil
}

func (f *fakeLoginStore) RecordLoginEvents(ctx context.Context, events []store.LoginEvent) error {
	return nil
}

func (f *fakeLoginStore) ListLoginEvents(ctx context.Context, flt store.LoginEventFilter, page, pageSize int) ([]store.LoginEvent, int64, error) {
	return []store.LoginEvent{{ID: 1, IP: "1.2.3.4", Outcome: store.LoginOutcomeInvalidKey}}, 1, nil
}

func (f *fakeLoginStore) PurgeLoginEventsBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	return 0, nil
}

func (f *fakeLoginStore) ClearLoginEvents(ctx context.Context) (int64, error) { return 0, nil }

func TestPutLoginSecuritySettingsUpdatesCache(t *testing.T) {
	h := newLoginTestHandler(store.DefaultLoginSecuritySettings())
	h.pgLogin = &fakeLoginStore{settings: store.DefaultLoginSecuritySettings()}

	engine := gin.New()
	engine.PUT("/settings", h.PutLoginSecuritySettings)

	body := strings.NewReader(`{"max_failed_attempts":9}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/settings", body)
	req.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := h.currentLoginSettings().MaxFailedAttempts; got != 9 {
		t.Fatalf("cache not updated: MaxFailedAttempts = %d, want 9", got)
	}
}

func TestListLoginSecuritySettingsRequiresPG(t *testing.T) {
	h := &Handler{cfg: &config.Config{}, failedAttempts: make(map[string]*attemptInfo)}
	engine := gin.New()
	engine.GET("/settings", h.GetLoginSecuritySettings)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/settings", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}
