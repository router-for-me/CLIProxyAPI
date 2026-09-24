package auth

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/sirupsen/logrus"
)

// retryAfterProvider mirrors the package-local interface already used by
// retryAfterFromError (conductor_cooldown.go:1487). It lets upstream-typed
// errors (e.g. Gemini/Codex error structs) carry a Retry-After hint that
// overrides the exponential backoff.
type retryAfterProvider interface {
	RetryAfter() *time.Duration
}

// extractRetryAfter pulls a Retry-After hint from err via the
// retryAfterProvider interface. Returns nil when the error does not
// implement the interface or carries no hint.
func extractRetryAfter(err error) *time.Duration {
	if err == nil {
		return nil
	}
	var rap retryAfterProvider
	if errors.As(err, &rap) && rap != nil {
		return rap.RetryAfter()
	}
	return nil
}

// LogInnerLoopResult emits the structured log fields for an inner-loop
// outcome. Caller decides whether to use logrus.Debug/Info/Warn based on
// the reason. The fields line up with the design doc's
// entry_retry_attempt / entry_retry_elapsed_ms / entry_retry_reason
// naming so log scrapers can find them.
func LogInnerLoopResult(entry *logrus.Entry, model, authID string, res InnerLoopResult, elapsed time.Duration, maxAttempts uint16, maxTimeMS uint32) {
	if entry == nil {
		return
	}
	entry.WithFields(logrus.Fields{
		"entry_retry_attempt":      res.Attempts,
		"entry_retry_elapsed_ms":   elapsed.Milliseconds(),
		"entry_retry_reason":       string(res.Reason),
		"entry_retry_max_attempts": maxAttempts,
		"entry_retry_max_time_ms":  maxTimeMS,
		"entry_retry_model":        model,
		"entry_retry_auth_id":      authID,
	}).Debug("entry_retry_loop_exit")
}

// InnerAttemptResult is what one attempt returns to the inner retry loop.
// A nil Result means the attempt did not run (callers should not retry);
// FirstByte indicates whether any byte has been written to the client
// (post-stream, no retry per the AGENTS.md rule).
type InnerAttemptResult struct {
	Status    int
	Err       error
	FirstByte bool
}

// AttemptFn runs one attempt against a single upstream entry. It receives
// a sub-context bounded by the per-entry deadline; it MUST honor ctx
// cancellation (the stdlib HTTP client does this automatically).
type AttemptFn func(ctx context.Context) InnerAttemptResult

// InnerLoopOpts configures the per-entry retry sequence.
type InnerLoopOpts struct {
	// MaxAttempts caps the number of attempts on this entry (including
	// the first). 0 disables retry: one attempt, then exit with
	// ReasonNonTransient / ReasonAttemptsOut.
	MaxAttempts uint16
	// MaxTimeMS caps the total wall-time budget for the entry. When 0,
	// DefaultMaxTimeMS (5000) applies.
	MaxTimeMS uint32
	// BackoffMS is the base inter-attempt backoff. When 0,
	// DefaultBackoffMS (200) applies. Ignored when the response carries
	// a Retry-After header (Retry-After wins).
	BackoffMS uint32
}

// InnerLoopResult is what the inner loop returns to the outer pool
// rotation layer. Last is the result of the last attempt (or zero-value
// when no attempt ran); Reason is one of the ExitReason constants.
type InnerLoopResult struct {
	Last   InnerAttemptResult
	Reason ExitReason
	// Attempts is the total number of attempts the loop ran (1 = first try).
	Attempts int32
}

// innerLoopOptsFromConfig projects the global routing.retry config into
// InnerLoopOpts. A zero MaxAttempts disables retry so the conductor
// degrades to exactly one attempt per entry — the behavior before this
// wiring existed; enabling retry is an explicit operator decision. The
// conductor only enters RunInnerLoop when the result has MaxAttempts >= 2
// (see executeMixedOnce), so a disabled config never wraps attempts in the
// per-entry deadline that EntryBudget would otherwise stamp.
func innerLoopOptsFromConfig(cfg *internalconfig.Config) InnerLoopOpts {
	if cfg == nil || cfg.Routing.Retry.MaxAttempts == 0 {
		return InnerLoopOpts{}
	}
	return InnerLoopOpts{
		MaxAttempts: cfg.Routing.Retry.MaxAttempts,
		MaxTimeMS:   cfg.Routing.Retry.MaxTimeMS,
		BackoffMS:   cfg.Routing.Retry.BackoffMS,
	}
}

