package auth

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

const issue7521PoolModel = "claude-sonnet-4-6-issue-7521"

func newIssue7521Manager(
	t *testing.T,
	selector Selector,
	exec ProviderExecutor,
	auths ...*Auth,
) *Manager {
	t.Helper()
	withQuotaCooldownEnabled(t)
	if selector == nil {
		selector = &RoundRobinSelector{}
	}
	manager := NewManager(nil, selector, nil)
	manager.SetRetryConfig(2, 30*time.Second, 0)
	manager.RegisterExecutor(exec)
	reg := registry.GetGlobalRegistry()
	for _, auth := range auths {
		authID := auth.ID
		reg.RegisterClient(authID, auth.Provider, []*registry.ModelInfo{{ID: issue7521PoolModel}})
		t.Cleanup(func() {
			reg.UnregisterClient(authID)
		})
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register auth %s: %v", authID, errRegister)
		}
	}
	return manager
}

func newIssue7521Auth(id string) *Auth {
	return &Auth{
		ID:         id,
		Provider:   "claude",
		Status:     StatusActive,
		Attributes: map[string]string{"api_key": "key-" + id},
	}
}

func newIssue7521Upstream429(msg string, retryAfter time.Duration) error {
	hint := retryAfter
	return customStatusError{
		code:       http.StatusTooManyRequests,
		msg:        msg,
		retryAfter: &hint,
	}
}

func assertIssue7521PoolCooldownError(t *testing.T, err error, wantMessage string, wantDeadline time.Duration) {
	t.Helper()
	if err == nil {
		t.Fatal("expected terminal 429 error, got nil")
	}
	if got := statusCodeFromError(err); got != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", got)
	}
	if !strings.Contains(err.Error(), wantMessage) {
		t.Fatalf("error = %q, want upstream message %q preserved", err.Error(), wantMessage)
	}
	var marker interface{ IsModelCooldown() bool }
	if !errors.As(err, &marker) || marker == nil || !marker.IsModelCooldown() {
		t.Fatalf("error %T (%v) must report IsModelCooldown() == true", err, err)
	}
	if !HasTrustedRetryDeadline(err) {
		t.Fatal("HasTrustedRetryDeadline(err) = false, want true")
	}
	retryAfter := SafeResponseHeaders(err).Get("Retry-After")
	seconds, errParse := strconv.ParseInt(retryAfter, 10, 64)
	if errParse != nil || seconds <= 0 {
		t.Fatalf("Retry-After = %q, want positive integer seconds", retryAfter)
	}
	gotDuration := time.Duration(seconds) * time.Second
	if gotDuration > wantDeadline+10*time.Second || gotDuration < wantDeadline-10*time.Second {
		t.Fatalf("Retry-After = %ds (%s), want near pool recovery %s", seconds, gotDuration, wantDeadline)
	}
}

// Variant 2: fresh start where every candidate credential is tried in the same
// round and returns 429; no modelCooldownError is built during selection, yet
// the terminal 429 must report IsModelCooldown() and carry the earliest pool reset.
func TestExecuteLastCredential429CarriesEarliestPoolReset(t *testing.T) {
	first := newIssue7521Auth("7521-exec-a")
	second := newIssue7521Auth("7521-exec-b")
	shortHint := 90 * time.Minute
	longHint := 3 * time.Hour

	var attempts []string
	exec := &customStreamMockExecutor{
		identifier: "claude",
		mockCustomErrorExecutor: mockCustomErrorExecutor{
			identifier: "claude",
			executeFn: func(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				cliproxyexecutor.MarkUpstreamAttempt(ctx)
				attempts = append(attempts, auth.ID)
				if auth.ID == first.ID {
					return cliproxyexecutor.Response{}, newIssue7521Upstream429("first credential rate limited", shortHint)
				}
				return cliproxyexecutor.Response{}, newIssue7521Upstream429("monthly spend limit reached", longHint)
			},
		},
	}
	manager := newIssue7521Manager(t, nil, exec, first, second)

	_, errExec := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: issue7521PoolModel}, cliproxyexecutor.Options{})
	assertIssue7521PoolCooldownError(t, errExec, "monthly spend limit reached", shortHint)
	if len(attempts) != 2 {
		t.Fatalf("attempts = %v, want 2 credentials tried", attempts)
	}
}

