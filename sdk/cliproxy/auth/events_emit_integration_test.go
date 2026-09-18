package auth

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/events"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// TestPoolBreakerTripEmitsEvent verifies the round-2 breaker.tripped event
// is recorded when the pool breaker crosses the failure threshold. Uses the
// package-level globalPoolBreaker with a synthetic key so concurrent tests
// stay isolated by key.
func TestPoolBreakerTripEmitsEvent(t *testing.T) {
	ring := withEventRing(t)

	key := "smoke-trip-" + t.Name()
	base := time.Now()
	for i := 0; i < poolBreakerFailureThreshold; i++ {
		globalPoolBreaker.recordFailure(key, base.Add(time.Duration(i)*time.Second))
	}

	snap := ring.Snapshot()
	var tripEvent *events.Event
	for i := range snap {
		if snap[i].Type == "breaker.tripped" && snap[i].AuthID == key {
			tripEvent = &snap[i]
			break
		}
	}
	if tripEvent == nil {
		t.Fatalf("breaker.tripped event not recorded; got events=%+v", snap)
	}
	if tripEvent.Type != "breaker.tripped" {
		t.Errorf("event type = %q, want breaker.tripped", tripEvent.Type)
	}
	if tripEvent.AuthID != key {
		t.Errorf("event AuthID = %q, want %q", tripEvent.AuthID, key)
	}
}

// TestPoolBreakerProbeFailEmitsEvent verifies the round-2 breaker.probe_fail
// event fires when a HALF_OPEN probe re-trips the breaker via a failure.
func TestPoolBreakerProbeFailEmitsEvent(t *testing.T) {
	ring := withEventRing(t)

	key := "smoke-probe-fail-" + t.Name()
	base := time.Now()
	// Trip the breaker.
	for i := 0; i < poolBreakerFailureThreshold; i++ {
		globalPoolBreaker.recordFailure(key, base.Add(time.Duration(i)*time.Second))
	}
	// Force HALF_OPEN via admitProbe (the design path: deadline elapses, but
	// we can simulate it directly by checking the door is open then advancing
	// the deadline).
	globalPoolBreaker.mu.Lock()
	entry := globalPoolBreaker.pools[key]
	entry.state = breakerHalfOpen
	entry.openedAt = base // anchor the probe window so admitProbe takes the slot
	globalPoolBreaker.mu.Unlock()

	if !globalPoolBreaker.admitProbe(key, base) {
		t.Fatalf("admitProbe should accept the probe request when HALF_OPEN")
	}
	// Now drive a failure during the probe — recordFailure with the breaker
	// in HALF_OPEN will surface breaker.probe_fail AND breaker.tripped.
	globalPoolBreaker.recordFailure(key, base.Add(time.Millisecond))

	snap := ring.Snapshot()
	var sawProbeFail bool
	for i := range snap {
		if snap[i].Type == "breaker.probe_fail" && snap[i].AuthID == key {
			sawProbeFail = true
			break
		}
	}
	if !sawProbeFail {
		t.Fatalf("breaker.probe_fail event not recorded; got events=%+v", snap)
	}
}

// TestPoolBreakerProbeOkEmitsEvent verifies the round-2 breaker.probe_ok
// event fires when a HALF_OPEN probe succeeds.
func TestPoolBreakerProbeOkEmitsEvent(t *testing.T) {
	ring := withEventRing(t)

	key := "smoke-probe-ok-" + t.Name()
	base := time.Now()
	// Trip the breaker.
	for i := 0; i < poolBreakerFailureThreshold; i++ {
		globalPoolBreaker.recordFailure(key, base.Add(time.Duration(i)*time.Second))
	}
	// Force HALF_OPEN.
	globalPoolBreaker.mu.Lock()
	entry := globalPoolBreaker.pools[key]
	entry.state = breakerHalfOpen
	entry.openedAt = base
	globalPoolBreaker.mu.Unlock()

	if !globalPoolBreaker.admitProbe(key, base) {
		t.Fatalf("admitProbe should accept the probe request when HALF_OPEN")
	}
	globalPoolBreaker.recordSuccess(key, base.Add(time.Millisecond))

	snap := ring.Snapshot()
	var sawProbeOk bool
	for i := range snap {
		if snap[i].Type == "breaker.probe_ok" && snap[i].AuthID == key {
			sawProbeOk = true
			break
		}
	}
	if !sawProbeOk {
		t.Fatalf("breaker.probe_ok event not recorded; got events=%+v", snap)
	}
}

