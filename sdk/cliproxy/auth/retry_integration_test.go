package auth

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// ---------------------------------------------------------------------------
// Builder: routing.retry config -> InnerLoopOpts
// ---------------------------------------------------------------------------

func TestInnerLoopOptsFromConfig(t *testing.T) {
	t.Run("nil config disables retry", func(t *testing.T) {
		if got := innerLoopOptsFromConfig(nil); got != (InnerLoopOpts{}) {
			t.Fatalf("innerLoopOptsFromConfig(nil) = %+v, want zero opts", got)
		}
	})
	t.Run("zero config disables retry", func(t *testing.T) {
		cfg := &internalconfig.Config{}
		if got := innerLoopOptsFromConfig(cfg); got != (InnerLoopOpts{}) {
			t.Fatalf("innerLoopOptsFromConfig(zero cfg) = %+v, want zero opts", got)
		}
	})
	t.Run("passthrough", func(t *testing.T) {
		cfg := &internalconfig.Config{}
		cfg.Routing.Retry = internalconfig.RetryConfig{MaxAttempts: 3, MaxTimeMS: 5000, BackoffMS: 200}
		want := InnerLoopOpts{MaxAttempts: 3, MaxTimeMS: 5000, BackoffMS: 200}
		if got := innerLoopOptsFromConfig(cfg); got != want {
			t.Fatalf("innerLoopOptsFromConfig() = %+v, want %+v", got, want)
		}
	})
}

// ---------------------------------------------------------------------------
// RunInnerLoop parity: exercises the already-shipped helper the conductor
// wiring now drives, so the wrapper tests below can rely on its contract.
// ---------------------------------------------------------------------------

func TestRunInnerLoopDisabledOptsSingleNonTransientAttempt(t *testing.T) {
	var calls int32
	fn := func(context.Context) InnerAttemptResult {
		calls++
		// A plain error (not *net.OpError) is a non-transient failure.
		return InnerAttemptResult{Err: errors.New("connection refused")}
	}
	res := RunInnerLoop(context.Background(), InnerLoopOpts{}, fn)
	if calls != 1 {
		t.Fatalf("calls = %d, want exactly 1 (zero opts = one attempt)", calls)
	}
	if res.Reason != ReasonNonTransient {
		t.Fatalf("Reason = %q, want %q", res.Reason, ReasonNonTransient)
	}
	if res.Attempts != 1 {
		t.Fatalf("Attempts = %d, want 1", res.Attempts)
	}
}

func TestRunInnerLoopEnabledRetryReachesSuccessOnThirdAttempt(t *testing.T) {
	var calls int32
	fn := func(context.Context) InnerAttemptResult {
		calls++
		if calls < 3 {
			// A *net.OpError is a retryable transport failure.
			return InnerAttemptResult{Err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("connection refused")}}
		}
		// The loop counts success ONLY as Err==nil && 2xx status: a bare
		// InnerAttemptResult{} (Status 0) can never be ReasonSuccess.
		return InnerAttemptResult{Status: 200}
	}
	res := RunInnerLoop(context.Background(), InnerLoopOpts{MaxAttempts: 3, BackoffMS: 1}, fn)
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
	if res.Reason != ReasonSuccess {
		t.Fatalf("Reason = %q, want %q", res.Reason, ReasonSuccess)
	}
	if res.Attempts != 3 {
		t.Fatalf("Attempts = %d, want 3", res.Attempts)
	}
}

// ---------------------------------------------------------------------------
// End-to-end harness through the real conductor loops.
// ---------------------------------------------------------------------------

// retryTestExecutor records how often the conductor called it and whether
// the context it received carried a deadline (the AGENTS.md guard signal:
// the default, retry-disabled path must never stamp one).
type retryTestExecutor struct {
	executeFn func(context.Context, *Auth) (cliproxyexecutor.Response, error)
	countFn   func(context.Context, *Auth) (cliproxyexecutor.Response, error)

	executeCalls atomic.Int32
	countCalls   atomic.Int32
	deadlineSeen atomic.Bool
}