func TestExecuteSingleCredentialSubFloorHintReportsActualPoolRecovery(t *testing.T) {
	single := newIssue7521Auth("7521-subfloor-a")
	hint := 7 * time.Second
	exec := &customStreamMockExecutor{
		identifier: "claude",
		mockCustomErrorExecutor: mockCustomErrorExecutor{
			identifier: "claude",
			executeFn: func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				cliproxyexecutor.MarkUpstreamAttempt(ctx)
				return cliproxyexecutor.Response{}, newIssue7521Upstream429("try later", hint)
			},
		},
	}
	manager := newIssue7521Manager(t, nil, exec, single)
	manager.SetRetryConfig(0, 0, 0)

	_, errExec := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: issue7521PoolModel}, cliproxyexecutor.Options{})
	if errExec == nil {
		t.Fatal("expected terminal 429 error")
	}
	// MarkResult clamps the 7s hint up to minQuotaCooldownFloor (10s), so the
	// pool's actual recovery is 10s out. The wrap must advertise that real
	// deadline, not the shorter per-attempt hint.
	if got := SafeResponseHeaders(errExec).Get("Retry-After"); got != "10" {
		t.Fatalf("Retry-After = %q, want 10 (minQuotaCooldownFloor)", got)
	}
	var marker interface{ IsModelCooldown() bool }
	if !errors.As(errExec, &marker) || marker == nil || !marker.IsModelCooldown() {
		t.Fatalf("error %T must report IsModelCooldown() == true", errExec)
	}
}

// Variant 1: one credential is already cooling down; the remaining credential
// returns 429 and the next pick fails with modelCooldownError, which is
// replaced by preferredExecutionAttemptError. The terminal 429 must still keep
// the upstream text and carry the earliest pool cooldown reset.
func TestExecuteLastUsableCredential429KeepsExistingCooldownReset(t *testing.T) {
	first := newIssue7521Auth("7521-var1-a")
	second := newIssue7521Auth("7521-var1-b")
	existingCooldown := 75 * time.Minute
	freshHint := 3 * time.Hour

	exec := &customStreamMockExecutor{
		identifier: "claude",
		mockCustomErrorExecutor: mockCustomErrorExecutor{
			identifier: "claude",
			executeFn: func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				cliproxyexecutor.MarkUpstreamAttempt(ctx)
				return cliproxyexecutor.Response{}, newIssue7521Upstream429("second credential rate limited", freshHint)
			},
		},
	}
	manager := newIssue7521Manager(t, nil, exec, first, second)
	manager.MarkResult(context.Background(), Result{
		AuthID:          first.ID,
		Provider:        "claude",
		Model:           issue7521PoolModel,
		Success:         false,
		RetryAfter:      &existingCooldown,
		CredentialScope: true,
		Error:           &Error{HTTPStatus: http.StatusTooManyRequests, Message: "already cooling"},
	})

	_, errExec := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: issue7521PoolModel}, cliproxyexecutor.Options{})
	assertIssue7521PoolCooldownError(t, errExec, "second credential rate limited", existingCooldown)
}

func TestExecuteCountLastCredential429CarriesEarliestPoolReset(t *testing.T) {
	first := newIssue7521Auth("7521-count-a")
	second := newIssue7521Auth("7521-count-b")
	shortHint := 85 * time.Minute
	longHint := 4 * time.Hour

	exec := &customStreamMockExecutor{
		identifier: "claude",
		mockCustomErrorExecutor: mockCustomErrorExecutor{
			identifier: "claude",
			countFn: func(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				cliproxyexecutor.MarkUpstreamAttempt(ctx)
				if auth.ID == first.ID {
					return cliproxyexecutor.Response{}, newIssue7521Upstream429("count first limited", shortHint)
				}
				return cliproxyexecutor.Response{}, newIssue7521Upstream429("count second limited", longHint)
			},
		},
	}
	manager := newIssue7521Manager(t, nil, exec, first, second)

	_, errCount := manager.ExecuteCount(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: issue7521PoolModel}, cliproxyexecutor.Options{})
	assertIssue7521PoolCooldownError(t, errCount, "count second limited", shortHint)
}

