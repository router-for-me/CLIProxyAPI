package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// Review round 4 regressions: sibling-model quota must not re-impose the
// quota wait floor on a transient model, positive transient hints schedule
// by their actual remaining time, and an expired trusted wrapper must not
// surface a negative hint.

func issue7017WaitSetup(t *testing.T, auth *Auth, model string) (*Manager, authSelectionEligibility) {
	t.Helper()
	withQuotaCooldownEnabled(t)
	manager := NewManager(nil, nil, nil)
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: model}, {ID: "gpt-6-astra"}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), auth); errRegister != nil {
		t.Fatalf("Register returned error: %v", errRegister)
	}
	return manager, authSelectionEligibilityForRequest(context.Background(), cliproxyexecutor.Options{})
}

func TestIssue7017SiblingQuotaDoesNotBlockTransientZeroWait(t *testing.T) {
	model := "gpt-6.1-sol"
	future := time.Now().Add(time.Hour)
	auth := &Auth{
		ID:       "turn-sibling-quota",
		Provider: "codex",
		// Auth-level aggregate mirrors the sibling model quota.
		Quota: QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: future, BackoffLevel: 1},
		ModelStates: map[string]*ModelState{
			model: {
				Status:    StatusError,
				LastError: &Error{Code: ErrorCodeTransientRateLimit, Message: issue7017TurnBody(), HTTPStatus: http.StatusTooManyRequests},
			},
			"gpt-6-astra": {
				Status:         StatusError,
				Unavailable:    true,
				NextRetryAfter: future,
				Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: future, BackoffLevel: 1},
			},
		},
	}
	manager, eligibility := issue7017WaitSetup(t, auth, model)

	attempted := map[string]struct{}{auth.ID: {}}
	wait, found := manager.closestCooldownWaitWithAttempted([]string{"codex"}, model, 0, eligibility, "", 5, http.StatusTooManyRequests, attempted)
	if !found || wait != 0 {
		t.Fatalf("sibling quota must not block transient zero-wait: wait=%v found=%t", wait, found)
	}
}

func TestIssue7017PositiveTransientHintSkipsQuotaFloor(t *testing.T) {
	model := "gpt-6.1-sol"
	next := time.Now().Add(8 * time.Second)
	auth := &Auth{
		ID:       "turn-positive-hint",
		Provider: "codex",
		ModelStates: map[string]*ModelState{
			model: {
				Status:         StatusError,
				Unavailable:    true,
				NextRetryAfter: next,
				LastError:      &Error{Code: ErrorCodeTransientRateLimit, Message: issue7017TurnBody(), HTTPStatus: http.StatusTooManyRequests},
			},
		},
	}
	manager, eligibility := issue7017WaitSetup(t, auth, model)

	attempted := map[string]struct{}{auth.ID: {}}
	wait, found := manager.closestCooldownWaitWithAttempted([]string{"codex"}, model, 0, eligibility, "", 5, http.StatusTooManyRequests, attempted)
	if !found {
		t.Fatal("expected a retry candidate")
	}
	if wait <= 0 || wait > 8*time.Second {
		t.Fatalf("positive transient hint must schedule by actual remaining time, wait=%v want (0, 8s]", wait)
	}
}

func TestIssue7017ExpiredTrustedWrapperFallsBackToProviderHint(t *testing.T) {
	hint := 256 * time.Second
	inner := issue7017HintError{msg: issue7017TurnBody(), hint: &hint}
	err := withTrustedRetryAfter(inner, &hint)
	wrapped, ok := err.(*trustedRetryAfterError)
	if !ok {
		t.Fatalf("expected trusted wrapper, got %T", err)
	}
	wrapped.deadline = time.Now().Add(-time.Second)

	got := retryAfterFromError(err)
	if got == nil || *got != hint {
		t.Fatalf("expired wrapper must fall back to provider hint, got %v want %v", got, hint)
	}
}
