package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type streamTransientRateLimitError struct {
	retryAfter time.Duration
}

func (e streamTransientRateLimitError) Error() string            { return "429 rate limited" }
func (e streamTransientRateLimitError) StatusCode() int          { return http.StatusTooManyRequests }
func (e streamTransientRateLimitError) TransientRateLimit() bool { return true }

func (e streamTransientRateLimitError) RetryAfter() *time.Duration {
	hint := e.retryAfter
	return &hint
}

// TestExecuteStreamFloorsTransientRateLimitHint covers the streaming failure
// path: a tiny provider hint must not repeatedly select the same credential.
func TestExecuteStreamFloorsTransientRateLimitHint(t *testing.T) {
	withQuotaCooldownEnabled(t)

	hint := time.Duration(observedExhaustedQuotaHint)
	executor := &claudeCancellationTestExecutor{
		streamFn: func(context.Context, *Auth) (*cliproxyexecutor.StreamResult, error) {
			return nil, streamTransientRateLimitError{retryAfter: hint}
		},
	}
	manager, auth, model := newClaudeCancellationTestManager(t, executor, nil)

	before := time.Now()
	_, errStream := manager.ExecuteStream(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
	if errStream == nil {
		t.Fatal("expected the stream request to fail with the upstream 429")
	}

	updated, ok := manager.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatalf("GetByID(%q) did not return auth", auth.ID)
	}
	state := updated.ModelStates[model]
	if state == nil {
		t.Fatalf("expected model state for %q after the failure", model)
	}
	if state.Quota.BackoffLevel != 0 {
		t.Fatalf("expected BackoffLevel to stay 0 for a transient rate limit, got %d", state.Quota.BackoffLevel)
	}
	if state.Quota.Exceeded {
		t.Fatal("transient stream 429 must not set Quota.Exceeded")
	}
	if got := state.NextRetryAfter.Sub(before); got < transientRateLimitMinimum {
		t.Fatalf("sub-second transient stream hint was not floored: got %v, want at least %v", got, transientRateLimitMinimum)
	}
}

// TestExecuteStreamFloorsLateTransientRateLimitHint covers wrapStreamResult:
// a classified 429 after the first payload must not take the quota ladder.
func TestExecuteStreamFloorsLateTransientRateLimitHint(t *testing.T) {
	withQuotaCooldownEnabled(t)

	hint := time.Duration(observedExhaustedQuotaHint)
	executor := &claudeCancellationTestExecutor{
		streamFn: func(context.Context, *Auth) (*cliproxyexecutor.StreamResult, error) {
			ch := make(chan cliproxyexecutor.StreamChunk, 2)
			ch <- cliproxyexecutor.StreamChunk{Payload: []byte(`data: {"type":"response.created"}`)}
			ch <- cliproxyexecutor.StreamChunk{Err: streamTransientRateLimitError{retryAfter: hint}}
			close(ch)
			return &cliproxyexecutor.StreamResult{Chunks: ch}, nil
		},
	}
	manager, auth, model := newClaudeCancellationTestManager(t, executor, nil)

	before := time.Now()
	result, errStream := manager.ExecuteStream(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{Stream: true})
	if errStream != nil {
		t.Fatalf("expected a committed stream after the first payload, got error: %v", errStream)
	}
	if result == nil {
		t.Fatal("expected a committed stream result")
	}
	var sawErr bool
	for chunk := range result.Chunks {
		if chunk.Err != nil {
			sawErr = true
		}
	}
	if !sawErr {
		t.Fatal("expected the classified 429 to arrive after the first payload")
	}

	updated, ok := manager.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatalf("GetByID(%q) did not return auth", auth.ID)
	}
	state := updated.ModelStates[model]
	if state == nil {
		t.Fatalf("expected model state for %q after the late stream failure", model)
	}
	if state.Quota.BackoffLevel != 0 {
		t.Fatalf("expected BackoffLevel to stay 0 for a late transient rate limit, got %d", state.Quota.BackoffLevel)
	}
	if state.Quota.Exceeded {
		t.Fatal("late transient stream 429 must not set Quota.Exceeded")
	}
	if got := state.NextRetryAfter.Sub(before); got < transientRateLimitMinimum {
		t.Fatalf("sub-second late stream hint was not floored: got %v, want at least %v", got, transientRateLimitMinimum)
	}
}
