package registry

import (
	"testing"
	"time"
)

func getCircuitBreakerTracker(t *testing.T, r *ModelRegistry, clientID, modelID string) failureTracker {
	t.Helper()

	r.mutex.RLock()
	defer r.mutex.RUnlock()

	registration, ok := r.models[modelID]
	if !ok || registration == nil {
		t.Fatalf("model registration not found for %s", modelID)
	}
	if registration.CircuitBreakerClients == nil {
		t.Fatalf("circuit breaker tracker map not found for %s", modelID)
	}
	tracker, ok := registration.CircuitBreakerClients[clientID]
	if !ok || tracker == nil {
		t.Fatalf("circuit breaker tracker not found for client=%s model=%s", clientID, modelID)
	}
	return *tracker
}

func setCircuitBreakerTrackerState(t *testing.T, r *ModelRegistry, clientID, modelID string, mutate func(*failureTracker)) {
	t.Helper()

	r.mutex.Lock()
	defer r.mutex.Unlock()

	registration, ok := r.models[modelID]
	if !ok || registration == nil || registration.CircuitBreakerClients == nil {
		t.Fatalf("circuit breaker registration not found for client=%s model=%s", clientID, modelID)
	}
	tracker, ok := registration.CircuitBreakerClients[clientID]
	if !ok || tracker == nil {
		t.Fatalf("circuit breaker tracker not found for client=%s model=%s", clientID, modelID)
	}
	mutate(tracker)
}

func assertDurationApprox(t *testing.T, got, want, tolerance time.Duration) {
	t.Helper()
	delta := got - want
	if delta < 0 {
		delta = -delta
	}
	if delta > tolerance {
		t.Fatalf("duration mismatch: got=%v want=%v tolerance=%v", got, want, tolerance)
	}
}

func TestCircuitBreakerOpenUsesBaseRecoveryTimeout(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-1", "openai", []*ModelInfo{{ID: "m1"}})

	r.RecordFailure("client-1", "m1", 2, 43200)
	beforeOpen := getCircuitBreakerTracker(t, r, "client-1", "m1")
	if beforeOpen.State != CircuitClosed {
		t.Fatalf("expected state %s before threshold, got %s", CircuitClosed, beforeOpen.State)
	}

	r.RecordFailure("client-1", "m1", 2, 43200)
	tracker := getCircuitBreakerTracker(t, r, "client-1", "m1")

	if tracker.State != CircuitOpen {
		t.Fatalf("expected state %s, got %s", CircuitOpen, tracker.State)
	}
	if tracker.FailureCount != 1 {
		t.Fatalf("expected failureCount 1, got %d", tracker.FailureCount)
	}
	assertDurationApprox(t, tracker.RecoveryAt.Sub(tracker.LastFailure), 12*time.Hour, time.Second)
}

func TestCircuitBreakerHalfOpenFailureReopensImmediatelyAndDoubles(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-1", "openai", []*ModelInfo{{ID: "m1"}})

	r.RecordFailure("client-1", "m1", 1, 2)
	setCircuitBreakerTrackerState(t, r, "client-1", "m1", func(tracker *failureTracker) {
		tracker.State = CircuitHalfOpen
		tracker.Count = 0
	})

	// threshold is intentionally high to prove half-open failure no longer waits for threshold.
	r.RecordFailure("client-1", "m1", 99, 2)
	first := getCircuitBreakerTracker(t, r, "client-1", "m1")
	if first.State != CircuitOpen {
		t.Fatalf("expected state %s after half-open failure, got %s", CircuitOpen, first.State)
	}
	if first.FailureCount != 2 {
		t.Fatalf("expected failureCount 2 after first half-open failure, got %d", first.FailureCount)
	}
	assertDurationApprox(t, first.RecoveryAt.Sub(first.LastFailure), 4*time.Second, time.Second)

	setCircuitBreakerTrackerState(t, r, "client-1", "m1", func(tracker *failureTracker) {
		tracker.State = CircuitHalfOpen
		tracker.Count = 0
	})
	r.RecordFailure("client-1", "m1", 99, 2)
	second := getCircuitBreakerTracker(t, r, "client-1", "m1")
	if second.FailureCount != 3 {
		t.Fatalf("expected failureCount 3 after second half-open failure, got %d", second.FailureCount)
	}
	assertDurationApprox(t, second.RecoveryAt.Sub(second.LastFailure), 8*time.Second, time.Second)
}

func TestCircuitBreakerHalfOpenSuccessResetsBackoffLevel(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-1", "openai", []*ModelInfo{{ID: "m1"}})

	r.RecordFailure("client-1", "m1", 1, 2)
	setCircuitBreakerTrackerState(t, r, "client-1", "m1", func(tracker *failureTracker) {
		tracker.State = CircuitHalfOpen
		tracker.FailureCount = 4
		tracker.RecoveryAt = time.Now().Add(16 * time.Second)
	})

	r.RecordSuccess("client-1", "m1")
	afterSuccess := getCircuitBreakerTracker(t, r, "client-1", "m1")
	if afterSuccess.State != CircuitClosed {
		t.Fatalf("expected state %s after success, got %s", CircuitClosed, afterSuccess.State)
	}
	if afterSuccess.FailureCount != 0 {
		t.Fatalf("expected failureCount reset to 0, got %d", afterSuccess.FailureCount)
	}
	if !afterSuccess.RecoveryAt.IsZero() {
		t.Fatalf("expected recoveryAt reset to zero, got %s", afterSuccess.RecoveryAt.Format(time.RFC3339))
	}

	r.RecordFailure("client-1", "m1", 1, 2)
	reopened := getCircuitBreakerTracker(t, r, "client-1", "m1")
	if reopened.FailureCount != 1 {
		t.Fatalf("expected failureCount 1 after reset and reopen, got %d", reopened.FailureCount)
	}
	assertDurationApprox(t, reopened.RecoveryAt.Sub(reopened.LastFailure), 2*time.Second, time.Second)
}

func TestCircuitBreakerHalfOpenFailureOverflowSaturates(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-1", "openai", []*ModelInfo{{ID: "m1"}})
	r.RecordFailure("client-1", "m1", 1, 43200)

	setCircuitBreakerTrackerState(t, r, "client-1", "m1", func(tracker *failureTracker) {
		tracker.State = CircuitHalfOpen
		tracker.FailureCount = 62
	})

	r.RecordFailure("client-1", "m1", 1, 43200)
	tracker := getCircuitBreakerTracker(t, r, "client-1", "m1")
	if tracker.State != CircuitOpen {
		t.Fatalf("expected state %s, got %s", CircuitOpen, tracker.State)
	}
	if tracker.FailureCount != 63 {
		t.Fatalf("expected failureCount 63, got %d", tracker.FailureCount)
	}
	if !tracker.RecoveryAt.After(tracker.LastFailure) {
		t.Fatalf("expected recoveryAt after lastFailure, got recoveryAt=%s lastFailure=%s", tracker.RecoveryAt.Format(time.RFC3339), tracker.LastFailure.Format(time.RFC3339))
	}
	if tracker.RecoveryAt.Sub(tracker.LastFailure) < 200*365*24*time.Hour {
		t.Fatalf("expected saturated large recovery window, got %v", tracker.RecoveryAt.Sub(tracker.LastFailure))
	}
}
