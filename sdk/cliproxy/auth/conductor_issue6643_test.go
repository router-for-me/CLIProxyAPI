package auth

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// issue6643Result records one client-visible outcome of the concurrent test.
type issue6643Result struct {
	err error
}

// issue6643Hook releases the blocked in-flight requests once the quota
// failure has been fully recorded by the manager.
type issue6643Hook struct {
	onResult func(context.Context, Result)
}

func (*issue6643Hook) OnAuthRegistered(context.Context, *Auth) {}

func (*issue6643Hook) OnAuthUpdated(context.Context, *Auth) {}

func (h *issue6643Hook) OnResult(ctx context.Context, result Result) {
	if h.onResult != nil {
		h.onResult(ctx, result)
	}
}

// issue6643ConcurrentExecutor models the upstream from issue 6643: many
// requests run concurrently on one credential, one of the later dispatches is
// answered with a quota-exhausted 429, and only afterwards do the requests
// dispatched earlier complete successfully.
type issue6643ConcurrentExecutor struct {
	exhausted            atomic.Bool
	dispatched           atomic.Int64
	allDispatched        chan struct{}
	quotaResultRecorded  chan struct{}
	callsAfterExhaustion atomic.Int64
	closeDispatchedOnce  sync.Once
}

const issue6643ConcurrentRequests = 6

func (*issue6643ConcurrentExecutor) Identifier() string { return "antigravity" }

func (e *issue6643ConcurrentExecutor) Execute(ctx context.Context, _ *Auth, req cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if e.exhausted.Load() {
		// A dispatch reaching the already exhausted credential. The proxy
		// could have avoided this upstream call entirely.
		e.callsAfterExhaustion.Add(1)
		return cliproxyexecutor.Response{}, &Error{Code: "quota_exhausted", Message: "QUOTA_EXHAUSTED: Resets in 140h36m", Retryable: true, HTTPStatus: http.StatusTooManyRequests}
	}
	call := e.dispatched.Add(1)
	if call < issue6643ConcurrentRequests {
		// Long-running requests dispatched before the quota error: they only
		// complete, successfully, once the 429 result has been recorded.
		select {
		case <-e.quotaResultRecorded:
			return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
		case <-ctx.Done():
			return cliproxyexecutor.Response{}, ctx.Err()
		}
	}
	// The last dispatch observes the quota exhausting right now.
	e.exhausted.Store(true)
	e.closeDispatchedOnce.Do(func() { close(e.allDispatched) })
	return cliproxyexecutor.Response{}, &Error{Code: "quota_exhausted", Message: "QUOTA_EXHAUSTED: Resets in 140h36m", Retryable: true, HTTPStatus: http.StatusTooManyRequests}
}

func (e *issue6643ConcurrentExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "not implemented"}
}

func (*issue6643ConcurrentExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (*issue6643ConcurrentExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusNotImplemented, Message: "not implemented"}
}

func (*issue6643ConcurrentExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "not implemented"}
}

// issue6643SeqTestAuth registers a single auth for the MarkResult-level
// watermark tests and returns the manager.
func issue6643SeqTestAuth(t *testing.T, authID string) *Manager {
	t.Helper()
	withQuotaCooldownEnabled(t)
	manager := NewManager(nil, nil, nil)
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), &Auth{
		ID:       authID,
		Provider: "antigravity",
		Status:   StatusActive,
	}); errRegister != nil {
		t.Fatalf("Register returned error: %v", errRegister)
	}
	return manager
}

func issue6643QuotaFailure(authID, model string, seq uint64) Result {
	return Result{
		AuthID:       authID,
		Provider:     "antigravity",
		Model:        model,
		Success:      false,
		ExecutionSeq: seq,
		Error:        &Error{Code: "quota_exhausted", Message: "QUOTA_EXHAUSTED", Retryable: true, HTTPStatus: http.StatusTooManyRequests},
	}
}

func issue6643ModelState(t *testing.T, manager *Manager, authID, model string) *ModelState {
	t.Helper()
	updated, ok := manager.GetByID(authID)
	if !ok || updated == nil || updated.ModelStates[model] == nil {
		t.Fatalf("expected model state for %q", model)
	}
	return updated.ModelStates[model]
}