func (*retryTestExecutor) Identifier() string { return "claude" }

func (e *retryTestExecutor) Execute(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.recordDeadline(ctx)
	e.executeCalls.Add(1)
	if e.executeFn != nil {
		return e.executeFn(ctx, auth)
	}
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}

func (e *retryTestExecutor) CountTokens(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.recordDeadline(ctx)
	e.countCalls.Add(1)
	if e.countFn != nil {
		return e.countFn(ctx, auth)
	}
	return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
}

func (*retryTestExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, errors.New("streaming not used")
}

func (*retryTestExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (*retryTestExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func (e *retryTestExecutor) recordDeadline(ctx context.Context) {
	if _, ok := ctx.Deadline(); ok {
		e.deadlineSeen.Store(true)
	}
}

// retryIntegrationStatusError is a custom error that carries an HTTP status
// via StatusCode() but implements none of the request-scoped markers.
type retryIntegrationStatusError struct{ status int }

func (e *retryIntegrationStatusError) Error() string   { return "custom retry status error" }
func (e *retryIntegrationStatusError) StatusCode() int { return e.status }

func newRetryIntegrationManager(t *testing.T, executor *retryTestExecutor, hook Hook, retry internalconfig.RetryConfig) (*Manager, *Auth, string) {
	t.Helper()
	if hook == nil {
		hook = NoopHook{}
	}
	model := "claude-retry-model-" + uuid.NewString()
	auth := &Auth{
		ID:         "claude-retry-auth-" + uuid.NewString(),
		Provider:   "claude",
		Attributes: map[string]string{"auth_kind": "api_key"},
		Metadata: map[string]any{
			"access_token":  "access-token",
			"request_retry": float64(0),
		},
	}
	manager := NewManager(nil, nil, hook)
	// Zero the rotation/candidate caps so pool rotation cannot confound the
	// per-entry attempt counts asserted below.
	manager.SetRetryConfig(0, 0, 0)
	manager.RegisterExecutor(executor)
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	cfg := &internalconfig.Config{}
	cfg.Routing.Retry = retry
	manager.SetConfigSnapshot(cfg)
	return manager, auth, model
}

// TestRetryWiringExecuteDefaultSingleAttemptNoDeadline is the AGENTS.md
// guard: with no routing.retry config the conductor calls the executor
// exactly once and passes it the unmodified execCtx (no deadline).
func TestRetryWiringExecuteDefaultSingleAttemptNoDeadline(t *testing.T) {
	executor := &retryTestExecutor{
		executeFn: func(context.Context, *Auth) (cliproxyexecutor.Response, error) {
			return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "upstream down"}
		},
	}
	manager, _, model := newRetryIntegrationManager(t, executor, nil, internalconfig.RetryConfig{})

	_, errExecute := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if statusCodeFromError(errExecute) != http.StatusServiceUnavailable {
		t.Fatalf("Execute() error = %v, want HTTP 503", errExecute)
	}
	if got := executor.executeCalls.Load(); got != 1 {
		t.Fatalf("Execute calls = %d, want exactly 1 (retry disabled)", got)
	}
	if executor.deadlineSeen.Load() {
		t.Fatal("default path stamped a deadline on the execution context; the retry-disabled path must pass execCtx unchanged")
	}
}

// TestRetryWiringExecuteMaxAttemptsOneStaysInline pins the guard boundary:
// MaxAttempts == 1 is below the activation threshold and behaves exactly
// like the disabled path (one call, no deadline).
func TestRetryWiringExecuteMaxAttemptsOneStaysInline(t *testing.T) {
	executor := &retryTestExecutor{
		executeFn: func(context.Context, *Auth) (cliproxyexecutor.Response, error) {
			return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "upstream down"}
		},
	}
	retry := internalconfig.RetryConfig{MaxAttempts: 1, MaxTimeMS: 5000, BackoffMS: 1}
	manager, _, model := newRetryIntegrationManager(t, executor, nil, retry)

	_, errExecute := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if statusCodeFromError(errExecute) != http.StatusServiceUnavailable {
		t.Fatalf("Execute() error = %v, want HTTP 503", errExecute)
	}
	if got := executor.executeCalls.Load(); got != 1 {
		t.Fatalf("Execute calls = %d, want exactly 1 (MaxAttempts=1 runs the inline path)", got)
	}
	if executor.deadlineSeen.Load() {
		t.Fatal("MaxAttempts=1 stamped a deadline on the execution context; the inline path must pass execCtx unchanged")
	}
}

