package auth

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// signedOutAuth is a credential whose login was rejected with no refresh pending.
func signedOutAuth(t *testing.T, id string) *Auth {
	return registerExhaustionAuth(t, &Auth{ID: id, Provider: "claude", Status: StatusError, Unavailable: true,
		LastError: &Error{HTTPStatus: http.StatusUnauthorized, Message: "unauthorized"},
	})
}

// blockedAuth is a credential benched for a non-quota reason until retryAt.
func blockedAuth(t *testing.T, id string, retryAt time.Time) *Auth {
	return registerExhaustionAuth(t, &Auth{ID: id, Provider: "claude", Status: StatusActive, Unavailable: true, NextRetryAfter: retryAt})
}

func TestPoolQuotaLimit_CredentialsBesideSpentOnes(t *testing.T) {
	now := time.Now()
	reset := now.Add(time.Hour).Truncate(time.Second)
	tests := []struct {
		name  string
		other *Auth
		want  bool
	}{
		{"signed out", signedOutAuth(t, "blocked-signed-out"), true},
		{"blocked past the reset", blockedAuth(t, "blocked-past-reset", reset.Add(2*time.Hour)), true},
		{"blocked until before the reset", blockedAuth(t, "blocked-before-reset", now.Add(time.Minute)), false},
		{"blocked with no known end", blockedAuth(t, "blocked-no-end", time.Time{}), false},
		{"usable", registerExhaustionAuth(t, &Auth{ID: "blocked-usable", Provider: "claude", Status: StatusActive}), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			auths := []*Auth{claudeWindowAuth(t, "spent-"+tc.other.ID, "rejected", reset), tc.other}
			got, ok := poolQuotaLimit(auths, []string{"claude"}, exhaustionTestModel, now)
			if ok != tc.want || (ok && !got.ResetsAt.Equal(reset)) {
				t.Fatalf("got %+v %v, want usage limit %v resetting at %v", got, ok, tc.want, reset)
			}
		})
	}
}

func TestPoolQuotaLimit_NoQuotaEvidenceStaysRetryable(t *testing.T) {
	now := time.Now()
	auths := []*Auth{signedOutAuth(t, "only-signed-out"), blockedAuth(t, "only-blocked", now.Add(time.Hour))}
	if _, ok := poolQuotaLimit(auths, []string{"claude"}, exhaustionTestModel, now); ok {
		t.Fatal("a pool with no spent credential classified as a usage limit")
	}
}

// A spent pool beside a signed-out credential selects as unavailable (503). Clients must still get
// a usage-limit 429 with the reset in their headers.
func TestClassifyQuotaExhaustion_UnavailablePoolReportsReset(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	now := time.Now()
	reset := now.Add(time.Hour).Truncate(time.Second)
	spent := credentialLimitAuth(t, "unavailable-spent", map[string]string{
		claudeUnifiedSignalPrefix + "5h-Status": QuotaWindowStatusRejected,
		claudeUnifiedSignalPrefix + "5h-Reset":  strconv.FormatInt(reset.Unix(), 10),
	}, reset)
	for _, auth := range []*Auth{spent, signedOutAuth(t, "unavailable-signed-out")} {
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("Register: %v", errRegister)
		}
	}

	err := manager.classifyQuotaExhaustion(newAuthUnavailableErrorWithCause(reset, now, nil), []string{"claude"}, exhaustionTestModel)
	if statusCodeFromError(err) != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", statusCodeFromError(err))
	}
	// Claude's unified headers ride upstream's Headers(), which handlers send with passthrough-headers.
	headers := err.(interface{ Headers() http.Header }).Headers()
	if got := headers.Get(claudeUnifiedSignalPrefix + "Reset"); got != strconv.FormatInt(reset.Unix(), 10) {
		t.Fatalf("client reset header = %q, want %d (headers %v)", got, reset.Unix(), headers)
	}
	if headers.Get(HeaderShouldRetry) != "false" {
		t.Fatalf("%s = %q, want false", HeaderShouldRetry, headers.Get(HeaderShouldRetry))
	}

	// Selection fails before any upstream call; the executor only makes the provider routable.
	manager.RegisterExecutor(&issue6865Executor{id: "claude"})
	_, errExec := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: exhaustionTestModel}, cliproxyexecutor.Options{})
	assertUsageLimit(t, errExec, time.Until(reset))
}
