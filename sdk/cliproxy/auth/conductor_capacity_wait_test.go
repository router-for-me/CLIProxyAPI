package auth

import (
	"context"
	"net/http"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// capacityWaitTestExecutor is the minimal ProviderExecutor for the
// wait-then-failover tests. It counts Execute calls so tests can assert that a
// successful re-pick after the capacity wait actually dispatched.
type capacityWaitTestExecutor struct {
	provider     string
	executeCalls atomic.Int32
}

func (e *capacityWaitTestExecutor) Identifier() string {
	if e.provider == "" {
		return "gemini"
	}
	return e.provider
}

func (e *capacityWaitTestExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.executeCalls.Add(1)
	return cliproxyexecutor.Response{Payload: []byte(auth.ID)}, nil
}

func (e *capacityWaitTestExecutor) ExecuteStream(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	ch := make(chan cliproxyexecutor.StreamChunk, 1)
	ch <- cliproxyexecutor.StreamChunk{Payload: []byte(auth.ID)}
	close(ch)
	return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
}

func (e *capacityWaitTestExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *capacityWaitTestExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *capacityWaitTestExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

// capacityWaitProbe records the waitForCapacity invocations (count + resolved
// budget) so tests stay deterministic without real 50ms/200ms sleeps. override
// substitutes the production sleep; when nil, the probe resolves true until
// ctx is canceled (a 1ms yield keeps the loop from hammer-spinning).
type capacityWaitProbe struct {
	override func(context.Context, time.Duration) bool
	waits    atomic.Int32
	maxWait  atomic.Int64
}

func (p *capacityWaitProbe) fn() func(context.Context, time.Duration) bool {
	return func(ctx context.Context, maxWait time.Duration) bool {
		p.waits.Add(1)
		p.maxWait.Store(int64(maxWait))
		if p.override != nil {
			return p.override(ctx, maxWait)
		}
		time.Sleep(time.Millisecond)
		select {
		case <-ctx.Done():
			return false
		default:
			return true
		}
	}
}

// capacityWaitManager builds a fully wired test manager: a round-robin
// scheduler over gemini auths registered for the model, a registered gemini
// executor, and the capacity wait seam installed. Registry/model cleanup is
// registered via t.Cleanup. Tests seed the scheduler's in-flight counters
// directly (same package) to drive the all-entries-over-cap pick.
func capacityWaitManager(t *testing.T, auths ...*Auth) (*Manager, string, *capacityWaitTestExecutor, *capacityWaitProbe) {
	t.Helper()
	model := "capacity-wait-model-" + uuid.NewString()
	ids := make([]string, 0, len(auths))
	for _, auth := range auths {
		ids = append(ids, auth.ID)
	}
	registerSchedulerModels(t, "gemini", model, ids...)
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.SetRetryConfig(0, 0, 0)
	executor := &capacityWaitTestExecutor{provider: "gemini"}
	manager.RegisterExecutor(executor)
	for _, auth := range auths {
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", auth.ID, errRegister)
		}
	}
	probe := &capacityWaitProbe{}
	manager.capacityWaitFn = probe.fn()
	return manager, model, executor, probe
}

// capacityAuthForWait is capacityAuth (max_parallel) plus the max_wait_ms
// attribute the wait budget resolver reads. maxWait <= 0 leaves the attribute
// unset (resolves to DefaultCapacityWaitMS).
func capacityAuthForWait(id string, maxParallel, maxWait int) *Auth {
	auth := capacityAuth(id, maxParallel)
	if maxWait > 0 {
		auth.Attributes[AttributeMaxWaitMs] = strconv.Itoa(maxWait)
	}
	return auth
}

// TestExecuteMixedOnce_WaitsThenRepicksOnCapacity pins the core Task-5
// behavior: the picker reports ErrEntryCapacity once (every candidate over its
// max_concurrent cap), the execute loop waits within the entry's max_wait_ms
// budget, then re-picks and returns the successful execution. The wait seam
// frees the second entry's in-flight slot between picks, so the second pick
// genuinely succeeds through the real scheduler.
func TestExecuteMixedOnce_WaitsThenRepicksOnCapacity(t *testing.T) {
	ctx := context.Background()
	authA := capacityAuthForWait("cap-wait-a", 1, 100)
	authB := capacityAuthForWait("cap-wait-b", 1, 100)
	manager, model, executor, probe := capacityWaitManager(t, authA, authB)
	manager.scheduler.adjustInFlight(authA.ID, 1)
	manager.scheduler.adjustInFlight(authB.ID, 1)
	probe.override = func(ctx context.Context, _ time.Duration) bool {
		manager.scheduler.adjustInFlight(authB.ID, -1) // free a slot during the wait
		return true
	}

	resp, errExec := manager.Execute(ctx, []string{"gemini"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExec != nil {
		t.Fatalf("Execute() error = %v, want success after capacity wait + re-pick", errExec)
	}
	if string(resp.Payload) != authB.ID {
		t.Fatalf("Execute() served auth %q, want %q (the entry freed during the wait)", string(resp.Payload), authB.ID)
	}
	if got := probe.waits.Load(); got != 1 {
		t.Fatalf("waitForCapacity invocations = %d, want 1 (one capacity pick, waited once)", got)
	}
	if got := executor.executeCalls.Load(); got != 1 {
		t.Fatalf("executor.Execute calls = %d, want 1 (single dispatch after wait)", got)
	}
}

// TestExecuteMixedOnce_TwoCapacityPicksWaitTwice covers the repeated-capacity
// path: each capacity signal consumes one wait before the next re-pick
// succeeds within the remaining budget.
func TestExecuteMixedOnce_TwoCapacityPicksWaitTwice(t *testing.T) {
	ctx := context.Background()
	authA := capacityAuthForWait("cap-wait-two-a", 1, 100)
	authB := capacityAuthForWait("cap-wait-two-b", 1, 100)
	manager, model, _, probe := capacityWaitManager(t, authA, authB)
	manager.scheduler.adjustInFlight(authA.ID, 1)
	manager.scheduler.adjustInFlight(authB.ID, 1)

	waits := &atomic.Int32{}
	probe.override = func(ctx context.Context, _ time.Duration) bool {
		if patience := waits.Add(1); patience == 2 {
			manager.scheduler.adjustInFlight(authB.ID, -1) // free a slot on the second wait
		}
		return true
	}

	_, errExec := manager.Execute(ctx, []string{"gemini"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExec != nil {
		t.Fatalf("Execute() error = %v, want success after two capacity waits", errExec)
	}
	if got := probe.waits.Load(); got != 2 {
		t.Fatalf("waitForCapacity invocations = %d, want 2", got)
	}
}

// TestExecuteMixedOnce_BudgetExpiryReturnsCapacity pins the budget-expiry
// branch: every re-pick reports capacity and the probe resolves the wait as
// exhausted (false), so executeMixedOnce falls through to the existing error
// return unchanged — the caller sees the entry_capacity sentinel (D6), still
// HTTP 503.
func TestExecuteMixedOnce_BudgetExpiryReturnsCapacity(t *testing.T) {
	ctx := context.Background()
	authA := capacityAuthForWait("cap-wait-expire", 1, 20)
	manager, model, _, probe := capacityWaitManager(t, authA)
	manager.scheduler.adjustInFlight(authA.ID, 1)
	probe.override = func(context.Context, time.Duration) bool { return false }

	_, errExec := manager.Execute(ctx, []string{"gemini"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExec == nil {
		t.Fatal("Execute() error = nil, want entry_capacity after budget expiry")
	}
	if !isEntryCapacityError(errExec) {
		t.Fatalf("Execute() error = %v, want ErrEntryCapacity sentinel (D6)", errExec)
	}
	if statusCodeFromError(errExec) != http.StatusServiceUnavailable {
		t.Fatalf("Execute() status = %d, want 503 (D6 keeps the no-auth shape)", statusCodeFromError(errExec))
	}
	if got := probe.waits.Load(); got != 1 {
		t.Fatalf("waitForCapacity invocations = %d, want 1 (one bounded wait before falling through)", got)
	}
}

// TestExecuteCountMixedOnce_WaitsThenRepicksOnCapacity mirrors the
// non-streaming case through ExecuteCount, which drives executeCountMixedOnce —
// the second insertion point required by the plan.
func TestExecuteCountMixedOnce_WaitsThenRepicksOnCapacity(t *testing.T) {
	ctx := context.Background()
	authA := capacityAuthForWait("cap-count-wait-a", 1, 100)
	authB := capacityAuthForWait("cap-count-wait-b", 1, 100)
	manager, model, _, probe := capacityWaitManager(t, authA, authB)
	manager.scheduler.adjustInFlight(authA.ID, 1)
	manager.scheduler.adjustInFlight(authB.ID, 1)
	probe.override = func(ctx context.Context, _ time.Duration) bool {
		manager.scheduler.adjustInFlight(authB.ID, -1)
		return true
	}

	_, errCount := manager.ExecuteCount(ctx, []string{"gemini"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errCount != nil {
		t.Fatalf("ExecuteCount() error = %v, want success after capacity wait + re-pick", errCount)
	}
	if got := probe.waits.Load(); got != 1 {
		t.Fatalf("waitForCapacity invocations = %d, want 1", got)
	}
}

// TestExecuteStreamMixedOnce_WaitsThenRepicksOnCapacity mirrors the bounded
// wait on the streaming picked-nothing path (executeStreamMixedOnce, the third
// insertion point): streaming has the same capacity condition and should not
// fail faster than non-streaming.
func TestExecuteStreamMixedOnce_WaitsThenRepicksOnCapacity(t *testing.T) {
	ctx := context.Background()
	authA := capacityAuthForWait("cap-stream-wait-a", 1, 100)
	authB := capacityAuthForWait("cap-stream-wait-b", 1, 100)
	manager, model, _, probe := capacityWaitManager(t, authA, authB)
	manager.scheduler.adjustInFlight(authA.ID, 1)
	manager.scheduler.adjustInFlight(authB.ID, 1)
	probe.override = func(ctx context.Context, _ time.Duration) bool {
		manager.scheduler.adjustInFlight(authB.ID, -1)
		return true
	}

	stream, errStream := manager.ExecuteStream(ctx, []string{"gemini"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v, want success after capacity wait + re-pick", errStream)
	}
	var payload []byte
	for chunk := range stream.Chunks {
		if chunk.Err == nil {
			payload = chunk.Payload
		}
	}
	if string(payload) != authB.ID {
		t.Fatalf("ExecuteStream() served auth %q, want %q", string(payload), authB.ID)
	}
	if got := probe.waits.Load(); got != 1 {
		t.Fatalf("waitForCapacity invocations = %d, want 1", got)
	}
}

// TestExecuteMixedOnce_MaxWaitUnsetUsesDefault verifies the "no instant
// re-pick on capacity" rule from the plan: an entry with max_wait_ms unset
// resolves the bounded default (DefaultCapacityWaitMS) instead of busy-spinning.
// The wait seam records the resolved budget so no real 200ms sleep is needed.
func TestExecuteMixedOnce_MaxWaitUnsetUsesDefault(t *testing.T) {
	ctx := context.Background()
	authA := capacityAuth("cap-wait-default-a", 1) // no max_wait_ms attribute
	authB := capacityAuth("cap-wait-default-b", 1)
	manager, model, _, probe := capacityWaitManager(t, authA, authB)
	manager.scheduler.adjustInFlight(authA.ID, 1)
	manager.scheduler.adjustInFlight(authB.ID, 1)
	probe.override = func(ctx context.Context, _ time.Duration) bool {
		manager.scheduler.adjustInFlight(authB.ID, -1)
		return true
	}

	_, errExec := manager.Execute(ctx, []string{"gemini"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExec != nil {
		t.Fatalf("Execute() error = %v, want success after default-budget wait", errExec)
	}
	if got := probe.waits.Load(); got != 1 {
		t.Fatalf("waitForCapacity invocations = %d, want 1", got)
	}
	got := time.Duration(probe.maxWait.Load())
	if want := time.Duration(DefaultCapacityWaitMS) * time.Millisecond; got != want {
		t.Fatalf("resolved capacity wait = %v, want the DefaultCapacityWaitMS (%v)", got, want)
	}
}

// TestExecuteMixedOnce_CtxCanceledDuringWait returns promptly with the pick
// error: the wait loop must honor cancellation and never hang. A canceled
// context during the wait resolves false (budget abandoned), so the request
// falls through to the normal error return — no busy wait, no hang.
func TestExecuteMixedOnce_CtxCanceledDuringWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	authA := capacityAuthForWait("cap-wait-cancel", 1, 20)
	manager, model, _, _ := capacityWaitManager(t, authA)
	manager.scheduler.adjustInFlight(authA.ID, 1)

	done := make(chan error, 1)
	go func() {
		_, errExec := manager.Execute(ctx, []string{"gemini"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
		done <- errExec
	}()
	// Cancel once the request is guaranteed to be inside its capacity wait
	// (the seam yields 1ms per pass, so a short sleep leaves it spinning).
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case errExec := <-done:
		if errExec == nil {
			t.Fatal("Execute() error = nil after ctx cancellation during the capacity wait")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Execute() hung after ctx cancellation during the capacity wait")
	}
}

// TestExecuteMixedOnce_UncappedEntrySkipsWait pins the no-regression case:
// when no entry has a max_concurrent cap, the picker never returns
// ErrEntryCapacity, so the wait must never trigger.
func TestExecuteMixedOnce_UncappedEntrySkipsWait(t *testing.T) {
	ctx := context.Background()
	authA := &Auth{ID: "cap-wait-uncapped", Provider: "gemini"} // no max_parallel attribute
	manager, model, executor, probe := capacityWaitManager(t, authA)

	_, errExec := manager.Execute(ctx, []string{"gemini"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExec != nil {
		t.Fatalf("Execute() error = %v, want success for uncapped entry", errExec)
	}
	if got := probe.waits.Load(); got != 0 {
		t.Fatalf("waitForCapacity invocations = %d, want 0 for uncapped entries", got)
	}
	if got := executor.executeCalls.Load(); got != 1 {
		t.Fatalf("executor.Execute calls = %d, want 1", got)
	}
}

// TestMaxWaitForAuth_ResolvesEntryBudget pins the budget resolver: the FIRST
// candidate auth carrying a positive max_wait_ms wins; nil candidates and
// unset/0 values fall back to DefaultCapacityWaitMS (never zero/crash).
func TestMaxWaitForAuth_ResolvesEntryBudget(t *testing.T) {
	defaultWait := time.Duration(DefaultCapacityWaitMS) * time.Millisecond

	if got := maxWaitForAuth(capacityAuthForWait("a", 1, 300)); got != 300*time.Millisecond {
		t.Fatalf("maxWaitForAuth(set budget) = %v, want 300ms", got)
	}
	if got := maxWaitForAuth(capacityAuth("b", 1)); got != defaultWait {
		t.Fatalf("maxWaitForAuth(unset) = %v, want default %v", got, defaultWait)
	}
	if got := maxWaitForAuth(nil); got != defaultWait {
		t.Fatalf("maxWaitForAuth(nil) = %v, want default %v", got, defaultWait)
	}
	if got := maxWaitForAuth(nil, capacityAuthForWait("c", 1, 700)); got != 700*time.Millisecond {
		t.Fatalf("maxWaitForAuth(first non-nil candidate) = %v, want 700ms", got)
	}
	if got := maxWaitForAuth(capacityAuth("d", 1), capacityAuthForWait("e", 1, 500)); got != 500*time.Millisecond {
		t.Fatalf("maxWaitForAuth(skip zero budget) = %v, want 500ms", got)
	}
}