func TestExecuteStreamLastCredential429CarriesEarliestPoolReset(t *testing.T) {
	shortHint := 80 * time.Minute
	longHint := 3 * time.Hour

	t.Run("direct stream error", func(t *testing.T) {
		first := newIssue7521Auth("7521-stream-direct-a")
		second := newIssue7521Auth("7521-stream-direct-b")
		exec := &customStreamMockExecutor{
			identifier: "claude",
			streamFn: func(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
				cliproxyexecutor.MarkUpstreamAttempt(ctx)
				if auth.ID == first.ID {
					return nil, newIssue7521Upstream429("stream first limited", shortHint)
				}
				return nil, newIssue7521Upstream429("stream second limited", longHint)
			},
		}
		manager := newIssue7521Manager(t, nil, exec, first, second)

		_, errStream := manager.ExecuteStream(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: issue7521PoolModel}, cliproxyexecutor.Options{Stream: true})
		assertIssue7521PoolCooldownError(t, errStream, "stream second limited", shortHint)
	})

	t.Run("bootstrap chunk error", func(t *testing.T) {
		first := newIssue7521Auth("7521-stream-boot-a")
		second := newIssue7521Auth("7521-stream-boot-b")
		exec := &customStreamMockExecutor{
			identifier: "claude",
			streamFn: func(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
				cliproxyexecutor.MarkUpstreamAttempt(ctx)
				chunks := make(chan cliproxyexecutor.StreamChunk, 1)
				if auth.ID == first.ID {
					chunks <- cliproxyexecutor.StreamChunk{Err: newIssue7521Upstream429("bootstrap first limited", shortHint)}
				} else {
					chunks <- cliproxyexecutor.StreamChunk{Err: newIssue7521Upstream429("bootstrap second limited", longHint)}
				}
				close(chunks)
				return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
			},
		}
		manager := newIssue7521Manager(t, nil, exec, first, second)

		streamRes, errStream := manager.ExecuteStream(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: issue7521PoolModel}, cliproxyexecutor.Options{Stream: true})
		if errStream != nil {
			t.Fatalf("expected streamErrorResult, got direct err: %v", errStream)
		}
		chunk, ok := <-streamRes.Chunks
		if !ok {
			t.Fatal("expected error chunk on bootstrap failure")
		}
		assertIssue7521PoolCooldownError(t, chunk.Err, "bootstrap second limited", shortHint)
	})
}

func TestExecute429LeavesOrdinary429UnchangedWhenCandidateAvailable(t *testing.T) {
	first := newIssue7521Auth("7521-avail-a")
	second := newIssue7521Auth("7521-avail-b")
	hint := time.Hour

	exec := &customStreamMockExecutor{
		identifier: "claude",
		mockCustomErrorExecutor: mockCustomErrorExecutor{
			identifier: "claude",
			executeFn: func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				cliproxyexecutor.MarkUpstreamAttempt(ctx)
				return cliproxyexecutor.Response{}, newIssue7521Upstream429("single attempt 429", hint)
			},
		},
	}
	manager := newIssue7521Manager(t, nil, exec, first, second)
	// Stop after 1 credential attempt with 0 retries so the second credential remains unblocked.
	manager.SetRetryConfig(0, 0, 1)

	_, errExec := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: issue7521PoolModel}, cliproxyexecutor.Options{})
	if errExec == nil {
		t.Fatal("expected 429 error")
	}
	if got := statusCodeFromError(errExec); got != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", got)
	}
	var marker interface{ IsModelCooldown() bool }
	if errors.As(errExec, &marker) && marker != nil && marker.IsModelCooldown() {
		t.Fatalf("ordinary 429 must not report IsModelCooldown() when another candidate is still available: %T (%v)", errExec, errExec)
	}
}

func TestExecuteLastCredential429IgnoresZeroWeightCandidateUnderWeightedSelector(t *testing.T) {
	active := newIssue7521Auth("7521-weight-active")
	drained := newIssue7521Auth("7521-weight-zero")
	drained.Attributes["weight"] = "0"
	hint := 70 * time.Minute

	exec := &customStreamMockExecutor{
		identifier: "claude",
		mockCustomErrorExecutor: mockCustomErrorExecutor{
			identifier: "claude",
			executeFn: func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				cliproxyexecutor.MarkUpstreamAttempt(ctx)
				return cliproxyexecutor.Response{}, newIssue7521Upstream429("active credential exhausted", hint)
			},
		},
	}
	selector := NewSessionAffinitySelector(&WeightedRoundRobinSelector{})
	manager := newIssue7521Manager(t, selector, exec, active, drained)

	_, errExec := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: issue7521PoolModel}, cliproxyexecutor.Options{})
	assertIssue7521PoolCooldownError(t, errExec, "active credential exhausted", hint)
}

