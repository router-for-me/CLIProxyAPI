package auth

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executionregistry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// poolBreakerGuardExecutor records dispatches per method so tests can assert
// whether an execution actually reached the executor (the probe commit) or was
// dropped by the pool-breaker admission gate. prepareHook runs from the
// RequestAuthPreparer seam — after pick/model-filter, before the gate — so a
// test can simulate a concurrent request stealing the probe slot mid-flight,
// which is the only state in which the gate's denial branch is reachable.
type poolBreakerGuardExecutor struct {
	id string

	mu          sync.Mutex
	exec        []string
	count       []string
	stream      []string
	prepareHook func()
}

func (e *poolBreakerGuardExecutor) Identifier() string { return e.id }

func (e *poolBreakerGuardExecutor) ShouldPrepareRequestAuth(*Auth) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.prepareHook != nil
}

func (e *poolBreakerGuardExecutor) PrepareRequestAuth(_ context.Context, auth *Auth) (*Auth, error) {
	e.mu.Lock()
	hook := e.prepareHook
	e.mu.Unlock()
	if hook != nil {
		hook()
	}
	return auth, nil
}

func (e *poolBreakerGuardExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	_ = ctx
	_ = auth
	e.mu.Lock()
	e.exec = append(e.exec, req.Model)
	e.mu.Unlock()
	return cliproxyexecutor.Response{Payload: []byte(req.Model)}, nil
}

func (e *poolBreakerGuardExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	_ = ctx
	_ = auth
	e.mu.Lock()
	e.stream = append(e.stream, req.Model)
	e.mu.Unlock()
	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte(req.Model)}
	close(ch)
	return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
}

func (e *poolBreakerGuardExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *poolBreakerGuardExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	_ = ctx
	_ = auth
	e.mu.Lock()
	e.count = append(e.count, req.Model)
	e.mu.Unlock()
	return cliproxyexecutor.Response{Payload: []byte(req.Model)}, nil
}

func (e *poolBreakerGuardExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("HttpRequest not implemented")
}

func (e *poolBreakerGuardExecutor) executeCalls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.exec...)
}

func (e *poolBreakerGuardExecutor) streamCalls() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.stream...)
}

// armProbeStealOnPrepare makes the executor's next PrepareRequestAuth call
// take the pool's probe slot, simulating a concurrent request winning the
// probe between this request's pick and its admission gate. The hook disarms
// itself after firing so a later rotation (if the test wants one) proceeds
// normally.
func (e *poolBreakerGuardExecutor) armProbeStealOnPrepare(pool string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.prepareHook = func() {
		globalPoolBreaker.mu.Lock()
		if entry, ok := globalPoolBreaker.pools[pool]; ok && entry.state == breakerHalfOpen && !entry.probeUsed {
			entry.probeUsed = true
			entry.openedAt = time.Now()
		}
		globalPoolBreaker.mu.Unlock()
		e.mu.Lock()
		e.prepareHook = nil
		e.mu.Unlock()
	}
}