// TestManager_Issue6643_SuccessDispatchedAfterCooldownClearsIt pins the
// positive half of the watermark rule: only a success stamped after the
// cooldown was recorded proves recovery and clears it.
func TestManager_Issue6643_SuccessDispatchedAfterCooldownClearsIt(t *testing.T) {
	const (
		authID = "issue6643-after-auth"
		model  = "gemini-4-argon-high"
	)
	manager := issue6643SeqTestAuth(t, authID)

	// A long-running request is dispatched first, then a later dispatch hits
	// the quota limit and records the cooldown.
	staleSeq := manager.nextExecutionSeq()
	manager.MarkResult(context.Background(), issue6643QuotaFailure(authID, model, manager.nextExecutionSeq()))
	if state := issue6643ModelState(t, manager, authID, model); !state.Quota.Exceeded {
		t.Fatalf("expected quota cooldown after failure, got %+v", state)
	}

	// The earlier dispatch now completes successfully. It predates the
	// cooldown, so the cooldown must survive.
	manager.MarkResult(context.Background(), Result{AuthID: authID, Provider: "antigravity", Model: model, Success: true, ExecutionSeq: staleSeq})
	if state := issue6643ModelState(t, manager, authID, model); !state.Unavailable || !state.Quota.Exceeded || !state.NextRetryAfter.After(time.Now()) {
		t.Fatalf("stale success cleared the quota cooldown: state = %+v", state)
	}

	// A request dispatched after the cooldown was recorded (for example a
	// probe just before the reset boundary) succeeds and clears it.
	manager.MarkResult(context.Background(), Result{AuthID: authID, Provider: "antigravity", Model: model, Success: true, ExecutionSeq: manager.nextExecutionSeq()})
	if state := issue6643ModelState(t, manager, authID, model); state.Unavailable || state.Quota.Exceeded || !state.NextRetryAfter.IsZero() {
		t.Fatalf("post-cooldown success did not clear the cooldown: state = %+v", state)
	}
}

// TestManager_Issue6643_UnstampedSuccessKeepsLegacyClearBehavior pins the
// compatibility rule: results without a dispatch stamp (direct MarkResult
// callers, prepare-stage errors, or a zero watermark after restart) behave
// exactly as before the ordering guard existed.
func TestManager_Issue6643_UnstampedSuccessKeepsLegacyClearBehavior(t *testing.T) {
	const (
		authID = "issue6643-unstamped-auth"
		model  = "gemini-4-argon-high"
	)
	manager := issue6643SeqTestAuth(t, authID)

	manager.MarkResult(context.Background(), issue6643QuotaFailure(authID, model, manager.nextExecutionSeq()))
	if state := issue6643ModelState(t, manager, authID, model); !state.Quota.Exceeded {
		t.Fatalf("expected quota cooldown after failure, got %+v", state)
	}

	// An unstamped success cannot be ordered against the cooldown, so it
	// still clears it like any legacy caller would.
	manager.MarkResult(context.Background(), Result{AuthID: authID, Provider: "antigravity", Model: model, Success: true})
	if state := issue6643ModelState(t, manager, authID, model); state.Unavailable || state.Quota.Exceeded {
		t.Fatalf("unstamped success should clear the cooldown: state = %+v", state)
	}
}

// TestManager_Issue6643_StaleAuthLevelSuccessKeepsCooldown covers the
// back door: a success without a model key takes the auth-level clear path
// and must not wipe an auth-level cooldown recorded after it was dispatched.
func TestManager_Issue6643_StaleAuthLevelSuccessKeepsCooldown(t *testing.T) {
	const authID = "issue6643-auth-level-auth"
	manager := issue6643SeqTestAuth(t, authID)

	staleSeq := manager.nextExecutionSeq()
	manager.MarkResult(context.Background(), Result{
		AuthID:       authID,
		Provider:     "antigravity",
		Success:      false,
		ExecutionSeq: manager.nextExecutionSeq(),
		Error:        &Error{Code: "quota_exhausted", Message: "QUOTA_EXHAUSTED", Retryable: true, HTTPStatus: http.StatusTooManyRequests},
	})
	updated, ok := manager.GetByID(authID)
	if !ok || updated == nil || !updated.Quota.Exceeded || !updated.NextRetryAfter.After(time.Now()) {
		t.Fatalf("expected auth-level quota cooldown after failure, got %+v", updated)
	}

	manager.MarkResult(context.Background(), Result{AuthID: authID, Provider: "antigravity", Success: true, ExecutionSeq: staleSeq})
	updated, ok = manager.GetByID(authID)
	if !ok || updated == nil || !updated.Quota.Exceeded || !updated.NextRetryAfter.After(time.Now()) {
		t.Fatalf("stale auth-level success cleared the cooldown: auth = %+v", updated)
	}

	manager.MarkResult(context.Background(), Result{AuthID: authID, Provider: "antigravity", Success: true, ExecutionSeq: manager.nextExecutionSeq()})
	updated, ok = manager.GetByID(authID)
	if !ok || updated == nil || updated.Quota.Exceeded || !updated.NextRetryAfter.IsZero() {
		t.Fatalf("post-cooldown auth-level success did not clear the cooldown: auth = %+v", updated)
	}
}

