package auth

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

// helper: build an AttemptFn that returns a fixed sequence of results.
func sequence(results ...InnerAttemptResult) (AttemptFn, *int32) {
	var idx int32
	fn := func(ctx context.Context) InnerAttemptResult {
		i := int(idx)
		idx++
		if i < len(results) {
			return results[i]
		}
		return results[len(results)-1]
	}
	return fn, &idx
}

func TestInnerLoopSuccessFirstTry(t *testing.T) {
	fn, _ := sequence(InnerAttemptResult{Status: 200})
	res := RunInnerLoop(context.Background(), InnerLoopOpts{MaxAttempts: 3, MaxTimeMS: 10000}, fn)
	if res.Reason != ReasonSuccess {
		t.Fatalf("want success, got %v", res.Reason)
	}
	if res.Attempts != 1 {
		t.Fatalf("want 1 attempt, got %d", res.Attempts)
	}
}

func TestInnerLoopAttemptsExhaustedOnTransient5xx(t *testing.T) {
	fn, counter := sequence(
		InnerAttemptResult{Status: 503},
		InnerAttemptResult{Status: 503},
		InnerAttemptResult{Status: 503},
	)
	res := RunInnerLoop(context.Background(), InnerLoopOpts{MaxAttempts: 3, MaxTimeMS: 10000, BackoffMS: 1}, fn)
	if res.Reason != ReasonAttemptsOut {
		t.Fatalf("want attempts_exhausted, got %v (attempts=%d, counter=%d)", res.Reason, res.Attempts, *counter)
	}
	if res.Attempts != 3 {
		t.Fatalf("want 3 attempts, got %d", res.Attempts)
	}
}

func TestInnerLoopStopsOnNonTransient4xx(t *testing.T) {
	fn, counter := sequence(InnerAttemptResult{Status: 404})
	res := RunInnerLoop(context.Background(), InnerLoopOpts{MaxAttempts: 3, MaxTimeMS: 10000, BackoffMS: 1}, fn)
	if res.Reason != ReasonNonTransient {
		t.Fatalf("want non_transient, got %v", res.Reason)
	}
	if res.Attempts != 1 {
		t.Fatalf("want 1 attempt (no retry on 404), got %d (counter=%d)", res.Attempts, *counter)
	}
}

func TestInnerLoopStopsOnApplicationError(t *testing.T) {
	fn, _ := sequence(InnerAttemptResult{Err: errors.New("invalid_request_error: model not supported")})
	res := RunInnerLoop(context.Background(), InnerLoopOpts{MaxAttempts: 3, MaxTimeMS: 10000}, fn)
	if res.Reason != ReasonNonTransient {
		t.Fatalf("want non_transient, got %v", res.Reason)
	}
	if res.Attempts != 1 {
		t.Fatalf("want 1 attempt (no retry on app error), got %d", res.Attempts)
	}
}

func TestInnerLoopRetriesOnTransientNetworkError(t *testing.T) {
	fn, _ := sequence(
		InnerAttemptResult{Err: io.EOF},
		InnerAttemptResult{Status: 200},
	)
	res := RunInnerLoop(context.Background(), InnerLoopOpts{MaxAttempts: 3, MaxTimeMS: 10000, BackoffMS: 1}, fn)
	if res.Reason != ReasonSuccess {
		t.Fatalf("want success after 1 retry, got %v", res.Reason)
	}
	if res.Attempts != 2 {
		t.Fatalf("want 2 attempts, got %d", res.Attempts)
	}
}

func TestInnerLoopBudgetExceededBeforeFirstAttempt(t *testing.T) {
	// Construct a parent context whose deadline is already in the past.
	parent, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	fn, counter := sequence(InnerAttemptResult{Status: 200})
	res := RunInnerLoop(parent, InnerLoopOpts{MaxAttempts: 3, MaxTimeMS: 5000, BackoffMS: 1}, fn)
	if res.Reason != ReasonBudgetOut {
		t.Fatalf("want budget_exhausted, got %v", res.Reason)
	}
	if *counter != 0 {
		t.Fatalf("expected zero attempts when budget is already gone, got %d", *counter)
	}
	if res.Attempts != 0 {
		t.Fatalf("res.Attempts = %d, want 0", res.Attempts)
	}
}

