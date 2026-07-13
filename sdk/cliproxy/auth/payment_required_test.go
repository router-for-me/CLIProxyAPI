package auth

import (
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
	if !suspend {
		t.Fatal("expected shouldSuspendModel true")
	}
	if !auth.Disabled || auth.Status != StatusDisabled {
		t.Fatalf("auth not disabled: disabled=%v status=%s", auth.Disabled, auth.Status)
	}
	if !auth.NextRetryAfter.IsZero() {
		t.Fatalf("expected no retry-after for permanent disable, got %v", auth.NextRetryAfter)
	}
	if state.Status != StatusDisabled {
		t.Fatalf("state status = %s", state.Status)
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