// TestRetryWiringExecuteRetriesTransientStatus drives the wired loop: a
// retryable 503 on attempt one, success on attempt two, inside ONE
// credential iteration (no pool rotation involved).
func TestRetryWiringExecuteRetriesTransientStatus(t *testing.T) {
	var calls atomic.Int32
	executor := &retryTestExecutor{
		executeFn: func(context.Context, *Auth) (cliproxyexecutor.Response, error) {
			if calls.Add(1) == 1 {
				return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "upstream down"}
			}
			return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
		},
	}
	retry := internalconfig.RetryConfig{MaxAttempts: 3, MaxTimeMS: 5000, BackoffMS: 1}
	manager, _, model := newRetryIntegrationManager(t, executor, nil, retry)

	resp, errExecute := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errExecute != nil {
		t.Fatalf("Execute() error = %v, want success after one retry", errExecute)
	}
	if string(resp.Payload) != "ok" {
		t.Fatalf("response payload = %q, want ok", resp.Payload)
	}
	if got := executor.executeCalls.Load(); got != 2 {
		t.Fatalf("Execute calls = %d, want 2 (fail once, then succeed)", got)
	}
	if !executor.deadlineSeen.Load() {
		t.Fatal("retry path did not bound the attempt context with the entry budget")
	}
}

// TestRetryWiringExecuteRetriesCustomStatusError proves classification runs
// through statusCodeFromError for non-auth error types: a custom error
// carrying 429 is retried up to MaxAttempts and then surfaces.
func TestRetryWiringExecuteRetriesCustomStatusError(t *testing.T) {
	executor := &retryTestExecutor{
		executeFn: func(context.Context, *Auth) (cliproxyexecutor.Response, error) {
			return cliproxyexecutor.Response{}, &retryIntegrationStatusError{status: http.StatusTooManyRequests}
		},
	}
	retry := internalconfig.RetryConfig{MaxAttempts: 3, MaxTimeMS: 5000, BackoffMS: 1}
	manager, _, model := newRetryIntegrationManager(t, executor, nil, retry)

	_, errExecute := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if statusCodeFromError(errExecute) != http.StatusTooManyRequests {
		t.Fatalf("Execute() error = %v, want HTTP 429", errExecute)
	}
	if got := executor.executeCalls.Load(); got != 3 {
		t.Fatalf("Execute calls = %d, want MaxAttempts=3", got)
	}
}

// TestRetryWiringMarkResultOncePerCredential checks that per-entry retry
// does not double-mark the credential: three failed attempts produce
// exactly one result for the credential iteration (the final error).
func TestRetryWiringMarkResultOncePerCredential(t *testing.T) {
	executor := &retryTestExecutor{
		executeFn: func(context.Context, *Auth) (cliproxyexecutor.Response, error) {
			return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "upstream down"}
		},
	}
	hook := &resultCaptureHook{}
	retry := internalconfig.RetryConfig{MaxAttempts: 3, MaxTimeMS: 5000, BackoffMS: 1}
	manager, _, model := newRetryIntegrationManager(t, executor, hook, retry)

	_, errExecute := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if statusCodeFromError(errExecute) != http.StatusServiceUnavailable {
		t.Fatalf("Execute() error = %v, want HTTP 503", errExecute)
	}
	if got := executor.executeCalls.Load(); got != 3 {
		t.Fatalf("Execute calls = %d, want 3", got)
	}
	results := hook.Results()
	if len(results) != 1 {
		t.Fatalf("hook results = %d (%#v), want exactly 1 per credential iteration", len(results), results)
	}
	if results[0].Success {
		t.Fatalf("hook result = %#v, want the failed final-attempt result", results[0])
	}
	if results[0].Error == nil || results[0].Error.StatusCode() != http.StatusServiceUnavailable {
		t.Fatalf("hook result error = %#v, want HTTP 503 from the final attempt", results[0].Error)
	}
}

