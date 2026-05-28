package auth

import (
	"context"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func registerAuthForCB(t *testing.T, m *Manager, id, provider string) {
	t.Helper()
	_, err := m.Register(context.Background(), &Auth{
		ID:       id,
		Provider: provider,
		Metadata: map[string]any{"email": id + "@example.com"},
	})
	if err != nil {
		t.Fatalf("register auth %s: %v", id, err)
	}
}

// markTransientFailureWithModel uses the model path so that auth.NextRetryAfter
// is only set by the circuit breaker (model-state aggregation for 503 sets model
// state.NextRetryAfter to now+1m, but circuit breaker sets auth.NextRetryAfter
// to now+cooldown when threshold is reached).
func markTransientFailureWithModel(m *Manager, authID string, statusCode int) {
	m.MarkResult(context.Background(), Result{
		AuthID:  authID,
		Model:   "test-model",
		Success: false,
		Error:   &Error{HTTPStatus: statusCode},
	})
}

func markSuccessWithModel(m *Manager, authID string) {
	m.MarkResult(context.Background(), Result{
		AuthID:  authID,
		Model:   "test-model",
		Success: true,
	})
}

// TestCircuitBreaker_CounterIncrements verifies ConsecutiveTransientFailures increments
// on each transient failure.
func TestCircuitBreaker_CounterIncrements(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.SetConfig(&internalconfig.Config{
		AuthCircuitBreakerThreshold:       10,
		AuthCircuitBreakerCooldownMinutes: 10,
	})
	registerAuthForCB(t, m, "cb-count", "claude")

	for i := 1; i <= 3; i++ {
		markTransientFailureWithModel(m, "cb-count", 503)
		auth, ok := m.GetByID("cb-count")
		if !ok {
			t.Fatal("auth not found")
		}
		if auth.ConsecutiveTransientFailures != i {
			t.Fatalf("after failure %d: expected ConsecutiveTransientFailures=%d, got %d", i, i, auth.ConsecutiveTransientFailures)
		}
	}
}

// TestCircuitBreaker_OpensAfterThreshold verifies that NextRetryAfter is extended
// to the cooldown duration once ConsecutiveTransientFailures reaches the threshold.
func TestCircuitBreaker_OpensAfterThreshold(t *testing.T) {
	const threshold = 3
	const cooldownMins = 10
	m := NewManager(nil, nil, nil)
	m.SetConfig(&internalconfig.Config{
		AuthCircuitBreakerThreshold:       threshold,
		AuthCircuitBreakerCooldownMinutes: cooldownMins,
	})
	registerAuthForCB(t, m, "cb-open", "claude")

	// threshold-1 failures — counter at 2, circuit not yet open.
	for i := 0; i < threshold-1; i++ {
		markTransientFailureWithModel(m, "cb-open", 503)
	}
	auth, _ := m.GetByID("cb-open")
	if auth.ConsecutiveTransientFailures != threshold-1 {
		t.Fatalf("after %d failures: expected counter=%d, got %d", threshold-1, threshold-1, auth.ConsecutiveTransientFailures)
	}

	// threshold-th failure — circuit should open; NextRetryAfter extended to cooldown.
	before := time.Now()
	markTransientFailureWithModel(m, "cb-open", 503)
	after := time.Now()

	auth, _ = m.GetByID("cb-open")
	if auth.ConsecutiveTransientFailures < threshold {
		t.Fatalf("expected ConsecutiveTransientFailures >= %d, got %d", threshold, auth.ConsecutiveTransientFailures)
	}

	minExpected := before.Add(cooldownMins * time.Minute)
	maxExpected := after.Add(cooldownMins * time.Minute)
	if auth.NextRetryAfter.Before(minExpected) || auth.NextRetryAfter.After(maxExpected) {
		t.Fatalf("NextRetryAfter %v not in cooldown range [%v, %v]", auth.NextRetryAfter, minExpected, maxExpected)
	}
}

// TestCircuitBreaker_ResetOnSuccess verifies that a success call clears the counter.
func TestCircuitBreaker_ResetOnSuccess(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.SetConfig(&internalconfig.Config{
		AuthCircuitBreakerThreshold:       5,
		AuthCircuitBreakerCooldownMinutes: 10,
	})
	registerAuthForCB(t, m, "cb-reset", "claude")

	// Accumulate failures below threshold.
	for i := 0; i < 3; i++ {
		markTransientFailureWithModel(m, "cb-reset", 502)
	}

	// Success resets counter.
	markSuccessWithModel(m, "cb-reset")

	auth, _ := m.GetByID("cb-reset")
	if auth.ConsecutiveTransientFailures != 0 {
		t.Fatalf("expected ConsecutiveTransientFailures=0 after success, got %d", auth.ConsecutiveTransientFailures)
	}
}

// TestCircuitBreaker_NonTransientErrorResetsCounter verifies that a 429 does not
// increment the transient failure counter and resets it instead.
func TestCircuitBreaker_NonTransientErrorResetsCounter(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.SetConfig(&internalconfig.Config{
		AuthCircuitBreakerThreshold:       5,
		AuthCircuitBreakerCooldownMinutes: 10,
	})
	registerAuthForCB(t, m, "cb-nontransient", "claude")

	// Build up some transient failures.
	for i := 0; i < 2; i++ {
		markTransientFailureWithModel(m, "cb-nontransient", 503)
	}

	// A 429 is not in the transient set — counter should reset.
	m.MarkResult(context.Background(), Result{
		AuthID:  "cb-nontransient",
		Model:   "test-model",
		Success: false,
		Error:   &Error{HTTPStatus: 429},
	})

	auth, _ := m.GetByID("cb-nontransient")
	if auth.ConsecutiveTransientFailures != 0 {
		t.Fatalf("expected counter reset on non-transient error, got %d", auth.ConsecutiveTransientFailures)
	}
}

// TestCircuitBreaker_Disabled verifies that threshold=-1 disables the breaker entirely.
// With disabled circuit breaker, NextRetryAfter should only reflect the base backoff
// (1 minute for transient errors), not the 10-minute cooldown.
func TestCircuitBreaker_Disabled(t *testing.T) {
	const cooldownMins = 10
	m := NewManager(nil, nil, nil)
	m.SetConfig(&internalconfig.Config{
		AuthCircuitBreakerThreshold:       -1,
		AuthCircuitBreakerCooldownMinutes: cooldownMins,
	})
	registerAuthForCB(t, m, "cb-disabled", "claude")

	before := time.Now()
	for i := 0; i < 20; i++ {
		markTransientFailureWithModel(m, "cb-disabled", 503)
	}

	auth, _ := m.GetByID("cb-disabled")
	// When disabled, the circuit breaker never extends NextRetryAfter to cooldown.
	// The base model-state backoff (1m) may still aggregate to auth.NextRetryAfter,
	// but it should never reach the cooldown window.
	maxBaseline := before.Add(cooldownMins * time.Minute)
	if !auth.NextRetryAfter.IsZero() && auth.NextRetryAfter.After(maxBaseline) {
		t.Fatalf("circuit breaker disabled but NextRetryAfter %v exceeds cooldown ceiling %v", auth.NextRetryAfter, maxBaseline)
	}
}

// TestCircuitBreaker_DefaultThreshold verifies defaults (threshold=0 → 5, cooldown=0 → 10m).
func TestCircuitBreaker_DefaultThreshold(t *testing.T) {
	m := NewManager(nil, nil, nil)
	// AuthCircuitBreakerThreshold=0 → use defaultCircuitBreakerThreshold (5)
	m.SetConfig(&internalconfig.Config{})
	registerAuthForCB(t, m, "cb-default", "claude")

	// Fail threshold-1 times — circuit should still use model-state backoff only.
	for i := 0; i < defaultCircuitBreakerThreshold-1; i++ {
		markTransientFailureWithModel(m, "cb-default", 504)
	}
	auth, _ := m.GetByID("cb-default")
	if auth.ConsecutiveTransientFailures != defaultCircuitBreakerThreshold-1 {
		t.Fatalf("expected counter=%d, got %d", defaultCircuitBreakerThreshold-1, auth.ConsecutiveTransientFailures)
	}

	// One more failure — counter should reach threshold, NextRetryAfter extended.
	before := time.Now()
	markTransientFailureWithModel(m, "cb-default", 504)
	after := time.Now()

	auth, _ = m.GetByID("cb-default")
	if auth.ConsecutiveTransientFailures < defaultCircuitBreakerThreshold {
		t.Fatalf("expected counter >= %d at threshold, got %d", defaultCircuitBreakerThreshold, auth.ConsecutiveTransientFailures)
	}

	minExpected := before.Add(defaultCircuitBreakerCooldownMinutes * time.Minute)
	maxExpected := after.Add(defaultCircuitBreakerCooldownMinutes * time.Minute)
	if auth.NextRetryAfter.Before(minExpected) || auth.NextRetryAfter.After(maxExpected) {
		t.Fatalf("NextRetryAfter %v not in default cooldown range [%v, %v]", auth.NextRetryAfter, minExpected, maxExpected)
	}
}

// TestCircuitBreaker_RaceCondition exercises concurrent SetConfig + MarkResult under -race.
func TestCircuitBreaker_RaceCondition(t *testing.T) {
	m := NewManager(nil, nil, nil)
	m.SetConfig(&internalconfig.Config{AuthCircuitBreakerThreshold: 3})
	registerAuthForCB(t, m, "cb-race", "claude")

	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			m.SetConfig(&internalconfig.Config{AuthCircuitBreakerThreshold: 3})
		}
		close(done)
	}()
	for i := 0; i < 100; i++ {
		markTransientFailureWithModel(m, "cb-race", 503)
	}
	<-done
}