func TestInnerLoopStopsOnFirstByte(t *testing.T) {
	// First attempt returns 200 + FirstByte=true; loop must NOT retry.
	fn, counter := sequence(InnerAttemptResult{Status: 200, FirstByte: true})
	res := RunInnerLoop(context.Background(), InnerLoopOpts{MaxAttempts: 3, MaxTimeMS: 10000}, fn)
	if res.Reason != ReasonStreamStarted {
		t.Fatalf("want stream_started, got %v", res.Reason)
	}
	if res.Attempts != 1 {
		t.Fatalf("want 1 attempt, got %d (counter=%d)", res.Attempts, *counter)
	}
}

func TestInnerLoopMaxAttemptsZeroBecomesOne(t *testing.T) {
	// MaxAttempts=0 means "no retry" → exactly one attempt, regardless of
	// the result. With a 503, that's ReasonNonTransient (loop exits at
	// i=1 because !IsRetryable is false → it goes to i==maxAttempts →
	// ReasonAttemptsOut). Either is acceptable for the "off" case; the
	// invariant is exactly one attempt.
	fn, counter := sequence(InnerAttemptResult{Status: 503})
	res := RunInnerLoop(context.Background(), InnerLoopOpts{MaxAttempts: 0, MaxTimeMS: 10000}, fn)
	if res.Attempts != 1 {
		t.Fatalf("MaxAttempts=0 should coerce to 1 attempt, got %d (counter=%d)", res.Attempts, *counter)
	}
	if res.Reason != ReasonAttemptsOut && res.Reason != ReasonNonTransient {
		t.Fatalf("unexpected reason: %v", res.Reason)
	}
}

func TestInnerLoopHonorsParentContextCancel(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(5 * time.Millisecond)
		cancel()
	}()
	fn, _ := sequence(
		InnerAttemptResult{Status: 503},
		InnerAttemptResult{Status: 503},
		InnerAttemptResult{Status: 503},
	)
	res := RunInnerLoop(parent, InnerLoopOpts{MaxAttempts: 10, MaxTimeMS: 60000, BackoffMS: 50}, fn)
	if res.Reason != ReasonParentCtxDone {
		t.Fatalf("want parent_ctx_done, got %v (attempts=%d)", res.Reason, res.Attempts)
	}
}

func TestInnerLoopContextCancellationDuringAttempt(t *testing.T) {
	// Attempt honors ctx cancellation by returning ctx.Err().
	// Tight budget so the test doesn't wait the full default.
	fn := func(ctx context.Context) InnerAttemptResult {
		<-ctx.Done()
		return InnerAttemptResult{Err: ctx.Err()}
	}
	res := RunInnerLoop(context.Background(), InnerLoopOpts{MaxAttempts: 1, MaxTimeMS: 50, BackoffMS: 1}, fn)
	if res.Reason != ReasonNonTransient {
		// ctx.Err() is context.Canceled or DeadlineExceeded; both are
		// application-level errors from IsRetryable's POV.
		t.Fatalf("want non_transient (ctx.Err()), got %v", res.Reason)
	}
}

func TestExtractRetryAfterNoCarrier(t *testing.T) {
	if d := extractRetryAfter(nil); d != nil {
		t.Fatalf("nil err: want nil, got %v", d)
	}
	if d := extractRetryAfter(errors.New("x")); d != nil {
		t.Fatalf("plain error: want nil, got %v", d)
	}
}

type stubCarrier struct{ d *time.Duration }

func (s stubCarrier) Error() string              { return "stub" }
func (s stubCarrier) RetryAfter() *time.Duration { return s.d }

func TestExtractRetryAfterWithCarrier(t *testing.T) {
	d := 7 * time.Second
	got := extractRetryAfter(stubCarrier{d: &d})
	if got == nil || *got != 7*time.Second {
		t.Fatalf("carrier: want 7s, got %v", got)
	}
}

func TestExtractRetryAfterCarrierNilHint(t *testing.T) {
	got := extractRetryAfter(stubCarrier{d: nil})
	if got != nil {
		t.Fatalf("nil hint: want nil, got %v", got)
	}
}