// TestRetryWiringExecuteCountDefaultNoDeadline is the count-path mirror of
// the default-safe gate.
func TestRetryWiringExecuteCountDefaultNoDeadline(t *testing.T) {
	executor := &retryTestExecutor{
		countFn: func(context.Context, *Auth) (cliproxyexecutor.Response, error) {
			return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "upstream down"}
		},
	}
	manager, _, model := newRetryIntegrationManager(t, executor, nil, internalconfig.RetryConfig{})

	_, errCount := manager.ExecuteCount(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if statusCodeFromError(errCount) != http.StatusServiceUnavailable {
		t.Fatalf("ExecuteCount() error = %v, want HTTP 503", errCount)
	}
	if got := executor.countCalls.Load(); got != 1 {
		t.Fatalf("CountTokens calls = %d, want exactly 1 (retry disabled)", got)
	}
	if executor.executeCalls.Load() != 0 {
		t.Fatalf("Execute calls = %d, want 0 on the count path", executor.executeCalls.Load())
	}
	if executor.deadlineSeen.Load() {
		t.Fatal("count path stamped a deadline with retry disabled")
	}
}

// TestRetryWiringExecuteCountRetriesTransientStatus is the count-path retry
// mirror. The 503 must not look like a count-tokens endpoint 404, so the
// availability-neutral branch never fires here.
func TestRetryWiringExecuteCountRetriesTransientStatus(t *testing.T) {
	var calls atomic.Int32
	executor := &retryTestExecutor{
		countFn: func(context.Context, *Auth) (cliproxyexecutor.Response, error) {
			if calls.Add(1) == 1 {
				return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "upstream down"}
			}
			return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
		},
	}
	retry := internalconfig.RetryConfig{MaxAttempts: 3, MaxTimeMS: 5000, BackoffMS: 1}
	manager, _, model := newRetryIntegrationManager(t, executor, nil, retry)

	resp, errCount := manager.ExecuteCount(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if errCount != nil {
		t.Fatalf("ExecuteCount() error = %v, want success after one retry", errCount)
	}
	if string(resp.Payload) != "ok" {
		t.Fatalf("response payload = %q, want ok", resp.Payload)
	}
	if got := executor.countCalls.Load(); got != 2 {
		t.Fatalf("CountTokens calls = %d, want 2 (fail once, then succeed)", got)
	}
}

// ---------------------------------------------------------------------------
// Seam-pinned degenerate loop outcomes. RunInnerLoop can report zero
// attempts only when the wall clock crosses the (parent-clamped) entry
// deadline while the parent context's timer has not yet flipped
// execCtx.Err() (the time.Until(deadline) pre-check in RunInnerLoop, plus
// EntryBudget clamping the child deadline onto the parent's). That race
// cannot be constructed
// deterministically from outside, so the conductor's zero-attempt guards
// (conductor_execution.go, both mixed loops) are driven through the
// runInnerLoopFn seam instead.
// ---------------------------------------------------------------------------

