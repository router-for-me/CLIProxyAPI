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
	if got := state.Quota.NextRecoverAt.Sub(before); got < transientRateLimitMinimum {
		t.Fatalf("sub-second transient stream hint was not floored: got %v, want at least %v", got, transientRateLimitMinimum)
	}
}
