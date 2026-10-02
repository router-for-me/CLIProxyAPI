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
	if allowed || status != http.StatusForbidden || !strings.HasPrefix(msg, "IP ") {
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

func TestShouldRecordLoginEvent(t *testing.T) {
	set := store.DefaultLoginSecuritySettings()
	if !shouldRecordLoginEvent(set, store.LoginOutcomeSuccess) {
		t.Fatal("success must be recorded when log_successes=true")
	}
	set.LogSuccesses = false
	if shouldRecordLoginEvent(set, store.LoginOutcomeSuccess) {
		t.Fatal("success must be suppressed when log_successes=false")
	}
	if !shouldRecordLoginEvent(set, store.LoginOutcomeInvalidKey) {
		t.Fatal("failure outcomes must always be recorded")
	}
	if !shouldRecordLoginEvent(set, store.LoginOutcomeBanStarted) {
		t.Fatal("ban_started must always be recorded")
	}
}

func TestApplyManagementFailure_DisabledDoesNotCount(t *testing.T) {
	set := store.DefaultLoginSecuritySettings()
	set.Enabled = false
	h := newLoginTestHandler(set)

	outcome, count := h.applyManagementFailure("1.1.1.1", store.LoginOutcomeInvalidKey)
	if outcome != store.LoginOutcomeInvalidKey {
		t.Fatalf("outcome = %q, want %q", outcome, store.LoginOutcomeInvalidKey)
	}
	if count != 0 {
		t.Fatalf("count = %d, want 0", count)
	}
	for i := 0; i < 10; i++ {
		h.applyManagementFailure("1.1.1.1", store.LoginOutcomeInvalidKey)
	}
	h.attemptsMu.Lock()
	ai := h.failedAttempts["1.1.1.1"]
	h.attemptsMu.Unlock()
	if ai == nil || ai.count != 0 || !ai.blockedUntil.IsZero() {
		t.Fatalf("disabled policy accrued ban state: %+v", ai)
	}
}

func TestAuthenticateManagementKey_DisabledRecoversActiveBan(t *testing.T) {
	set := store.DefaultLoginSecuritySettings()
	set.MaxFailedAttempts = 1
	h := newLoginTestHandler(set)
	// Drive the IP into a ban.
	if allowed, _, _ := h.AuthenticateManagementKey("127.0.0.1", true, "wrong"); allowed {
		t.Fatal("expected first failure to be rejected")
	}
	if allowed, status, _ := h.AuthenticateManagementKey("127.0.0.1", true, "test-secret"); allowed || status != http.StatusForbidden {
		t.Fatalf("expected active ban: allowed=%v status=%d", allowed, status)
	}
	// Disabling banning must immediately recover the active lockout.
	off := store.DefaultLoginSecuritySettings()
	off.Enabled = false
	h.setLoginSettingsCache(off)
	if allowed, _, _ := h.AuthenticateManagementKey("127.0.0.1", true, "test-secret"); !allowed {
		t.Fatal("disabling banning must short-circuit an active ban check")
	}
}

func TestMiddleware_ManagementTokenBypassesBanAccounting(t *testing.T) {
	set := store.DefaultLoginSecuritySettings()
	set.MaxFailedAttempts = 1
	h := newLoginTestHandler(set)
	h.allowRemoteOverride = true
	h.tokenAuthenticator = func(ctx context.Context, provided string) (*store.ManagementToken, *store.ManagementTokenPolicy) {
		if provided == "mgmt-token" {
			return &store.ManagementToken{ID: "tok-1", Status: store.MgmtTokenStatusActive}, nil
		}
		return nil, nil
	}

	engine := gin.New()
	engine.Use(h.Middleware())
	engine.GET("/x", func(c *gin.Context) { c.Status(http.StatusOK) })

	for i := 0; i < 5; i++ {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.Header.Set("X-Management-Key", "mgmt-token")
		engine.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("attempt %d: status = %d, body = %s", i+1, rec.Code, rec.Body.String())
		}
	}
	h.attemptsMu.Lock()
	n := len(h.failedAttempts)
	h.attemptsMu.Unlock()
	if n != 0 {
		t.Fatalf("management-token fallback accrued ban accounting: %d entries", n)
	}
}