// swapRunInnerLoopSeam installs fn as the conductor's inner-loop driver for
// the duration of the test and restores the real RunInnerLoop afterwards.
// The seam is a package-level variable, so the discipline is two-sided:
// only non-parallel tests may use it, AND no test that enables
// routing.retry (MaxAttempts >= 2) may call t.Parallel() — otherwise it
// could race a swapped seam. go test -race is the backstop for violations.
func swapRunInnerLoopSeam(t *testing.T, fn func(context.Context, InnerLoopOpts, AttemptFn) InnerLoopResult) {
	t.Helper()
	prev := runInnerLoopFn
	runInnerLoopFn = fn
	t.Cleanup(func() { runInnerLoopFn = prev })
}

// assertBudgetExhaustedOutcome checks the conductor surfaced the degenerate
// zero-attempt loop exit as a failed dispatch: a non-nil *Error with code
// entry_budget_exhausted, no request-invalid hard-stop classification, and
// exactly one failed MarkResult for the credential.
func assertBudgetExhaustedOutcome(t *testing.T, errExecute error, hook *resultCaptureHook, authID string) {
	t.Helper()
	if errExecute == nil {
		t.Fatal("returned error = nil, want entry_budget_exhausted (a zero-attempt loop exit must not look like a success)")
	}
	var budgetErr *Error
	if !errors.As(errExecute, &budgetErr) || budgetErr == nil || budgetErr.Code != "entry_budget_exhausted" {
		t.Fatalf("returned error = %v (%T), want *Error with code entry_budget_exhausted", errExecute, errExecute)
	}
	// The synthesized error carries no HTTP status. If classification ever
	// turned it into a request fault, the conductor's hard-stop branch
	// (isRequestInvalidError) would surface it without credential rotation;
	// pin that it does not.
	if isRequestInvalidError(errExecute) {
		t.Fatal("entry_budget_exhausted classified as a request-invalid error; the hard-stop branch would fire on it")
	}
	results := hook.Results()
	if len(results) != 1 {
		t.Fatalf("hook results = %d (%#v), want exactly 1 failed MarkResult", len(results), results)
	}
	if results[0].Success {
		t.Fatalf("hook result = %#v, want Success=false for a zero-attempt budget exit", results[0])
	}
	if results[0].Error == nil || results[0].Error.Code != "entry_budget_exhausted" {
		t.Fatalf("hook result error = %#v, want code entry_budget_exhausted", results[0].Error)
	}
	if results[0].AuthID != authID {
		t.Fatalf("hook result AuthID = %q, want %q", results[0].AuthID, authID)
	}
}

// TestRetryWiringZeroAttemptBudgetMarksFailure pins the executeMixedOnce
// zero-attempt guard: a loop that exits before any attempt ran must mark a
// failure, not a false success. The stub never invokes the attempt closure,
// so resp/errExec keep their zero values — without the guard the conductor
// would MarkResult(Success: true) and return an empty response with nil
// error.
func TestRetryWiringZeroAttemptBudgetMarksFailure(t *testing.T) {
	executor := &retryTestExecutor{
		executeFn: func(context.Context, *Auth) (cliproxyexecutor.Response, error) {
			t.Error("real executor invoked despite a zero-attempt stub loop")
			return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
		},
	}
	hook := &resultCaptureHook{}
	retry := internalconfig.RetryConfig{MaxAttempts: 3, MaxTimeMS: 5000, BackoffMS: 1}
	manager, auth, model := newRetryIntegrationManager(t, executor, hook, retry)

	swapRunInnerLoopSeam(t, func(context.Context, InnerLoopOpts, AttemptFn) InnerLoopResult {
		// Degenerate budget exit: deadline crossed before attempt one, so
		// Attempts is 0 and Last is the zero value.
		return InnerLoopResult{Reason: ReasonBudgetOut}
	})

	_, errExecute := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	assertBudgetExhaustedOutcome(t, errExecute, hook, auth.ID)
	if got := executor.executeCalls.Load(); got != 0 {
		t.Fatalf("Execute calls = %d, want 0 (the stub must not run the real attempt body)", got)
	}
}

