package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestQuotaShareRunWindowAggregateAllReturn pins the happy path: when all
// per-window sub-queries return within their timeout, the result map
// contains every window's used value and partial is false.
func TestQuotaShareRunWindowAggregateAllReturn(t *testing.T) {
	got, partial, err := quotaShareRunWindowAggregate(
		context.Background(),
		[]string{"hourly", "weekly", "monthly"},
		100*time.Millisecond,
		func(_ context.Context, windowType string) (int64, error) {
			switch windowType {
			case "hourly":
				return 100, nil
			case "weekly":
				return 200, nil
			case "monthly":
				return 300, nil
			}
			t.Fatalf("unexpected window_type: %q", windowType)
			return 0, nil
		},
	)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if partial {
		t.Errorf("partial = true, want false on all-return")
	}
	if got["hourly"] != 100 || got["weekly"] != 200 || got["monthly"] != 300 {
		t.Errorf("used map = %+v, want hourly=100 weekly=200 monthly=300", got)
	}
}

// TestQuotaShareRunWindowAggregatePartialOnTimeout pins the round-2
// review-finding fix: when one window's sub-query hits its per-window
// timeout (context-deadline-exceeded), the helper returns the windows that
// did come back plus a zero placeholder for the timed-out one, with
// partial=true. This is the core guarantee that lets the handler surface
// 200 + partial=true rather than HTTP 500 under sustained PG slowness.
func TestQuotaShareRunWindowAggregatePartialOnTimeout(t *testing.T) {
	// hourly returns quickly, weekly blocks past its 500ms budget, monthly
	// returns quickly. We use a per-window timeout of 80ms so the test
	// does not actually have to wait the full 500ms.
	got, partial, err := quotaShareRunWindowAggregate(
		context.Background(),
		[]string{"hourly", "weekly", "monthly"},
		80*time.Millisecond,
		func(ctx context.Context, windowType string) (int64, error) {
			switch windowType {
			case "hourly":
				return 111, nil
			case "weekly":
				// Block past the per-window timeout. The helper sees
				// ctx.Done() first and treats the timeout as a soft
				// partial, so we must NOT return an error here —
				// simulating real PG behavior where the driver
				// surfaces the context error to the caller's scan.
				select {
				case <-ctx.Done():
					return 0, ctx.Err()
				case <-time.After(500 * time.Millisecond):
					return 222, nil
				}
			case "monthly":
				return 333, nil
			}
			return 0, nil
		},
	)
	if err != nil {
		t.Fatalf("err = %v, want nil (timeout is reported via partial, not err)", err)
	}
	if !partial {
		t.Errorf("partial = false, want true when any window timed out")
	}
	if got["hourly"] != 111 {
		t.Errorf("hourly used = %d, want 111 (came back before timeout)", got["hourly"])
	}
	if got["monthly"] != 333 {
		t.Errorf("monthly used = %d, want 333 (came back before timeout)", got["monthly"])
	}
	if got["weekly"] != 0 {
		t.Errorf("weekly used = %d, want 0 (placeholder for timed-out window)", got["weekly"])
	}
}

// TestQuotaShareRunWindowAggregateCancelHonorsParentContext pins the
// case where the handler's outer 2s context cancels mid-aggregation: the
// helper observes the parent cancel and marks every still-pending window
// as partial. This guards against the scenario where a slow first window
// eats the entire budget and the dashboard sees a fully-zeroed response
// with partial=true (acceptable — the timeout fired).
func TestQuotaShareRunWindowAggregateCancelHonorsParentContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel — every sub-query sees an already-cancelled context

	got, partial, err := quotaShareRunWindowAggregate(
		ctx,
		[]string{"hourly", "weekly"},
		500*time.Millisecond,
		func(ctx context.Context, windowType string) (int64, error) {
			return 0, ctx.Err()
		},
	)
	if err != nil {
		t.Fatalf("err = %v, want nil (parent cancel is reported via partial)", err)
	}
	if !partial {
		t.Errorf("partial = false, want true when parent context pre-cancelled")
	}
	if got["hourly"] != 0 || got["weekly"] != 0 {
		t.Errorf("got = %+v, want all zeros (no window completed)", got)
	}
}

// TestQuotaShareRunWindowAggregateNonContextErrorHalts pins the failure
// path: a real (non-context) DB error halts aggregation immediately and
// propagates to the caller so the handler can return 500. Partial is
// surfaced for whatever windows had already been aggregated so the
// caller can log a useful diagnostic.
func TestQuotaShareRunWindowAggregateNonContextErrorHalts(t *testing.T) {
	dbErr := errors.New("connection reset")
	_, _, err := quotaShareRunWindowAggregate(
		context.Background(),
		[]string{"hourly", "weekly", "monthly"},
		500*time.Millisecond,
		func(_ context.Context, windowType string) (int64, error) {
			if windowType == "weekly" {
				return 0, dbErr
			}
			return 42, nil
		},
	)
	if err == nil {
		t.Fatalf("err = nil, want dbErr propagated")
	}
	if !errors.Is(err, dbErr) {
		t.Errorf("err = %v, want wraps %v", err, dbErr)
	}
}

// TestQuotaShareRunWindowAggregateEmptyNoOp pins the no-windows edge
// case: an empty windowTypes slice returns an empty map with no error
// and partial=false. The caller can pre-filter quota windows without
// special-casing the empty path.
func TestQuotaShareRunWindowAggregateEmptyNoOp(t *testing.T) {
	got, partial, err := quotaShareRunWindowAggregate(
		context.Background(),
		nil,
		100*time.Millisecond,
		func(_ context.Context, _ string) (int64, error) {
			t.Fatalf("query should not be called on empty windowTypes")
			return 0, nil
		},
	)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if partial {
		t.Errorf("partial = true, want false on empty input")
	}
	if len(got) != 0 {
		t.Errorf("got = %+v, want empty", got)
	}
}

// TestQuotaShareRunWindowAggregateContextCancelDuringIteration pins the
// mid-flight cancel path: the helper sees ctx.Done() for any sub-query
// that was still pending, marks those windows as partial, and continues
// running subsequent windows (each sub-query gets its own sub-context, so
// later windows still see the parent's deadline). This is the realistic
// shape of "outer 2s timeout fires while the third sub-query is in
// flight": hourly + weekly come back, monthly gets canceled and is
// recorded as zeroed partial.
func TestQuotaShareRunWindowAggregateContextCancelDuringIteration(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	got, partial, err := quotaShareRunWindowAggregate(
		ctx,
		[]string{"hourly", "weekly", "monthly"},
		500*time.Millisecond,
		func(ctx context.Context, windowType string) (int64, error) {
			switch windowType {
			case "hourly":
				return 10, nil
			case "weekly":
				time.Sleep(75 * time.Millisecond)
				// Returns before the per-window timeout (500ms) but
				// possibly after the parent's 50ms deadline. The
				// helper's sub-context inherits the parent's
				// deadline, so a query that overruns it surfaces
				// context.DeadlineExceeded.
				return 0, ctx.Err()
			case "monthly":
				return 0, ctx.Err()
			}
			return 0, nil
		},
	)
	if err != nil {
		t.Fatalf("err = %v, want nil (timeout is reported via partial)", err)
	}
	if !partial {
		t.Errorf("partial = false, want true when parent context cancels mid-flight")
	}
	if got["hourly"] != 10 {
		t.Errorf("hourly used = %d, want 10 (came back before cancel)", got["hourly"])
	}
}
