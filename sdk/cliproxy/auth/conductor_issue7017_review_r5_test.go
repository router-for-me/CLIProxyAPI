package auth

import (
	"net/http"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// Review round 5 regressions: an expired own-model quota marker is history,
// not a live gate, and every safe-header carrier must outrank a raw
// passthrough Retry-After.

func TestIssue7017ExpiredOwnQuotaDoesNotBlockTransientRetry(t *testing.T) {
	model := "gpt-6.1-sol"
	expired := time.Now().Add(-time.Hour)
	next := time.Now().Add(8 * time.Second)
	auth := &Auth{
		ID:       "turn-expired-own-quota",
		Provider: "codex",
		ModelStates: map[string]*ModelState{
			model: {
				Status:         StatusError,
				Unavailable:    true,
				NextRetryAfter: next,
				Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: expired, BackoffLevel: 1},
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
		t.Fatalf("expired own quota must not re-impose the floor: wait=%v want (0, 8s]", wait)
	}
}

func TestIssue7017LiveOwnQuotaKeepsFloorOverTransient(t *testing.T) {
	model := "gpt-6.1-sol"
	future := time.Now().Add(3 * time.Second)
	auth := &Auth{
		ID:       "turn-live-own-quota",
		Provider: "codex",
		ModelStates: map[string]*ModelState{
			model: {
				Status:         StatusError,
				Unavailable:    true,
				NextRetryAfter: future,
				Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: future, BackoffLevel: 1},
				LastError:      &Error{Code: ErrorCodeTransientRateLimit, Message: issue7017TurnBody(), HTTPStatus: http.StatusTooManyRequests},
			},
		},
	}
	manager, eligibility := issue7017WaitSetup(t, auth, model)

	attempted := map[string]struct{}{auth.ID: {}}
	wait, found := manager.closestCooldownWaitWithAttempted([]string{"codex"}, model, 0, eligibility, "", 5, http.StatusTooManyRequests, attempted)
	if !found || wait < minQuotaCooldownFloor {
		t.Fatalf("live own quota must keep the floor: wait=%v found=%t want >= %v", wait, found, minQuotaCooldownFloor)
	}
}

type issue7017SafeHeaderError struct{ retryAfter string }

func (e issue7017SafeHeaderError) Error() string { return "plugin failure" }

func (e issue7017SafeHeaderError) SafeResponseHeaders() http.Header {
	return http.Header{"Retry-After": []string{e.retryAfter}}
}

func TestHasTrustedRetryDeadlineCoversSafeHeaderCarriers(t *testing.T) {
	if !HasTrustedRetryDeadline(newHomeConcurrencyBusyError(&Error{Message: "busy", HTTPStatus: http.StatusTooManyRequests}, time.Second)) {
		t.Fatal("Home concurrency busy must report a trusted deadline")
	}
	dispatchCooldown := &homeDispatchRetryAfterError{cause: &Error{Message: "cooling", HTTPStatus: http.StatusTooManyRequests}, retryAfter: time.Second}
	if !HasTrustedRetryDeadline(dispatchCooldown) {
		t.Fatal("Home dispatch cooldown must report a trusted deadline")
	}
	if !HasTrustedRetryDeadline(issue7017SafeHeaderError{retryAfter: "1"}) {
		t.Fatal("generic safe-header carriers must report a trusted deadline")
	}
	opts := cliproxyexecutor.Options{}
	_ = opts
}
