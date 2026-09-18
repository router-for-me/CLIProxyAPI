package auth

import (
	"context"
	"time"
)

// ExitReason is the outcome of a single per-entry retry sequence. The
// inner retry loop in conductor_execution.go returns one of these to the
// outer pool-failover path, which decides what to do next.
type ExitReason string

const (
	// ReasonSuccess — the entry returned a non-retryable 2xx response.
	ReasonSuccess ExitReason = "success"
	// ReasonNonTransient — the entry returned a non-retryable error (4xx
	// other than 408/429, or an application-level error). The outer pool
	// failover decides whether to surface the error or move on.
	ReasonNonTransient ExitReason = "non_transient"
	// ReasonAttemptsOut — MaxAttempts exhausted before success or
	// non-transient classification.
	ReasonAttemptsOut ExitReason = "attempts_exhausted"
	// ReasonBudgetOut — MaxTimeMS deadline expired before completion.
	ReasonBudgetOut ExitReason = "budget_exhausted"
	// ReasonStreamStarted — the first byte has been written; the inner
	// loop exits because post-stream retry is forbidden.
	ReasonStreamStarted ExitReason = "stream_started"
	// ReasonParentCtxDone — the parent request context was canceled.
	ReasonParentCtxDone ExitReason = "parent_ctx_done"
)

// EntryBudgetOpts captures the per-entry retry budget for one request.
// MaxTimeMS == 0 falls back to DefaultMaxTimeMS.
type EntryBudgetOpts struct {
	MaxTimeMS uint32
}

// DefaultMaxTimeMS is the per-entry wall-time budget when neither the
// global config nor the per-entry override sets a value.
const DefaultMaxTimeMS uint32 = 5000

// EntryBudget returns a sub-context derived from parent with a deadline
// that is the earlier of (now + MaxTimeMS) and parent's existing deadline.
// The caller must defer cancel() to release the sub-context resources.
// The deadline is also returned so the inner loop can sleep up to it
// without re-reading from the context.
//
// When MaxTimeMS is 0, DefaultMaxTimeMS applies. When parent has no
// deadline, the returned deadline equals now + MaxTimeMS.
func EntryBudget(parent context.Context, opts EntryBudgetOpts) (context.Context, context.CancelFunc, time.Time) {
	maxMS := opts.MaxTimeMS
	if maxMS == 0 {
		maxMS = DefaultMaxTimeMS
	}
	deadline := time.Now().Add(time.Duration(maxMS) * time.Millisecond)
	if d, ok := parent.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	ctx, cancel := context.WithDeadline(parent, deadline)
	return ctx, cancel, deadline
}

// NextBackoff returns the duration to sleep before the next retry attempt.
// The value is the exponential base (baseMS * 2^(attempt-1)) clamped to
// half the remaining time until deadline so we never sleep past the budget.
// When baseMS is 0, DefaultBackoffMS (200ms) applies.
func NextBackoff(deadline time.Time, baseMS uint32, attempt int) time.Duration {
	if baseMS == 0 {
		baseMS = DefaultBackoffMS
	}
	if attempt < 1 {
		attempt = 1
	}
	base := time.Duration(baseMS) * time.Millisecond
	// Cap the exponent so we don't overflow on absurd attempt counts.
	shift := attempt - 1
	if shift > 16 {
		shift = 16
	}
	b := base << shift
	remaining := time.Until(deadline) / 2
	if remaining < 0 {
		remaining = 0
	}
	if b > remaining {
		return remaining
	}
	return b
}

// DefaultBackoffMS is the base inter-attempt backoff when neither the
// global config nor the per-entry override sets a value.
const DefaultBackoffMS uint32 = 200