// RunInnerLoop drives per-entry retry with bounded attempts + wall-time
// budget. The contract:
//
//   - Honors parent context: if parent is canceled, exit ReasonParentCtxDone.
//   - Honors MaxAttempts: after MaxAttempts transient failures, exit
//     ReasonAttemptsOut.
//   - Honors MaxTimeMS: when the deadline expires (before or during an
//     attempt), exit ReasonBudgetOut. The sub-context passed to the
//     attempt also expires at the deadline so in-flight HTTP calls are
//     canceled by the stdlib client.
//   - FirstByte=true ends the loop immediately (ReasonStreamStarted) —
//     post-stream retry is forbidden.
//   - Retry-After header (if present) overrides the exponential backoff
//     for that attempt's sleep, clamped to the remaining budget.
//   - Any non-retryable response (2xx success, or a 4xx other than 408/429,
//     or an application-level error) exits with ReasonSuccess or
//     ReasonNonTransient respectively. The caller distinguishes 2xx from
//     hard-fail by inspecting Result.Last.Status and Result.Last.Err.
//
// Conductor integration: executeMixedOnce and executeCountMixedOnce build
// an InnerLoopOpts from routing.retry (innerLoopOptsFromConfig) and enter
// this loop only when MaxAttempts >= 2, so a default config keeps the
// single-attempt inline path (and never wraps execCtx in the EntryBudget
// deadline). The AttemptFn wraps the executor.Execute / CountTokens call
// plus the unauthorized-refresh retry, and MarkResult still runs exactly
// once per credential iteration with the final attempt's outcome. Streaming
// loops are intentionally unwired: pre-first-byte failover is handled by
// credential rotation in the outer loop, and post-first-byte retry is
// forbidden (FirstByte / ReasonStreamStarted stays reserved for a future
// stream-aware wrapper).
//
// Concurrency: RunInnerLoop is single-goroutine by design; the caller
// invokes it from the per-attempt loop in executeMixedOnce /
// executeCountMixedOnce (one RunInnerLoop per picked auth, in series).
func RunInnerLoop(parent context.Context, opts InnerLoopOpts, attempt AttemptFn) InnerLoopResult {
	maxAttempts := opts.MaxAttempts
	if maxAttempts == 0 {
		maxAttempts = 1
	}
	ctx, cancel, deadline := EntryBudget(parent, EntryBudgetOpts{MaxTimeMS: opts.MaxTimeMS})
	defer cancel()

	res := InnerLoopResult{Reason: ReasonNonTransient}
	var attempts int32
	for i := uint16(1); i <= maxAttempts; i++ {
		if time.Until(deadline) <= 0 {
			res.Reason = ReasonBudgetOut
			res.Attempts = atomic.LoadInt32(&attempts)
			return res
		}
		// Per-attempt sub-context tied to the deadline so any blocking
		// attempt is canceled when the budget expires.
		subCtx, subCancel := context.WithDeadline(ctx, deadline)
		r := attempt(subCtx)
		subCancel()
		atomic.AddInt32(&attempts, 1)
		res.Last = r

		if r.FirstByte {
			res.Reason = ReasonStreamStarted
			res.Attempts = atomic.LoadInt32(&attempts)
			return res
		}
		// Success: 2xx response with no error and not flagged retryable.
		if r.Err == nil && !IsRetryableStatus(r.Status) && r.Status >= 200 && r.Status < 300 {
			res.Reason = ReasonSuccess
			res.Attempts = atomic.LoadInt32(&attempts)
			return res
		}
		// Non-retryable: caller should decide based on the response.
		// We surface success vs non-transient using the unified predicate:
		// if it's NOT retryable AND not 2xx → non_transient.
		if !IsRetryable(nil, r.Err) && !IsRetryableStatus(r.Status) {
			res.Reason = ReasonNonTransient
			res.Attempts = atomic.LoadInt32(&attempts)
			return res
		}
		if i == maxAttempts {
			res.Reason = ReasonAttemptsOut
			res.Attempts = atomic.LoadInt32(&attempts)
			return res
		}
		// Sleep before the next attempt.
		sleep := NextBackoff(deadline, opts.BackoffMS, int(i))
		// If the response carried a Retry-After, prefer it (clamped to
		// the remaining budget). We honor it for any retryable status
		// (408/429/5xx) and for any error that implements
		// retryAfterProvider.
		if ra := extractRetryAfter(r.Err); ra != nil && *ra > 0 {
			d := *ra
			if remaining := time.Until(deadline); remaining > 0 && d > remaining {
				d = remaining
			}
			if d > sleep {
				sleep = d
			}
		}
		select {
		case <-time.After(sleep):
		case <-ctx.Done():
			res.Reason = ReasonParentCtxDone
			res.Attempts = atomic.LoadInt32(&attempts)
			return res
		case <-time.After(time.Until(deadline)):
			res.Reason = ReasonBudgetOut
			res.Attempts = atomic.LoadInt32(&attempts)
			return res
		}
	}
	// Unreachable, but keeps the compiler happy if MaxAttempts == 0
	// is ever passed (we coerce to 1 above, so this is defensive).
	res.Reason = ReasonAttemptsOut
	res.Attempts = atomic.LoadInt32(&attempts)
	return res
}
