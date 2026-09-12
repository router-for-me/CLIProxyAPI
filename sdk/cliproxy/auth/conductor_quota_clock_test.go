package auth

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestQuotaDeadlineUsesWallClock_ModelExplicitRetry(t *testing.T) {
	withQuotaCooldownEnabled(t)

	manager := NewManager(nil, nil, nil)
	auth := &Auth{ID: "quota-clock-model-explicit", Provider: "codex"}
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	retryAfter := time.Hour
	manager.MarkResult(context.Background(), Result{
		AuthID:     auth.ID,
		Provider:   auth.Provider,
		Model:      "gpt-5",
		RetryAfter: &retryAfter,
		Error:      &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exceeded"},
	})

	updated, ok := manager.GetByID(auth.ID)
	if !ok || updated == nil || updated.ModelStates["gpt-5"] == nil {
		t.Fatal("missing model state for gpt-5")
	}

	retryDeadline := updated.ModelStates["gpt-5"].NextRetryAfter
	if retryDeadline.IsZero() || retryDeadline != retryDeadline.Round(0) {
		t.Errorf("model NextRetryAfter is zero or retains monotonic clock reading: %v", retryDeadline)
	}
	recoverDeadline := updated.ModelStates["gpt-5"].Quota.NextRecoverAt
	if recoverDeadline.IsZero() || recoverDeadline != recoverDeadline.Round(0) {
		t.Errorf("model Quota.NextRecoverAt is zero or retains monotonic clock reading: %v", recoverDeadline)
	}
	if !retryDeadline.Equal(recoverDeadline) {
		t.Errorf("expected NextRetryAfter and Quota.NextRecoverAt to match, got %v and %v", retryDeadline, recoverDeadline)
	}
}

func TestQuotaDeadlineUsesWallClock_ModelFallbackBackoff(t *testing.T) {
	withQuotaCooldownEnabled(t)

	manager := NewManager(nil, nil, nil)
	auth := &Auth{ID: "quota-clock-model-fallback", Provider: "codex"}
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	manager.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    "gpt-5",
		Error:    &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exceeded"},
	})

	updated, ok := manager.GetByID(auth.ID)
	if !ok || updated == nil || updated.ModelStates["gpt-5"] == nil {
		t.Fatal("missing model state for gpt-5")
	}

	retryDeadline := updated.ModelStates["gpt-5"].NextRetryAfter
	if retryDeadline.IsZero() || retryDeadline != retryDeadline.Round(0) {
		t.Errorf("fallback model NextRetryAfter is zero or retains monotonic clock reading: %v", retryDeadline)
	}
	recoverDeadline := updated.ModelStates["gpt-5"].Quota.NextRecoverAt
	if recoverDeadline.IsZero() || recoverDeadline != recoverDeadline.Round(0) {
		t.Errorf("fallback model Quota.NextRecoverAt is zero or retains monotonic clock reading: %v", recoverDeadline)
	}
	if !retryDeadline.Equal(recoverDeadline) {
		t.Errorf("expected fallback NextRetryAfter and Quota.NextRecoverAt to match, got %v and %v", retryDeadline, recoverDeadline)
	}
}

func TestQuotaDeadlineUsesWallClock_AuthExplicitRetry(t *testing.T) {
	withQuotaCooldownEnabled(t)

	manager := NewManager(nil, nil, nil)
	auth := &Auth{ID: "quota-clock-auth-explicit", Provider: "codex"}
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	retryAfter := time.Hour
	manager.MarkResult(context.Background(), Result{
		AuthID:          auth.ID,
		Provider:        auth.Provider,
		Model:           "gpt-5",
		CredentialScope: true,
		RetryAfter:      &retryAfter,
		Error:           &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exceeded"},
	})

	updated, ok := manager.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatal("missing updated auth")
	}

	retryDeadline := updated.NextRetryAfter
	if retryDeadline.IsZero() || retryDeadline != retryDeadline.Round(0) {
		t.Errorf("auth NextRetryAfter is zero or retains monotonic clock reading: %v", retryDeadline)
	}
	recoverDeadline := updated.Quota.NextRecoverAt
	if recoverDeadline.IsZero() || recoverDeadline != recoverDeadline.Round(0) {
		t.Errorf("auth Quota.NextRecoverAt is zero or retains monotonic clock reading: %v", recoverDeadline)
	}
	if !retryDeadline.Equal(recoverDeadline) {
		t.Errorf("expected auth NextRetryAfter and Quota.NextRecoverAt to match, got %v and %v", retryDeadline, recoverDeadline)
	}
}

func TestQuotaDeadlineUsesWallClock_AuthFallbackBackoff(t *testing.T) {
	withQuotaCooldownEnabled(t)

	manager := NewManager(nil, nil, nil)
	auth := &Auth{ID: "quota-clock-auth-fallback", Provider: "codex"}
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	manager.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    "", // no model -> auth-level failure
		Error:    &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exceeded"},
	})

	updated, ok := manager.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatal("missing updated auth")
	}

	retryDeadline := updated.NextRetryAfter
	if retryDeadline.IsZero() || retryDeadline != retryDeadline.Round(0) {
		t.Errorf("auth fallback NextRetryAfter is zero or retains monotonic clock reading: %v", retryDeadline)
	}
	recoverDeadline := updated.Quota.NextRecoverAt
	if recoverDeadline.IsZero() || recoverDeadline != recoverDeadline.Round(0) {
		t.Errorf("auth fallback Quota.NextRecoverAt is zero or retains monotonic clock reading: %v", recoverDeadline)
	}
	if !retryDeadline.Equal(recoverDeadline) {
		t.Errorf("expected auth fallback NextRetryAfter and Quota.NextRecoverAt to match, got %v and %v", retryDeadline, recoverDeadline)
	}
}
