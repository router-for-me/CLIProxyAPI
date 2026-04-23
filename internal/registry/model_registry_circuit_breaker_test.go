package registry

import (
	"context"
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

func TestCircuitBreakerClosedSuccessResetsFailureCount(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-1", "openai", []*ModelInfo{{ID: "m1"}})

	r.RecordFailure("client-1", "m1", 3, 2)
	r.RecordSuccess("client-1", "m1")
	afterSuccess := getCircuitBreakerTracker(t, r, "client-1", "m1")
	if afterSuccess.State != CircuitClosed {
		t.Fatalf("expected state %s after closed-state success, got %s", CircuitClosed, afterSuccess.State)
	}
	if afterSuccess.Count != 0 {
		t.Fatalf("expected count reset to 0 after closed-state success, got %d", afterSuccess.Count)
	}

	r.RecordFailure("client-1", "m1", 3, 2)
	afterNewFailure := getCircuitBreakerTracker(t, r, "client-1", "m1")
	if afterNewFailure.State != CircuitClosed {
		t.Fatalf("expected state %s after fresh failure, got %s", CircuitClosed, afterNewFailure.State)
	}
	if afterNewFailure.Count != 1 {
		t.Fatalf("expected fresh failure count 1 after reset, got %d", afterNewFailure.Count)
	}
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

func TestGetCircuitBreakerStatusSeparatesBackoffLevelAndConsecutiveFailures(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-1", "openai", []*ModelInfo{{ID: "m1"}})

	r.RecordFailure("client-1", "m1", 1, 2)
	status := r.GetCircuitBreakerStatus()
	initial, ok := status["client-1"]["m1"]
	if !ok {
		t.Fatalf("missing circuit breaker status for client-1/m1")
	}
	if initial.FailureCount != 1 {
		t.Fatalf("failureCount = %d, want 1 (backoff level)", initial.FailureCount)
	}
	if initial.ConsecutiveFailures != 1 {
		t.Fatalf("consecutiveFailures = %d, want 1 (continuous failures)", initial.ConsecutiveFailures)
	}

	setCircuitBreakerTrackerState(t, r, "client-1", "m1", func(tracker *failureTracker) {
		tracker.State = CircuitHalfOpen
		tracker.Count = 0
		tracker.FailureCount = 1
	})
	r.RecordFailure("client-1", "m1", 99, 2)

	status = r.GetCircuitBreakerStatus()
	reopened, ok := status["client-1"]["m1"]
	if !ok {
		t.Fatalf("missing reopened circuit breaker status for client-1/m1")
	}
	if reopened.FailureCount != 2 {
		t.Fatalf("failureCount = %d, want 2 after half-open failure", reopened.FailureCount)
	}
	if reopened.ConsecutiveFailures != 1 {
		t.Fatalf("consecutiveFailures = %d, want 1 after half-open failure", reopened.ConsecutiveFailures)
	}
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

func TestRestoreCircuitBreakersCreatesMissingTrackerForRegisteredClient(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-1", "openai", []*ModelInfo{{ID: "m1"}})

	lastFailure := time.Date(2026, time.April, 17, 10, 0, 0, 0, time.UTC)
	recoveryAt := lastFailure.Add(30 * time.Second)
	applied, skipped := r.RestoreCircuitBreakers(map[string]map[string]CircuitBreakerPersistStatus{
		"m1": {
			"client-1": {
				State:        CircuitOpen,
				Count:        3,
				FailureCount: 2,
				LastFailure:  lastFailure,
				RecoveryAt:   recoveryAt,
			},
		},
	})
	if applied != 1 || skipped != 0 {
		t.Fatalf("RestoreCircuitBreakers() = applied:%d skipped:%d, want applied:1 skipped:0", applied, skipped)
	}

	tracker := getCircuitBreakerTracker(t, r, "client-1", "m1")
	if tracker.State != CircuitOpen {
		t.Fatalf("expected restored state %s, got %s", CircuitOpen, tracker.State)
	}
	if tracker.Count != 3 {
		t.Fatalf("expected restored count 3, got %d", tracker.Count)
	}
	if tracker.FailureCount != 2 {
		t.Fatalf("expected restored failureCount 2, got %d", tracker.FailureCount)
	}
	if tracker.OpenCycles != 2 {
		t.Fatalf("expected restored openCycles 2, got %d", tracker.OpenCycles)
	}
	if !tracker.LastFailure.Equal(lastFailure) {
		t.Fatalf("expected restored lastFailure %v, got %v", lastFailure, tracker.LastFailure)
	}
	if !tracker.RecoveryAt.Equal(recoveryAt) {
		t.Fatalf("expected restored recoveryAt %v, got %v", recoveryAt, tracker.RecoveryAt)
	}
}

type capturingCircuitBreakerOpenHook struct {
	ch chan CircuitBreakerOpenEvent
}

func (h *capturingCircuitBreakerOpenHook) OnCircuitBreakerOpened(_ context.Context, event CircuitBreakerOpenEvent) {
	if h == nil || h.ch == nil {
		return
	}
	select {
	case h.ch <- event:
	default:
	}
}

func TestCircuitBreakerOpenHookEmitsOpenCycles(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-1", "openai", []*ModelInfo{{ID: "m1"}})

	hook := &capturingCircuitBreakerOpenHook{ch: make(chan CircuitBreakerOpenEvent, 2)}
	r.SetCircuitBreakerOpenHook(hook)

	r.RecordFailure("client-1", "m1", 1, 2)
	var first CircuitBreakerOpenEvent
	select {
	case first = <-hook.ch:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting first open hook event")
	}
	if first.OpenCycles != 1 {
		t.Fatalf("first open cycles = %d, want 1", first.OpenCycles)
	}
	if first.Provider != "openai" {
		t.Fatalf("provider = %q, want %q", first.Provider, "openai")
	}

	setCircuitBreakerTrackerState(t, r, "client-1", "m1", func(tracker *failureTracker) {
		tracker.State = CircuitHalfOpen
		tracker.Count = 0
	})
	r.RecordFailure("client-1", "m1", 1, 2)
	var second CircuitBreakerOpenEvent
	select {
	case second = <-hook.ch:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting second open hook event")
	}
	if second.OpenCycles != 2 {
		t.Fatalf("second open cycles = %d, want 2", second.OpenCycles)
	}
}

func TestRestoreCircuitBreakersSkipsUnknownOrUnsupportedBindings(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-1", "openai", []*ModelInfo{{ID: "m1"}})
	r.RegisterClient("client-2", "openai", []*ModelInfo{{ID: "m2"}})

	applied, skipped := r.RestoreCircuitBreakers(map[string]map[string]CircuitBreakerPersistStatus{
		"m1": {
			"client-2": {State: CircuitOpen},
			"client-3": {State: CircuitOpen},
		},
		"m3": {
			"client-1": {State: CircuitOpen},
		},
	})
	if applied != 0 || skipped != 3 {
		t.Fatalf("RestoreCircuitBreakers() = applied:%d skipped:%d, want applied:0 skipped:3", applied, skipped)
	}

	r.mutex.RLock()
	defer r.mutex.RUnlock()
	if registration := r.models["m1"]; registration != nil && len(registration.CircuitBreakerClients) != 0 {
		t.Fatalf("expected no trackers restored for unsupported bindings, got %d", len(registration.CircuitBreakerClients))
	}
}

func TestRestoreCircuitBreakersContinuesBackoffProgressionOnNextFailure(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-1", "openai", []*ModelInfo{{ID: "m1"}})

	lastFailure := time.Date(2026, time.April, 17, 10, 0, 0, 0, time.UTC)
	recoveryAt := lastFailure.Add(4 * time.Second)
	applied, skipped := r.RestoreCircuitBreakers(map[string]map[string]CircuitBreakerPersistStatus{
		"m1": {
			"client-1": {
				State:        CircuitHalfOpen,
				Count:        0,
				FailureCount: 2,
				LastFailure:  lastFailure,
				RecoveryAt:   recoveryAt,
			},
		},
	})
	if applied != 1 || skipped != 0 {
		t.Fatalf("RestoreCircuitBreakers() = applied:%d skipped:%d, want applied:1 skipped:0", applied, skipped)
	}

	r.RecordFailure("client-1", "m1", 99, 2)
	tracker := getCircuitBreakerTracker(t, r, "client-1", "m1")
	if tracker.State != CircuitOpen {
		t.Fatalf("expected state %s after restored half-open failure, got %s", CircuitOpen, tracker.State)
	}
	if tracker.FailureCount != 3 {
		t.Fatalf("expected failureCount 3 after continuing restored state, got %d", tracker.FailureCount)
	}
	assertDurationApprox(t, tracker.RecoveryAt.Sub(tracker.LastFailure), 8*time.Second, time.Second)
}

func TestRestoreCircuitBreakersContinuesStateMachineOnNextSuccess(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-1", "openai", []*ModelInfo{{ID: "m1"}})

	lastFailure := time.Date(2026, time.April, 17, 10, 0, 0, 0, time.UTC)
	recoveryAt := lastFailure.Add(16 * time.Second)
	applied, skipped := r.RestoreCircuitBreakers(map[string]map[string]CircuitBreakerPersistStatus{
		"m1": {
			"client-1": {
				State:        CircuitHalfOpen,
				Count:        0,
				FailureCount: 4,
				LastFailure:  lastFailure,
				RecoveryAt:   recoveryAt,
			},
		},
	})
	if applied != 1 || skipped != 0 {
		t.Fatalf("RestoreCircuitBreakers() = applied:%d skipped:%d, want applied:1 skipped:0", applied, skipped)
	}

	r.RecordSuccess("client-1", "m1")
	tracker := getCircuitBreakerTracker(t, r, "client-1", "m1")
	if tracker.State != CircuitClosed {
		t.Fatalf("expected state %s after restored half-open success, got %s", CircuitClosed, tracker.State)
	}
	if tracker.FailureCount != 0 {
		t.Fatalf("expected failureCount reset after restored half-open success, got %d", tracker.FailureCount)
	}
	if !tracker.RecoveryAt.IsZero() {
		t.Fatalf("expected recoveryAt reset after restored half-open success, got %s", tracker.RecoveryAt.Format(time.RFC3339))
	}
}

func TestRegisterClientRemovesCircuitTrackersForRemovedModels(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-1", "openai", []*ModelInfo{{ID: "m1"}, {ID: "m2"}})

	r.RecordFailure("client-1", "m1", 1, 2)
	r.RecordFailure("client-1", "m2", 1, 2)

	// Remove m1 from the client model list; its circuit tracker should be removed too.
	r.RegisterClient("client-1", "openai", []*ModelInfo{{ID: "m2"}})

	snapshot := r.SnapshotCircuitBreakersPersist()
	if modelStatus, ok := snapshot["m1"]; ok {
		if _, exists := modelStatus["client-1"]; exists {
			t.Fatalf("expected m1 tracker to be removed after model unbind")
		}
	}
	if modelStatus, ok := snapshot["m2"]; !ok || modelStatus["client-1"].State == "" {
		t.Fatalf("expected m2 tracker to remain after model update")
	}
}

func TestUnregisterClientRemovesCircuitTrackersFromSharedModel(t *testing.T) {
	r := newTestModelRegistry()
	r.RegisterClient("client-1", "openai", []*ModelInfo{{ID: "shared-model"}})
	r.RegisterClient("client-2", "openai", []*ModelInfo{{ID: "shared-model"}})

	r.RecordFailure("client-1", "shared-model", 1, 2)
	r.RecordFailure("client-2", "shared-model", 1, 2)

	r.UnregisterClient("client-1")

	snapshot := r.SnapshotCircuitBreakersPersist()
	sharedStatus, ok := snapshot["shared-model"]
	if !ok {
		t.Fatalf("expected shared-model status to remain for client-2")
	}
	if _, exists := sharedStatus["client-1"]; exists {
		t.Fatalf("expected client-1 tracker to be removed on unregister")
	}
	if _, exists := sharedStatus["client-2"]; !exists {
		t.Fatalf("expected client-2 tracker to remain on unregister of client-1")
	}
}
