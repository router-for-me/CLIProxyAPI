package auth

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

const cooldownTestTolerance = 2 * time.Second

func registerCooldownTestAuth(t *testing.T, manager *Manager, id string) {
	t.Helper()
	_, err := manager.Register(context.Background(), &Auth{
		ID:       id,
		Provider: "codex",
		Status:   StatusActive,
	})
	if err != nil {
		t.Fatalf("register auth: %v", err)
	}
}

func enableAdaptiveCooldown(manager *Manager, unauthorizedSeconds int) {
	manager.SetConfig(&internalconfig.Config{
		Codex: internalconfig.CodexConfig{
			QuotaCooldown: internalconfig.CodexQuotaCooldownConfig{
				Enabled:                     true,
				UnauthorizedCooldownSeconds: unauthorizedSeconds,
			},
		},
	})
}

func assertCooldownNear(t *testing.T, next, before, after time.Time, want time.Duration) {
	t.Helper()
	minimum := before.Add(want)
	maximum := after.Add(want).Add(cooldownTestTolerance)
	if next.Before(minimum) || next.After(maximum) {
		t.Fatalf("cooldown deadline %v outside [%v, %v]", next, minimum, maximum)
	}
}

func TestManager_MarkResult_UnauthorizedDefaultsToLegacyCooldown(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	registerCooldownTestAuth(t, manager, "auth-401-default")

	before := time.Now()
	manager.MarkResult(context.Background(), Result{
		AuthID:   "auth-401-default",
		Provider: "codex",
		Error:    &Error{HTTPStatus: http.StatusUnauthorized, Message: "unauthorized"},
	})
	after := time.Now()

	updated, _ := manager.GetByID("auth-401-default")
	assertCooldownNear(t, updated.NextRetryAfter, before, after, legacyUnauthorizedCooldown)
}

func TestManager_MarkResult_UnauthorizedUsesConfiguredCooldown(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	enableAdaptiveCooldown(manager, int((3 * time.Hour).Seconds()))
	registerCooldownTestAuth(t, manager, "model-401-configured")

	before := time.Now()
	manager.MarkResult(context.Background(), Result{
		AuthID:   "model-401-configured",
		Provider: "codex",
		Model:    "gpt-5.4-mini",
		Error:    &Error{HTTPStatus: http.StatusUnauthorized, Message: "unauthorized"},
	})
	after := time.Now()

	updated, _ := manager.GetByID("model-401-configured")
	state := updated.ModelStates["gpt-5.4-mini"]
	if state == nil {
		t.Fatal("model state not found")
	}
	assertCooldownNear(t, state.NextRetryAfter, before, after, 3*time.Hour)
}

func TestQuotaCooldownAfterFailure_AdaptiveLadderAndCap(t *testing.T) {
	policy := codexCooldownPolicy{
		adaptive:         true,
		transientBackoff: normalizeAdaptiveBackoff(nil),
		jitterPercent:    20,
	}
	now := time.Unix(1_700_000_000, 0)
	quota := QuotaState{}
	want := []time.Duration{15 * time.Second, 30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute, 5 * time.Minute}
	for i, expected := range want {
		next, level := quotaCooldownAfterFailure(quota, now, policy, 0.5)
		if got := next.Sub(now); got != expected {
			t.Fatalf("step %d cooldown = %v, want %v", i, got, expected)
		}
		quota.BackoffLevel = level
		quota.NextRecoverAt = time.Time{}
	}
}

func TestQuotaCooldownAfterFailure_DuplicateWithinWindowDoesNotEscalate(t *testing.T) {
	policy := codexCooldownPolicy{adaptive: true, transientBackoff: normalizeAdaptiveBackoff(nil), jitterPercent: 20}
	now := time.Unix(1_700_000_000, 0)
	first, level := quotaCooldownAfterFailure(QuotaState{}, now, policy, 0.5)
	duplicate, duplicateLevel := quotaCooldownAfterFailure(QuotaState{
		Exceeded:      true,
		Reason:        "rate_limit",
		NextRecoverAt: first,
		BackoffLevel:  level,
	}, now.Add(time.Second), policy, 1)
	if !duplicate.Equal(first) || duplicateLevel != level {
		t.Fatalf("duplicate failure changed window: next=%v level=%d, want next=%v level=%d", duplicate, duplicateLevel, first, level)
	}
}