// TestMarkResultQuota403ReclassificationEmitsEvent verifies that the
// round-2 cooldown.reclassified event fires when the 403→429 reclassifier
// matches.
func TestMarkResultQuota403ReclassificationEmitsEvent(t *testing.T) {
	ring := withEventRing(t)
	withQuota403Reclassification(t, true)

	const (
		provider = "smoke-reclass-provider"
		authID   = "smoke-reclass-auth"
		model    = "smoke-reclass-model"
	)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(authID) })

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	auth := &Auth{ID: authID, Provider: provider, Status: StatusActive}
	if _, errReg := manager.Register(context.Background(), auth); errReg != nil {
		t.Fatalf("Register() error = %v", errReg)
	}

	manager.MarkResult(context.Background(), Result{
		AuthID:   authID,
		Provider: provider,
		Model:    model,
		Success:  false,
		Error:    &Error{Code: "forbidden", Message: "quota exceeded for this model", HTTPStatus: http.StatusForbidden},
	})

	snap := ring.Snapshot()
	var sawReclass bool
	for i := range snap {
		if snap[i].Type == "cooldown.reclassified" && snap[i].AuthID == authID {
			sawReclass = true
			break
		}
	}
	if !sawReclass {
		t.Fatalf("cooldown.reclassified event not recorded; got events=%+v", snap)
	}
}

// TestCooldownWaitEmitsRoutingCooldownWaitEvent exercises the waitForCooldown
// smoke path: a positive wait drives a routing.cooldown_wait event when the
// timer elapses.
func TestCooldownWaitEmitsRoutingCooldownWaitEvent(t *testing.T) {
	ring := withEventRing(t)

	err := waitForCooldown(context.Background(), 5*time.Millisecond, 100*time.Millisecond, []string{"claude"}, "smoke-cooldown-model")
	if err != nil {
		t.Fatalf("waitForCooldown() error = %v", err)
	}
	snap := ring.Snapshot()
	var sawCooldownWait bool
	for i := range snap {
		if snap[i].Type == "routing.cooldown_wait" && snap[i].Model == "smoke-cooldown-model" {
			sawCooldownWait = true
			break
		}
	}
	if !sawCooldownWait {
		t.Fatalf("routing.cooldown_wait event not recorded; got events=%+v", snap)
	}
}

// TestAttemptsExhaustedFiresOnNoCandidates exercises the Execute retry loop's
// give-up path: no auth available → routing.attempts_exhausted is emitted
// once.
func TestAttemptsExhaustedFiresOnNoCandidates(t *testing.T) {
	ring := withEventRing(t)
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.SetRetryConfig(1, 5*time.Second, 0)
	// No auths registered → executeMixedOnce returns auth_not_found.
	_, errExec := manager.Execute(context.Background(), []string{"empty-provider"},
		cliproxyexecutor.Request{Model: "any-model"},
		cliproxyexecutor.Options{})
	if errExec == nil {
		t.Fatal("Execute should fail when no auth is available")
	}
	snap := ring.Snapshot()
	var sawAttemptsExhausted bool
	for i := range snap {
		if snap[i].Type == "routing.attempts_exhausted" && snap[i].Model == "any-model" {
			sawAttemptsExhausted = true
			break
		}
	}
	if !sawAttemptsExhausted {
		t.Fatalf("routing.attempts_exhausted event not recorded; got events=%+v", snap)
	}
}

