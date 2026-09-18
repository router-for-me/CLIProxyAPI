package auth

import (
	"context"
	"net/http"
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