// TestManager_Issue6643_RestoreKeepsCooldownWatermark covers the runtime
// restore path: configuration updates re-apply persisted cooldown records to
// live auths, and that merge must not drop the watermark protecting a fresh
// cooldown from stale in-flight successes.
func TestManager_Issue6643_RestoreKeepsCooldownWatermark(t *testing.T) {
	const (
		authID = "issue6643-restore-auth"
		model  = "gemini-4-argon-high"
	)
	manager := issue6643SeqTestAuth(t, authID)
	store := &recordingCooldownStateStore{}
	manager.SetCooldownStateStore(store)

	staleSeq := manager.nextExecutionSeq()
	manager.MarkResult(context.Background(), issue6643QuotaFailure(authID, model, manager.nextExecutionSeq()))
	before := issue6643ModelState(t, manager, authID, model)
	if !before.Quota.Exceeded {
		t.Fatalf("expected quota cooldown after failure, got %+v", before)
	}
	store.load = store.savedRecords()
	if len(store.load) == 0 {
		t.Fatal("expected the quota cooldown to be persisted for restore")
	}

	// A runtime configuration update restores the persisted cooldown into
	// the live auth while the earlier dispatch is still in flight.
	if errRestore := manager.RestoreCooldownStates(context.Background()); errRestore != nil {
		t.Fatalf("RestoreCooldownStates returned error: %v", errRestore)
	}

	manager.MarkResult(context.Background(), Result{AuthID: authID, Provider: "antigravity", Model: model, Success: true, ExecutionSeq: staleSeq})
	if state := issue6643ModelState(t, manager, authID, model); !state.Unavailable || !state.Quota.Exceeded || !state.NextRetryAfter.After(time.Now()) {
		t.Fatalf("stale success cleared the cooldown after runtime restore: state = %+v", state)
	}
}

// TestManager_Issue6643_InFlightSuccessesKeepFreshQuotaCooldown reproduces
// issue 6643 end to end: successes from requests dispatched before a quota
// cooldown was recorded must not clear that cooldown, so the exhausted
// credential stops being selected until the cooldown expires.
func TestManager_Issue6643_InFlightSuccessesKeepFreshQuotaCooldown(t *testing.T) {
	withQuotaCooldownEnabled(t)

	const (
		authID = "issue6643-auth"
		model  = "gemini-4-argon-high"
	)
	registry.GetGlobalRegistry().RegisterClient(authID, "antigravity", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })

	executor := &issue6643ConcurrentExecutor{
		allDispatched:       make(chan struct{}),
		quotaResultRecorded: make(chan struct{}),
	}
	var recordedOnce sync.Once
	hook := &issue6643Hook{onResult: func(_ context.Context, result Result) {
		if !result.Success && result.Error != nil && result.Error.HTTPStatus == http.StatusTooManyRequests {
			// OnResult fires after the cooldown state transition, so the
			// blocked in-flight requests are released only once the quota
			// cooldown is fully recorded.
			recordedOnce.Do(func() { close(executor.quotaResultRecorded) })
		}
	}}
	manager := NewManager(nil, &RoundRobinSelector{}, hook)
	manager.RegisterExecutor(executor)
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), &Auth{
		ID:       authID,
		Provider: "antigravity",
		Status:   StatusActive,
	}); errRegister != nil {
		t.Fatalf("Register returned error: %v", errRegister)
	}

	execCtx, cancelExec := context.WithCancel(context.Background())
	t.Cleanup(cancelExec)
	results := make(chan issue6643Result, issue6643ConcurrentRequests)
	for i := 0; i < issue6643ConcurrentRequests; i++ {
		go func() {
			_, errExecute := manager.Execute(execCtx, []string{"antigravity"}, cliproxyexecutor.Request{Model: model, Payload: []byte("ping")}, cliproxyexecutor.Options{})
			results <- issue6643Result{err: errExecute}
		}()
	}

	// Every dispatched request must actually complete; a timeout here means
	// the run never proved the stale successes were all recorded.
	failures := 0
	for i := 0; i < issue6643ConcurrentRequests; i++ {
		select {
		case outcome := <-results:
			if outcome.err != nil {
				failures++
			}
		case <-time.After(30 * time.Second):
			cancelExec()
			t.Fatalf("timed out waiting for concurrent result %d of %d", i+1, issue6643ConcurrentRequests)
		}
	}
	// The client sees exactly one failure: the dispatch that hit the quota
	// limit. Internal retries must find the only credential cooling down
	// rather than reaching upstream again, and the earlier dispatches succeed.
	if failures != 1 {
		t.Fatalf("concurrent failures = %d, want 1", failures)
	}
	if got := executor.callsAfterExhaustion.Load(); got != 0 {
		t.Fatalf("upstream calls after quota exhaustion = %d, want 0", got)
	}

	// The quota cooldown recorded by the 429 survives the successful
	// completions of every request dispatched before it, so the exhausted
	// credential stays excluded from selection.
	updated, ok := manager.GetByID(authID)
	if !ok || updated == nil {
		t.Fatal("expected auth after concurrent run")
	}
	state := updated.ModelStates[model]
	if state == nil {
		t.Fatalf("expected model state for %q", model)
	}
	if !state.Unavailable || !state.Quota.Exceeded || !state.NextRetryAfter.After(time.Now()) {
		t.Fatalf("quota cooldown was cleared by in-flight successes: state = %+v", state)
	}
	if blocked, _, _ := isAuthBlockedForModel(updated, model, time.Now()); !blocked {
		t.Fatal("exhausted credential is selectable again during its quota cooldown")
	}
}