// TestAttemptsExhaustedCarriesLastTriedAuthID closes the spec gap from
// Task 12's review (commit adcffddb): when the inner retry loop has at
// least one auth to try and that auth's dispatch fails, the outer loop's
// routing.attempts_exhausted event must carry that auth's ID. Before the
// fix, the outer loop cleared lastAuthID on every iteration and the event
// always recorded AuthID="". This test registers a single auth backed by an
// executor that always returns 500, drives the retry loop to its
// give-up path, and asserts the recorded event's AuthID matches the
// registered auth. The no-candidates test above covers the legitimately
// empty-AuthID path.
func TestAttemptsExhaustedCarriesLastTriedAuthID(t *testing.T) {
	ring := withEventRing(t)

	const (
		provider = "attempts-exhausted-auth-provider"
		authID   = "attempts-exhausted-auth"
		model    = "attempts-exhausted-model"
	)

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { reg.UnregisterClient(authID) })

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	// Single credential, no cross-credential retries, no cooldown so the
	// outer loop breaks on the first failure. The inner loop's
	// maxRetryCredentials=1 guard fires after the first attempt, returning
	// the failing error along with lastAuth = the auth we just dispatched.
	manager.SetRetryConfig(0, 0, 1)
	exec := &exhaustionAttemptExecutor{id: provider}
	manager.RegisterExecutor(exec)

	auth := &Auth{ID: authID, Provider: provider, Status: StatusActive}
	if _, errReg := manager.Register(context.Background(), auth); errReg != nil {
		t.Fatalf("Register() error = %v", errReg)
	}

	_, errExec := manager.Execute(context.Background(), []string{provider},
		cliproxyexecutor.Request{Model: model},
		cliproxyexecutor.Options{})
	if errExec == nil {
		t.Fatal("Execute should fail when every auth returns 500")
	}

	snap := ring.Snapshot()
	var exhaustion *events.Event
	for i := range snap {
		if snap[i].Type == "routing.attempts_exhausted" && snap[i].Model == model {
			exhaustion = &snap[i]
			break
		}
	}
	if exhaustion == nil {
		t.Fatalf("routing.attempts_exhausted event not recorded; got events=%+v", snap)
	}
	if exhaustion.AuthID != authID {
		t.Errorf("routing.attempts_exhausted AuthID = %q, want %q (the auth the inner loop actually dispatched)", exhaustion.AuthID, authID)
	}
	if exec.Calls() != 1 {
		t.Errorf("executor was called %d times; want 1 (single credential, max-retry-credentials=1)", exec.Calls())
	}
}

// exhaustionAttemptExecutor is a minimal ProviderExecutor that always
// fails with 500. Used by TestAttemptsExhaustedCarriesLastTriedAuthID to
// drive the inner retry loop's exhaustion path with a real auth under
// dispatch — the precondition for populating the events.AuthID field.
type exhaustionAttemptExecutor struct {
	id string

	mu    sync.Mutex
	calls int
}

func (e *exhaustionAttemptExecutor) Identifier() string { return e.id }

func (e *exhaustionAttemptExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.recordCall()
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusInternalServerError, Message: "synthetic exhaustion failure"}
}

func (e *exhaustionAttemptExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.recordCall()
	return nil, &Error{HTTPStatus: http.StatusInternalServerError, Message: "synthetic exhaustion failure"}
}

func (e *exhaustionAttemptExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *exhaustionAttemptExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return e.Execute(context.Background(), nil, cliproxyexecutor.Request{}, cliproxyexecutor.Options{})
}

func (e *exhaustionAttemptExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "HttpRequest not implemented"}
}

func (e *exhaustionAttemptExecutor) recordCall() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.calls++
}

func (e *exhaustionAttemptExecutor) Calls() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.calls
}