// registerPoolBreakerGuardAuth registers one opted-in pool auth bound to a
// compound routing key ("claude:<row>") and registers the model in the global
// registry so scheduler picks and model filtering accept it.
func registerPoolBreakerGuardAuth(t *testing.T, manager *Manager, id, pool, model string) *Auth {
	t.Helper()
	auth := &Auth{
		ID:       id,
		Provider: "claude",
		Status:   StatusActive,
		Attributes: map[string]string{
			"provider_key":              pool,
			AttributeEntryProviderKey:   pool + ":key-71",
			AttributePoolStrategy:       "round-robin",
			AttributePoolCircuitBreaker: "true",
			AttributeAuthKind:           AuthKindAPIKey,
			AttributeAPIKey:             "k-" + id,
		},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register(%s) error = %v", id, errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	manager.RefreshSchedulerEntry(auth.ID)
	return auth
}

// openAndExpirePoolBreaker feeds threshold 503 failures through MarkResult
// (opening the breaker for pool) and backdates openedAt so the reset period
// has elapsed. The transient per-model cooldown the 503s stamped on the auth is
// reset so the breaker is the only thing gating selection reads.
func openAndExpirePoolBreaker(t *testing.T, manager *Manager, auth *Auth, pool string) {
	t.Helper()
	for i := 0; i < poolBreakerFailureThreshold; i++ {
		manager.MarkResult(context.Background(), transient503Result(auth.ID))
	}
	if state, _, ok := poolBreakerEntryState(pool); !ok || state != breakerOpen {
		t.Fatalf("expected breaker for %s to be open after threshold failures, ok=%v state=%v", pool, ok, state)
	}
	expirePoolBreakerOpen(t, pool)
	if _, _, errReset := manager.ResetQuota(context.Background(), auth.ID); errReset != nil {
		t.Fatalf("ResetQuota(%s) error = %v", auth.ID, errReset)
	}
}

// TestPoolBreakerGuard_PostExpiryProbeDispatches pins the fixed defect end to
// end on the non-stream loop: after the pool breaker OPENs and its reset
// period elapses, a request must survive pick, model filtering, AND the
// admission gate to actually dispatch. Before the fix the pick consumed the
// probe and the model-filter read then blocked, dropping the request before
// dispatch — an opened pool could never recover through a probe.
func TestPoolBreakerGuard_PostExpiryProbeDispatches(t *testing.T) {
	const (
		pool  = "claude:11"
		model = "claude-sonnet-4-5"
	)
	executor := &poolBreakerGuardExecutor{id: "claude"}
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.executors["claude"] = executor
	auth := registerPoolBreakerGuardAuth(t, manager, "auth-guard-execute", pool, model)
	cleanupPoolBreakerKeys(t, pool)
	openAndExpirePoolBreaker(t, manager, auth, pool)

	resp, errExecute := manager.Execute(context.Background(), []string{pool}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("Execute() after breaker expiry error = %v, want dispatch (probe must reach commit)", errExecute)
	}
	if got := executor.executeCalls(); len(got) != 1 || got[0] != model {
		t.Fatalf("executor dispatches = %v, want exactly [%s]", got, model)
	}
	if string(resp.Payload) != model {
		t.Fatalf("Execute payload = %q, want %q", string(resp.Payload), model)
	}
	// The dispatched probe succeeded through MarkResult: the pool closed.
	for _, record := range PoolBreakerSnapshot() {
		if record.PoolKey == pool {
			t.Fatalf("PoolBreakerSnapshot() still contains %s after probe success: %+v", pool, record)
		}
	}
}

// TestPoolBreakerGuard_StreamProbeDeniedRotatesWithoutDispatch pins the
// stream loop's admission gate on its mixed (non-home) branch: a request
// whose probe slot is stolen mid-flight (between pick and gate) must be
// dropped WITHOUT dispatching and must terminate with an error instead of
// spinning — while the pool's probe is protected by exactly one in-flight
// request. The home branch is covered separately by
// TestPoolBreakerGuard_HomeStreamProbeDeniedReleasesSelection.
func TestPoolBreakerGuard_StreamProbeDeniedRotatesWithoutDispatch(t *testing.T) {
	const (
		pool  = "claude:12"
		model = "claude-sonnet-4-5"
	)
	executor := &poolBreakerGuardExecutor{id: "claude"}
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.executors["claude"] = executor
	auth := registerPoolBreakerGuardAuth(t, manager, "auth-guard-stream", pool, model)
	cleanupPoolBreakerKeys(t, pool)
	openAndExpirePoolBreaker(t, manager, auth, pool)

	// Steal the probe between this request's pick and its gate, simulating a
	// concurrent winner. The prepare seam runs exactly there.
	executor.armProbeStealOnPrepare(pool)

	_, errStream := manager.ExecuteStream(context.Background(), []string{pool}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
	if errStream == nil {
		t.Fatal("ExecuteStream() error = nil, want an error (the probe is held; no candidate remains)")
	}
	if got := executor.streamCalls(); len(got) != 0 {
		t.Fatalf("executor stream dispatches = %v, want none (probe held elsewhere)", got)
	}
}

// TestPoolBreakerGuard_DenialRotatesToNextPool pins the loop's rotation
// bookkeeping: a probe denial must not burn the request while selectable
// candidates remain. Because the breaker is pool-keyed, the surviving
// candidate is an auth on ANOTHER pool whose breaker was never opened: the
// first route entry is denied at commit, and the loop must rotate to the
// second entry and dispatch there. The route strategy is "priority"
// (fill-first) so the denied entry is deterministically tried first.
func TestPoolBreakerGuard_DenialRotatesToNextPool(t *testing.T) {
	const (
		openedPool = "claude:13"
		otherPool  = "claude:15"
		model      = "claude-sonnet-4-5"
	)
	executor := &poolBreakerGuardExecutor{id: "claude"}
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.executors["claude"] = executor
	opened := registerPoolBreakerGuardAuth(t, manager, "auth-guard-rotate-a", openedPool, model)
	registerPoolBreakerGuardAuth(t, manager, "auth-guard-rotate-b", otherPool, model)
	cleanupPoolBreakerKeys(t, openedPool, otherPool)
	openAndExpirePoolBreaker(t, manager, opened, openedPool)

	// Steal the opened pool's probe between pick and gate. The denied pick
	// must rotate to the other pool's auth and dispatch there.
	executor.armProbeStealOnPrepare(openedPool)

	route := []string{openedPool, otherPool}
	opts := cliproxyexecutor.Options{Metadata: map[string]any{
		cliproxyexecutor.RouteStrategyMetadataKey: "priority",
	}}
	resp, errExecute := manager.Execute(context.Background(), route, cliproxyexecutor.Request{Model: model}, opts)
	if errExecute != nil {
		t.Fatalf("Execute() with probe stolen error = %v, want rotation to dispatch anyway", errExecute)
	}
	if got := executor.executeCalls(); len(got) != 1 {
		t.Fatalf("executor dispatches = %v, want exactly one (rotation past the denial)", got)
	}
	if string(resp.Payload) != model {
		t.Fatalf("Execute payload = %q, want %q", string(resp.Payload), model)
	}
	// The other pool must NOT have fed the breaker (only 408/5xx feed it).
	for _, record := range PoolBreakerSnapshot() {
		if record.PoolKey == otherPool {
			t.Fatalf("PoolBreakerSnapshot() unexpectedly contains %s: %+v", otherPool, record)
		}
	}
}

// poolBreakerGuardHomeDispatcher is a fixture Home dispatcher that offers one
// credential carrying pool attributes on the first dispatch (count=1) and
// reports exhaustion for later counts, mirroring how a real Home backend
// responds when a request asks for the Nth credential of a pool. Without the
// exhaustion response the stream loop would keep re-picking the same auth
// until the probe window expires.
type poolBreakerGuardHomeDispatcher struct {
	payload []byte
	calls   int
}

func (*poolBreakerGuardHomeDispatcher) HeartbeatOK() bool { return true }

func (d *poolBreakerGuardHomeDispatcher) RPopAuth(_ context.Context, _, _ string, _ http.Header, count int) ([]byte, error) {
	d.calls++
	if count > 1 {
		return []byte(`{"error":{"type":"auth_not_found","message":"no more credentials"}}`), nil
	}
	return d.payload, nil
}

func (*poolBreakerGuardHomeDispatcher) AbortAmbiguousDispatch() {}

// TestPoolBreakerGuard_HomeStreamProbeDeniedReleasesSelection pins the home
// branch of the stream loop's admission gate: when the probe slot is held,
// the home selection must be released (attempt release + end-with-release
// bookkeeping) before rotating, not leaked. The release is observable via the
// execution registry: after ExecuteStream returns, no in-flight execution may
// remain. Note: home dispatches never feed the breaker (they bypass
// MarkResult), so this exercises only the gate, not pool recovery.
func TestPoolBreakerGuard_HomeStreamProbeDeniedReleasesSelection(t *testing.T) {
	const (
		pool  = "claude:14"
		model = "claude-sonnet-4-5"
	)
	executor := &poolBreakerGuardExecutor{id: "claude"}
	homePayload := []byte(`{"auth":{"id":"auth-guard-home","provider":"claude","attributes":{"provider_key":"` + pool + `","` + AttributePoolCircuitBreaker + `":"true"}}}`)
	dispatcher := &poolBreakerGuardHomeDispatcher{payload: homePayload}
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&config.Config{Home: config.HomeConfig{Enabled: true}})
	manager.PublishHomeDispatch(dispatcher, executionregistry.New(), 1)
	manager.executors["claude"] = executor
	cleanupPoolBreakerKeys(t, pool)

	// Open the pool breaker directly (home dispatches bypass MarkResult) and
	// expire it, then steal the probe between pick and gate via the prepare
	// seam so the gate's home denial branch (release + rotate) runs.
	for i := 0; i < poolBreakerFailureThreshold; i++ {
		globalPoolBreaker.recordFailure(pool, time.Now())
	}
	expirePoolBreakerOpen(t, pool)
	executor.armProbeStealOnPrepare(pool)

	_, errStream := manager.ExecuteStream(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
	if errStream == nil {
		t.Fatal("ExecuteStream() error = nil, want an error (probe held; Home has no other credential)")
	}
	if got := executor.streamCalls(); len(got) != 0 {
		t.Fatalf("executor stream dispatches = %v, want none (probe held elsewhere)", got)
	}
	// The home selection was released: the registry must show no in-flight
	// execution once the request has returned.
	if freeze := manager.HomeDispatchBundle().registry.FreezeInFlight(time.Now()); len(freeze.Executions) != 0 {
		t.Fatalf("in-flight executions after probe denial = %d, want 0 (selection must be released, not leaked)", len(freeze.Executions))
	}
}