func TestAnnotatePoolCooldownSelectionFallbackAndUnblockedGuard(t *testing.T) {
	now := time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
	fallbackDeadline := now.Add(42 * time.Minute)
	lastAttemptHint := 3 * time.Hour
	baseErr := withTrustedRetryAfter(newIssue7521Upstream429("upstream rate limited", lastAttemptHint), &lastAttemptHint)

	t.Run("falls back to selection cooldown when no candidate unblocked", func(t *testing.T) {
		manager := NewManager(nil, nil, nil)
		wrapped := manager.annotatePoolCooldownTerminalErrorAt(
			context.Background(),
			[]string{"claude"},
			issue7521PoolModel,
			cliproxyexecutor.Options{},
			baseErr,
			fallbackDeadline,
			now,
		)
		poolErr, ok := wrapped.(*poolCooldownTerminalError)
		if !ok || poolErr == nil {
			t.Fatalf("wrapped = %T, want *poolCooldownTerminalError", wrapped)
		}
		if !poolErr.deadline.Equal(fallbackDeadline) {
			t.Fatalf("deadline = %v, want %v", poolErr.deadline, fallbackDeadline)
		}
		if !poolErr.IsModelCooldown() {
			t.Fatal("IsModelCooldown() = false, want true")
		}
	})

	t.Run("does not fall back when an eligible candidate is unblocked", func(t *testing.T) {
		avail := newIssue7521Auth("7521-fallback-avail")
		exec := &customStreamMockExecutor{identifier: "claude"}
		manager := newIssue7521Manager(t, nil, exec, avail)

		got := manager.annotatePoolCooldownTerminalErrorAt(
			context.Background(),
			[]string{"claude"},
			issue7521PoolModel,
			cliproxyexecutor.Options{},
			baseErr,
			fallbackDeadline,
			now,
		)
		var marker interface{ IsModelCooldown() bool }
		if errors.As(got, &marker) && marker != nil && marker.IsModelCooldown() {
			t.Fatalf("must not wrap as model cooldown when a candidate is unblocked: %T", got)
		}
	})

	t.Run("expired pool deadline suppresses raw header and falls back for retryAfterFromError", func(t *testing.T) {
		cause := headerCarryingError{headers: http.Header{"X-Trace": {"1"}, "Retry-After": {"999"}}}
		expired := &poolCooldownTerminalError{
			cause:    cause,
			deadline: time.Now().Add(-time.Second),
		}
		if !HasTrustedRetryDeadline(expired) {
			t.Fatal("HasTrustedRetryDeadline(expired) = false, want true")
		}
		if got := SafeResponseHeaders(expired).Get("Retry-After"); got != "" {
			t.Fatalf("SafeResponseHeaders(expired) Retry-After = %q, want empty", got)
		}
		if got := expired.Headers().Get("X-Trace"); got != "1" {
			t.Fatalf("Headers() X-Trace = %q, want 1", got)
		}
		agg := 2 * time.Second
		homeTerminal := markHomeRetryRoundExhausted(&poolCooldownTerminalError{
			cause:    cause,
			deadline: time.Now().Add(time.Hour),
		}, &agg, false)
		if got := SafeResponseHeaders(homeTerminal).Get("Retry-After"); got != "2" {
			t.Fatalf("Home aggregate Retry-After = %q, want 2", got)
		}
	})
}

// Regression for the review finding: a short transient non-cooldown block on a
// sibling credential must pull the pool reset forward instead of being ignored,
// so the wrapped terminal 429 never advertises a longer wait than the pool's
// earliest auto-recovery.
func TestExecutePoolCooldownIncludesTransientNonCooldownBlock(t *testing.T) {
	longQuotaCooldown := 3 * time.Hour
	shortTransient := 90 * time.Second

	quotaAuth := newIssue7521Auth("7521-mixed-quota")
	transientAuth := newIssue7521Auth("7521-mixed-transient")
	execAuth := newIssue7521Auth("7521-mixed-exec")

	exec := &customStreamMockExecutor{
		identifier: "claude",
		mockCustomErrorExecutor: mockCustomErrorExecutor{
			identifier: "claude",
			executeFn: func(ctx context.Context, _ *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
				cliproxyexecutor.MarkUpstreamAttempt(ctx)
				return cliproxyexecutor.Response{}, newIssue7521Upstream429("last credential rate limited", 2*time.Hour)
			},
		},
	}
	manager := newIssue7521Manager(t, nil, exec, quotaAuth, transientAuth, execAuth)

	// Credential A: genuine quota cooldown recovering in 3h.
	manager.MarkResult(context.Background(), Result{
		AuthID:          quotaAuth.ID,
		Provider:        "claude",
		Model:           issue7521PoolModel,
		Success:         false,
		RetryAfter:      &longQuotaCooldown,
		CredentialScope: true,
		Error:           &Error{HTTPStatus: http.StatusTooManyRequests, Message: "quota exhausted"},
	})

	// Credential B: transient non-cooldown block recovering in 90s. Recording
	// a transient_rate_limit result (non-credential scope) marks only the
	// reporting model unavailable without a quota label, so the scan
	// classifies it as blockReasonOther rather than blockReasonCooldown.
	manager.MarkResult(context.Background(), Result{
		AuthID:     transientAuth.ID,
		Provider:   "claude",
		Model:      issue7521PoolModel,
		Success:    false,
		RetryAfter: &shortTransient,
		Error: &Error{
			Code:       ErrorCodeTransientRateLimit,
			HTTPStatus: http.StatusTooManyRequests,
			Message:    "transient capacity rejection",
		},
	})

	// Credential C is the only unblocked candidate; it fails and joins the
	// pool cooldown. The pool's earliest recovery is B's +90s, not A's +3h nor
	// the attempt's own 2h hint.
	_, errExec := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: issue7521PoolModel}, cliproxyexecutor.Options{})
	assertIssue7521PoolCooldownError(t, errExec, "last credential rate limited", shortTransient)
}
