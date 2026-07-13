package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestPaymentRequiredAction(t *testing.T) {
	t.Parallel()
	if got := paymentRequiredAction(nil); got != "cooldown" {
		t.Fatalf("nil cfg = %q", got)
	}
	if got := paymentRequiredAction(&internalconfig.Config{}); got != "cooldown" {
		t.Fatalf("empty = %q", got)
	}
	cfg := &internalconfig.Config{QuotaExceeded: internalconfig.QuotaExceeded{OnPaymentRequired: "disable"}}
	if got := paymentRequiredAction(cfg); got != "disable" {
		t.Fatalf("disable = %q", got)
	}
	cfg.QuotaExceeded.OnPaymentRequired = "CooldownDOWN"
	if got := paymentRequiredAction(cfg); got != "cooldown" {
		t.Fatalf("cooldown case = %q", got)
	}
	cfg.QuotaExceeded.OnPaymentRequired = "unknown"
	if got := paymentRequiredAction(cfg); got != "cooldown" {
		t.Fatalf("unknown = %q", got)
	}
}

func TestApplyPaymentRequiredModelFailure_Disable(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	auth := &Auth{ID: "k1", Status: StatusActive}
	state := &ModelState{Status: StatusActive}
	err := &Error{HTTPStatus: http.StatusPaymentRequired, Message: "insufficient balance"}

	suspend := applyPaymentRequiredModelFailure(auth, state, now, err, false, "disable")
	if suspend {
		t.Fatal("expected shouldSuspendModel false for auth-level disable")
	}
	if !auth.Disabled || auth.Status != StatusDisabled {
		t.Fatalf("auth not disabled: disabled=%v status=%s", auth.Disabled, auth.Status)
	}
	if !auth.NextRetryAfter.IsZero() {
		t.Fatalf("expected no retry-after for permanent disable, got %v", auth.NextRetryAfter)
	}
	// Model must not be hard-disabled so management re-enable restores routing.
	if state.Status != StatusError {
		t.Fatalf("state status = %s, want StatusError", state.Status)
	}
	if !state.NextRetryAfter.IsZero() {
		t.Fatalf("expected cleared model NextRetryAfter, got %v", state.NextRetryAfter)
	}
	if auth.StatusMessage != "insufficient balance" {
		t.Fatalf("auth StatusMessage = %q, want upstream detail", auth.StatusMessage)
	}
}

func TestApplyPaymentRequiredModelFailure_Cooldown(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	auth := &Auth{ID: "k1", Status: StatusActive}
	state := &ModelState{Status: StatusActive}
	err := &Error{HTTPStatus: http.StatusPaymentRequired, Message: "payment required"}

	suspend := applyPaymentRequiredModelFailure(auth, state, now, err, false, "cooldown")
	if !suspend {
		t.Fatal("expected suspend for cooldown path")
	}
	if auth.Disabled || auth.Status != StatusError {
		t.Fatalf("auth unexpected: disabled=%v status=%s", auth.Disabled, auth.Status)
	}
	if state.NextRetryAfter.Before(now.Add(29 * time.Minute)) {
		t.Fatalf("cooldown too short: %v", state.NextRetryAfter)
	}
}

func TestQuotaExceededPaymentRequiredAction(t *testing.T) {
	t.Parallel()
	q := internalconfig.QuotaExceeded{OnPaymentRequired: "Disable"}
	if q.PaymentRequiredAction() != "disable" {
		t.Fatalf("got %q", q.PaymentRequiredAction())
	}
}

func TestMarkResult_PaymentRequiredDisable_StopAuthAndSoftModelState(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.SetConfig(&internalconfig.Config{
		QuotaExceeded: internalconfig.QuotaExceeded{OnPaymentRequired: "disable"},
	})

	auth := &Auth{ID: "pay-key", Provider: "openai-compatibility", Status: StatusActive}
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("register: %v", err)
	}

	stop := m.markResult(context.Background(), Result{
		AuthID:   "pay-key",
		Provider: "openai-compatibility",
		Model:    "gpt-test",
		Success:  false,
		Error:    &Error{HTTPStatus: http.StatusPaymentRequired, Message: "insufficient balance"},
	})
	if !stop {
		t.Fatal("expected stopAuth=true after 402 disable")
	}

	updated, ok := m.GetByID("pay-key")
	if !ok || updated == nil {
		t.Fatal("auth missing")
	}
	if !updated.Disabled || updated.Status != StatusDisabled {
		t.Fatalf("auth not disabled: disabled=%v status=%s", updated.Disabled, updated.Status)
	}
	state := updated.ModelStates["gpt-test"]
	if state == nil {
		t.Fatal("missing model state")
	}
	if state.Status != StatusError {
		t.Fatalf("model status=%s want StatusError (soft)", state.Status)
	}
	if !state.NextRetryAfter.IsZero() {
		t.Fatalf("model NextRetryAfter should be zero, got %v", state.NextRetryAfter)
	}

	// Management re-enable only flips auth-level fields; model must not stay hard-disabled.
	updated.Disabled = false
	updated.Status = StatusActive
	updated.Unavailable = false
	if state.Status == StatusDisabled {
		t.Fatal("model StatusDisabled would block routing after re-enable")
	}
}