// TestRetryWiringExecuteCountZeroAttemptBudgetMarksFailure is the count-path
// mirror (executeCountMixedOnce guard).
func TestRetryWiringExecuteCountZeroAttemptBudgetMarksFailure(t *testing.T) {
	executor := &retryTestExecutor{
		countFn: func(context.Context, *Auth) (cliproxyexecutor.Response, error) {
			t.Error("real CountTokens invoked despite a zero-attempt stub loop")
			return cliproxyexecutor.Response{Payload: []byte("ok")}, nil
		},
	}
	hook := &resultCaptureHook{}
	retry := internalconfig.RetryConfig{MaxAttempts: 3, MaxTimeMS: 5000, BackoffMS: 1}
	manager, auth, model := newRetryIntegrationManager(t, executor, hook, retry)

	swapRunInnerLoopSeam(t, func(context.Context, InnerLoopOpts, AttemptFn) InnerLoopResult {
		return InnerLoopResult{Reason: ReasonBudgetOut}
	})

	_, errCount := manager.ExecuteCount(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	assertBudgetExhaustedOutcome(t, errCount, hook, auth.ID)
	if got := executor.countCalls.Load(); got != 0 {
		t.Fatalf("CountTokens calls = %d, want 0 (the stub must not run the real attempt body)", got)
	}
	if got := executor.executeCalls.Load(); got != 0 {
		t.Fatalf("Execute calls = %d, want 0 on the count path", got)
	}
}

// TestRetryWiringLoopReasonSurfacesStillFailClosed pins the non-degenerate
// exit: a loop that ran attempts and gave up on a retryable failure must
// surface the LAST attempt's error as a failed MarkResult, never a false
// success. The stub honors the loop's side-effect contract (the conductor
// reads the final outcome through resp/errExec written by AttemptFn, not
// through InnerLoopResult.Last), so it drives the real attempt closure
// twice against an always-503 executor.
func TestRetryWiringLoopReasonSurfacesStillFailClosed(t *testing.T) {
	executor := &retryTestExecutor{
		executeFn: func(context.Context, *Auth) (cliproxyexecutor.Response, error) {
			return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusServiceUnavailable, Message: "upstream down"}
		},
	}
	hook := &resultCaptureHook{}
	retry := internalconfig.RetryConfig{MaxAttempts: 3, MaxTimeMS: 5000, BackoffMS: 1}
	manager, auth, model := newRetryIntegrationManager(t, executor, hook, retry)

	swapRunInnerLoopSeam(t, func(ctx context.Context, _ InnerLoopOpts, attempt AttemptFn) InnerLoopResult {
		var last InnerAttemptResult
		for i := 0; i < 2; i++ {
			last = attempt(ctx)
		}
		return InnerLoopResult{Attempts: 2, Last: last, Reason: ReasonAttemptsOut}
	})

	_, errExecute := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
	if statusCodeFromError(errExecute) != http.StatusServiceUnavailable {
		t.Fatalf("returned error = %v, want the last attempt's HTTP 503 surfaced", errExecute)
	}
	results := hook.Results()
	if len(results) != 1 {
		t.Fatalf("hook results = %d (%#v), want exactly 1 failed MarkResult", len(results), results)
	}
	if results[0].Success {
		t.Fatalf("hook result = %#v, want Success=false for an attempts-out exit", results[0])
	}
	if results[0].Error == nil || results[0].Error.StatusCode() != http.StatusServiceUnavailable {
		t.Fatalf("hook result error = %#v, want HTTP 503 from the last attempt", results[0].Error)
	}
	if results[0].AuthID != auth.ID {
		t.Fatalf("hook result AuthID = %q, want %q", results[0].AuthID, auth.ID)
	}
	if got := executor.executeCalls.Load(); got != 2 {
		t.Fatalf("Execute calls = %d, want 2 (stub loop ran the closure twice)", got)
	}
}
