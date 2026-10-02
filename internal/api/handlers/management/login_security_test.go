package management

import (
	"net/http"
	"testing"
	"time"

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
