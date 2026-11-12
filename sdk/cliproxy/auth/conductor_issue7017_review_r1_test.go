package auth

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// Review round 1 regressions: real upstream-attempt marking, Home aggregate
// scheduling precedence, explicit zero hints, and deadline decay.

type issue7017MarkingExecutor struct {
	inner error
}

func (e *issue7017MarkingExecutor) Identifier() string { return "codex" }

func (e *issue7017MarkingExecutor) Execute(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	// Mirror the HTTP transport: it marks the upstream attempt before the
	// executor returns its status error.
	cliproxyexecutor.MarkUpstreamAttempt(ctx)
	return cliproxyexecutor.Response{}, e.inner
}

// streamFirstChunkErr selects the bootstrap variant: the executor accepts
// the stream and the first chunk carries the failure.
var issue7017StreamFirstChunk bool

func (e *issue7017MarkingExecutor) ExecuteStream(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	cliproxyexecutor.MarkUpstreamAttempt(ctx)
	if issue7017StreamFirstChunk {
		ch := make(chan cliproxyexecutor.StreamChunk, 1)
		ch <- cliproxyexecutor.StreamChunk{Err: e.inner}
		close(ch)
		return &cliproxyexecutor.StreamResult{Headers: http.Header{"Content-Type": []string{"text/event-stream"}}, Chunks: ch}, nil
	}
	return nil, e.inner
}

func (e *issue7017MarkingExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, e.inner
}

func (*issue7017MarkingExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (*issue7017MarkingExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestIssue7017ExecuteMarkedAttemptKeepsTrustedRetryAfter(t *testing.T) {
	withQuotaCooldownEnabled(t)
	hint := 256 * time.Second
	manager := NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 0)
	manager.RegisterExecutor(&issue7017MarkingExecutor{inner: issue7017HintError{msg: issue7017TurnBody(), hint: &hint}})
	registerRetryRoundLocalAuthsSkipCoolingOverride(t, manager, "turn-marked")

	_, errExecute := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-6.1-sol"}, cliproxyexecutor.Options{})
	if errExecute == nil {
		t.Fatal("expected terminal 429")
	}
	if got := SafeResponseHeaders(errExecute).Get("Retry-After"); got != "256" {
		t.Fatalf("marked-attempt terminal Retry-After = %q, want 256 (err=%v)", got, errExecute)
	}
}

func TestIssue7017ExecuteStreamMarkedAttemptKeepsTrustedRetryAfter(t *testing.T) {
	withQuotaCooldownEnabled(t)
	hint := 256 * time.Second
	manager := NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 0)
	manager.RegisterExecutor(&issue7017MarkingExecutor{inner: issue7017HintError{msg: issue7017TurnBody(), hint: &hint}})
	registerRetryRoundLocalAuthsSkipCoolingOverride(t, manager, "turn-marked-stream")

	_, errStream := manager.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-6.1-sol"}, cliproxyexecutor.Options{Stream: true})
	if errStream == nil {
		t.Fatal("expected terminal stream error")
	}
	if got := SafeResponseHeaders(errStream).Get("Retry-After"); got != "256" {
		t.Fatalf("marked stream terminal Retry-After = %q, want 256 (err=%v)", got, errStream)
	}
}

func TestIssue7017ExecuteStreamBootstrapMarkedAttemptKeepsTrustedRetryAfter(t *testing.T) {
	withQuotaCooldownEnabled(t)
	issue7017StreamFirstChunk = true
	t.Cleanup(func() { issue7017StreamFirstChunk = false })
	hint := 256 * time.Second
	manager := NewManager(nil, nil, nil)
	manager.SetRetryConfig(0, 0, 0)
	manager.RegisterExecutor(&issue7017MarkingExecutor{inner: issue7017HintError{msg: issue7017TurnBody(), hint: &hint}})
	registerRetryRoundLocalAuthsSkipCoolingOverride(t, manager, "turn-marked-bootstrap")

	result, errStream := manager.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "gpt-6.1-sol"}, cliproxyexecutor.Options{Stream: true})
	if errStream != nil {
		t.Fatalf("bootstrap failure must surface as an error stream, got %v", errStream)
	}
	if result == nil {
		t.Fatal("expected error stream result")
	}
	chunk := <-result.Chunks
	if chunk.Err == nil {
		t.Fatal("expected terminal chunk error")
	}
	if got := SafeResponseHeaders(chunk.Err).Get("Retry-After"); got != "256" {
		t.Fatalf("bootstrap terminal Retry-After = %q, want 256 (err=%v)", got, chunk.Err)
	}
}