func TestManager_MarkResult_ConcurrentRateLimitsAdvanceOnce(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	enableAdaptiveCooldown(manager, 0)
	registerCooldownTestAuth(t, manager, "concurrent-429")

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			manager.MarkResult(context.Background(), Result{
				AuthID:   "concurrent-429",
				Provider: "codex",
				Model:    "gpt-5.4-mini",
				Error:    &Error{HTTPStatus: http.StatusTooManyRequests, Code: "rate_limit", Message: "rate limit"},
			})
		}()
	}
	wg.Wait()

	updated, _ := manager.GetByID("concurrent-429")
	state := updated.ModelStates["gpt-5.4-mini"]
	if state == nil || state.Quota.BackoffLevel != 1 {
		t.Fatalf("concurrent backoff level = %+v, want 1", state)
	}
	remaining := time.Until(state.Quota.NextRecoverAt)
	if remaining < 10*time.Second || remaining > 20*time.Second {
		t.Fatalf("concurrent cooldown remaining = %v, want first adaptive window", remaining)
	}
}

func TestManager_MarkResult_UsageLimitPreservesProviderReset(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	enableAdaptiveCooldown(manager, 0)
	registerCooldownTestAuth(t, manager, "auth-usage-limit")
	retryAfter := 7*24*time.Hour + 17*time.Second

	before := time.Now()
	manager.MarkResult(context.Background(), Result{
		AuthID:     "auth-usage-limit",
		Provider:   "codex",
		RetryAfter: &retryAfter,
		Error: &Error{
			HTTPStatus: http.StatusTooManyRequests,
			Code:       "usage_limit_reached",
			Message:    `{"error":{"type":"usage_limit_reached"}}`,
		},
	})
	after := time.Now()

	updated, _ := manager.GetByID("auth-usage-limit")
	if updated.Quota.Reason != "usage_limit_reached" {
		t.Fatalf("quota reason = %q, want usage_limit_reached", updated.Quota.Reason)
	}
	assertCooldownNear(t, updated.NextRetryAfter, before, after, retryAfter)
}

func TestManager_MarkResult_ModelUsageLimitPreservesProviderReset(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	enableAdaptiveCooldown(manager, 0)
	registerCooldownTestAuth(t, manager, "model-usage-limit")
	retryAfter := 6*time.Hour + 31*time.Second

	before := time.Now()
	manager.MarkResult(context.Background(), Result{
		AuthID:     "model-usage-limit",
		Provider:   "codex",
		Model:      "gpt-5.4-mini",
		RetryAfter: &retryAfter,
		Error: &Error{
			HTTPStatus: http.StatusTooManyRequests,
			Code:       "usage_limit_reached",
			Message:    `{"error":{"type":"usage_limit_reached"}}`,
		},
	})
	after := time.Now()

	updated, _ := manager.GetByID("model-usage-limit")
	state := updated.ModelStates["gpt-5.4-mini"]
	if state == nil || state.Quota.Reason != "usage_limit_reached" {
		t.Fatalf("model usage state = %+v", state)
	}
	assertCooldownNear(t, state.NextRetryAfter, before, after, retryAfter)
}

func TestManager_MarkResult_SuccessClearsAdaptiveBackoff(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	enableAdaptiveCooldown(manager, 0)
	registerCooldownTestAuth(t, manager, "model-success-reset")

	manager.MarkResult(context.Background(), Result{
		AuthID:   "model-success-reset",
		Provider: "codex",
		Model:    "gpt-5.4-mini",
		Error:    &Error{HTTPStatus: http.StatusTooManyRequests, Code: "rate_limit", Message: "rate limit"},
	})
	failed, _ := manager.GetByID("model-success-reset")
	if failed.ModelStates["gpt-5.4-mini"].Quota.BackoffLevel != 1 {
		t.Fatalf("backoff level after failure = %d, want 1", failed.ModelStates["gpt-5.4-mini"].Quota.BackoffLevel)
	}

	manager.MarkResult(context.Background(), Result{
		AuthID:   "model-success-reset",
		Provider: "codex",
		Model:    "gpt-5.4-mini",
		Success:  true,
	})
	updated, _ := manager.GetByID("model-success-reset")
	state := updated.ModelStates["gpt-5.4-mini"]
	if state.Quota.BackoffLevel != 0 || state.Quota.Exceeded || !state.NextRetryAfter.IsZero() {
		t.Fatalf("success did not clear adaptive cooldown: %+v", state.Quota)
	}
}

func TestJitterAdaptiveCooldownBoundsAndCap(t *testing.T) {
	if got := jitterAdaptiveCooldown(time.Minute, 20, 0); got != 48*time.Second {
		t.Fatalf("minimum jitter = %v, want 48s", got)
	}
	if got := jitterAdaptiveCooldown(time.Minute, 20, 1); got != 72*time.Second {
		t.Fatalf("maximum jitter = %v, want 72s", got)
	}
	if got := jitterAdaptiveCooldown(5*time.Minute, 20, 1); got != 5*time.Minute {
		t.Fatalf("capped jitter = %v, want 5m", got)
	}
}