func TestIssue7017RetryAfterFromErrorPrefersHomeAggregate(t *testing.T) {
	cause := &Error{Message: "upstream body", HTTPStatus: http.StatusTooManyRequests}
	perAttempt := 5 * time.Second
	annotated := withTrustedRetryAfter(cause, &perAttempt)
	aggregated := 1500 * time.Millisecond
	terminal := markHomeRetryRoundExhausted(annotated, &aggregated, false)
	got := retryAfterFromError(terminal)
	if got == nil || *got != aggregated {
		t.Fatalf("retryAfterFromError = %v, want Home aggregate %v", got, aggregated)
	}
}

func TestIssue7017MarkResultExplicitZeroHintSkipsCooldown(t *testing.T) {
	withQuotaCooldownEnabled(t)
	zero := time.Duration(0)
	manager := NewManager(nil, nil, nil)
	auth := &Auth{ID: "turn-zero-hint", Provider: "codex", Metadata: map[string]any{"type": "codex"}}
	issue7017Register(t, manager, auth)

	manager.MarkResult(context.Background(), Result{
		AuthID:     auth.ID,
		Provider:   "codex",
		Model:      "gpt-6.1-sol",
		Success:    false,
		RetryAfter: &zero,
		Error: &Error{
			Code:       ErrorCodeTransientRateLimit,
			Message:    issue7017TurnBody(),
			Retryable:  true,
			HTTPStatus: http.StatusTooManyRequests,
		},
	})

	got, _ := manager.GetByID(auth.ID)
	state := got.ModelStates["gpt-6.1-sol"]
	if state == nil {
		t.Fatal("expected model state")
	}
	if state.Unavailable || !state.NextRetryAfter.IsZero() {
		t.Fatalf("explicit zero hint must not cool the model: next=%v unavailable=%t", state.NextRetryAfter, state.Unavailable)
	}
	if state.Quota.Exceeded || state.Quota.Reason == "quota" {
		t.Fatalf("explicit zero hint must not write quota: %+v", state.Quota)
	}
}

func TestIssue7017TransientZeroHintKeepsQuotaDeadline(t *testing.T) {
	withQuotaCooldownEnabled(t)
	zero := time.Duration(0)
	future := time.Now().Add(2 * time.Hour)
	manager := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:       "turn-zero-hint-quota",
		Provider: "codex",
		Metadata: map[string]any{"type": "codex"},
		ModelStates: map[string]*ModelState{
			"gpt-6.1-sol": {
				Status:         StatusError,
				Unavailable:    true,
				NextRetryAfter: future,
				Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: future, BackoffLevel: 2},
			},
		},
	}
	issue7017Register(t, manager, auth)

	manager.MarkResult(context.Background(), Result{
		AuthID:     auth.ID,
		Provider:   "codex",
		Model:      "gpt-6.1-sol",
		Success:    false,
		RetryAfter: &zero,
		Error: &Error{
			Code:       ErrorCodeTransientRateLimit,
			Message:    issue7017TurnBody(),
			Retryable:  true,
			HTTPStatus: http.StatusTooManyRequests,
		},
	})

	got, _ := manager.GetByID(auth.ID)
	state := got.ModelStates["gpt-6.1-sol"]
	if state == nil || !state.NextRetryAfter.Equal(future) || !state.Unavailable {
		t.Fatalf("live quota deadline must survive an explicit zero hint: %+v", state)
	}
	if !state.Quota.Exceeded || !state.Quota.NextRecoverAt.Equal(future) || state.Quota.BackoffLevel != 2 {
		t.Fatalf("quota state must stay untouched: %+v", state.Quota)
	}
}

func TestTrustedRetryAfterIdentityHintMayDifferFromDeadline(t *testing.T) {
	// The trusted wrapper hides behind the original *Error identity but must
	// not fabricate one for plain errors.
	plain := errors.New("plain")
	hint := time.Minute
	err := withTrustedRetryAfter(plain, &hint)
	var base *Error
	if errors.As(err, &base) && base != nil {
		t.Fatalf("plain errors must not resolve to *Error")
	}
}

func TestTrustedRetryAfterDeadlineDecays(t *testing.T) {
	cause := &Error{Message: "upstream body", HTTPStatus: http.StatusTooManyRequests}
	hint := 256 * time.Second
	err := withTrustedRetryAfter(cause, &hint)
	wrapped, ok := err.(*trustedRetryAfterError)
	if !ok {
		t.Fatalf("expected trusted wrapper, got %T", err)
	}
	// Move the captured deadline into the past by direct field manipulation
	// so the remaining calculation is deterministic without sleeping.
	wrapped.deadline = time.Now().Add(-time.Second)
	if got := SafeResponseHeaders(err).Get("Retry-After"); got != "" {
		t.Fatalf("expired trusted deadline must not advertise Retry-After, got %q", got)
	}
	remaining := retryAfterFromError(err)
	if remaining != nil {
		t.Fatalf("expired trusted deadline with no provider hint must yield nil, got %v", remaining)
	}
}
