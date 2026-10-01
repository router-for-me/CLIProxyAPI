package claudemaster

import (
	"net/http"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func backendAPIBackupTestSelector(t *testing.T) (*backendSeriesSelector, []*coreauth.Auth, time.Time) {
	t.Helper()
	reset := time.Date(2026, time.October, 4, 12, 0, 0, 0, time.UTC)
	selector := &backendSeriesSelector{
		authIDs: []string{"subscription-a", "subscription-b"}, backupAuthID: "api-backup", provider: "claude",
		now: func() time.Time { return reset.Add(-24 * time.Hour) },
	}
	t.Cleanup(selector.Stop)
	return selector, []*coreauth.Auth{
		backendSeriesTestAuth("api-backup", "claude"),
		backendSeriesTestAuth("subscription-b", "claude"),
		backendSeriesTestAuth("subscription-a", "claude"),
	}, reset
}

func backendAPIBackupTestOptions(session string, opaque bool) coreexecutor.Options {
	body := []byte(`{"model":"claude-test","messages":[{"role":"user","content":"hello"}]}`)
	if opaque {
		body = []byte(`{"model":"claude-test","messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"private","signature":"opaque-state"}]}]}`)
	}
	return coreexecutor.Options{
		Headers:         http.Header{"X-Claude-Code-Session-Id": {session}},
		OriginalRequest: body,
		Metadata:        map[string]any{coreexecutor.RequestedModelMetadataKey: "claude-test"},
	}
}

func requireBackendAPIBackupPick(t *testing.T, selector *backendSeriesSelector, opts coreexecutor.Options, auths []*coreauth.Auth, wantID string) {
	t.Helper()
	auth, err := selector.Pick(t.Context(), "mixed", backendAuthSelectionModel, opts, auths)
	if err != nil || auth == nil || auth.ID != wantID {
		t.Fatalf("Pick() = %#v, %v, want %q", auth, err, wantID)
	}
}

func TestBackendAPIBackupIsLastAfterSubscriptionCapacity(t *testing.T) {
	for _, tc := range []struct {
		name     string
		usedA    float64
		usedB    float64
		unknownB bool
		want     string
	}{
		{name: "normal_capacity", usedA: 0.2, usedB: 0.4, want: "subscription-a"},
		{name: "first_exhausted", usedA: 1, usedB: 0.4, want: "subscription-b"},
		{name: "first_reserved", usedA: 0.95, usedB: 0.4, want: "subscription-b"},
		{name: "both_reserved", usedA: 0.95, usedB: 0.96, want: "subscription-a"},
		{name: "remaining_reserve", usedA: 1, usedB: 0.96, want: "subscription-b"},
		{name: "unknown_is_not_exhausted", usedA: 1, unknownB: true, want: "subscription-b"},
		{name: "all_exhausted", usedA: 1, usedB: 1, want: "api-backup"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selector, auths, reset := backendAPIBackupTestSelector(t)
			selector.observeQuota("subscription-a", backendWeeklyQuota{known: true, used: tc.usedA, resetsAt: reset})
			if !tc.unknownB {
				selector.observeQuota("subscription-b", backendWeeklyQuota{known: true, used: tc.usedB, resetsAt: reset.Add(24 * time.Hour)})
			}
			requireBackendAPIBackupPick(t, selector, backendAPIBackupTestOptions("last-resort", false), auths, tc.want)
		})
	}
}

func TestBackendAPIBackupDoesNotTreatMissingSubscriptionAsExhausted(t *testing.T) {
	selector, auths, _ := backendAPIBackupTestSelector(t)
	if got, err := selector.Pick(t.Context(), "claude", backendAuthSelectionModel, backendAPIBackupTestOptions("missing", false), auths[:1]); err == nil || got != nil {
		t.Fatalf("missing credentials incorrectly permitted API spending: got=%#v err=%v", got, err)
	}
}

func TestBackendAPIBackupDoesNotReplaceBoundNonQuotaFailure(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  *coreauth.Error
	}{
		{name: "auth", err: &coreauth.Error{HTTPStatus: http.StatusUnauthorized}},
		{name: "model", err: &coreauth.Error{HTTPStatus: http.StatusNotFound}},
		{name: "transport", err: &coreauth.Error{Code: "transport_error"}},
		{name: "request", err: &coreauth.Error{Code: coreauth.ErrorCodeRequestScoped, HTTPStatus: http.StatusTooManyRequests}},
		{name: "forced_request_cooldown", err: &coreauth.Error{Code: coreauth.ErrorCodeForceCooldown, HTTPStatus: http.StatusTooManyRequests}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selector, auths, reset := backendAPIBackupTestSelector(t)
			opts := backendAPIBackupTestOptions("bound-failure", false)
			requireBackendAPIBackupPick(t, selector, opts, auths, "subscription-a")
			// A failing response can also report the final weekly watermark.
			// Quota headers must not convert an unrelated error into a paid retry.
			for _, id := range selector.authIDs {
				selector.observeQuota(id, backendWeeklyQuota{known: true, used: 1, resetsAt: reset})
			}
			selector.OnResult(coreauth.Result{AuthID: "subscription-a", Model: "claude-test", CredentialScope: true, Error: tc.err})
			if got, err := selector.Pick(t.Context(), "claude", backendAuthSelectionModel, opts, auths[:2]); err == nil || got != nil {
				t.Fatalf("non-quota failure retried on API backup: got=%#v err=%v", got, err)
			}
		})
	}
}

func TestBackendAPIBackupNeverMovesOpaqueSubscriptionContinuation(t *testing.T) {
	selector, auths, reset := backendAPIBackupTestSelector(t)
	plain := backendAPIBackupTestOptions("bound-opaque", false)
	requireBackendAPIBackupPick(t, selector, plain, auths, "subscription-a")
	for _, id := range selector.authIDs {
		selector.observeQuota(id, backendWeeklyQuota{known: true, used: 1, resetsAt: reset})
	}
	opaque := backendAPIBackupTestOptions("bound-opaque", true)
	if got, err := selector.Pick(t.Context(), "claude", backendAuthSelectionModel, opaque, auths); err == nil || got != nil {
		t.Fatalf("opaque exhausted subscription did not stop on its bound account: got=%#v err=%v", got, err)
	}
	if got, err := selector.Pick(t.Context(), "claude", backendAuthSelectionModel, opaque, auths[:2]); err == nil || got != nil {
		t.Fatalf("opaque state escaped unavailable subscription: got=%#v err=%v", got, err)
	}
	if got, err := selector.Pick(t.Context(), "claude", backendAuthSelectionModel, backendAPIBackupTestOptions("unbound-opaque", true), auths); err == nil || got != nil {
		t.Fatalf("unbound opaque state incorrectly selected API backup: got=%#v err=%v", got, err)
	}
}

func TestBackendAPIBackupReturnsCleanWorkAfterResetButKeepsOpaqueState(t *testing.T) {
	selector, auths, reset := backendAPIBackupTestSelector(t)
	now := reset.Add(-time.Minute)
	selector.now = func() time.Time { return now }
	selector.observeQuota("subscription-a", backendWeeklyQuota{known: true, used: 1, resetsAt: reset})
	selector.observeQuota("subscription-b", backendWeeklyQuota{known: true, used: 1, resetsAt: reset.Add(24 * time.Hour)})
	plain := backendAPIBackupTestOptions("bound-api", false)
	requireBackendAPIBackupPick(t, selector, plain, auths, "api-backup")
	now = reset
	requireBackendAPIBackupPick(t, selector, backendAPIBackupTestOptions("bound-api", true), auths, "api-backup")
	if got, err := selector.Pick(t.Context(), "claude", backendAuthSelectionModel, backendAPIBackupTestOptions("bound-api", true), auths[1:]); err == nil || got != nil {
		t.Fatalf("opaque API continuation moved to subscriptions when API unavailable: got=%#v err=%v", got, err)
	}
	child := backendAPIBackupTestOptions("bound-api", true)
	child.Headers.Set("X-Claude-Code-Agent-Id", "child-reviewer")
	child.Headers.Set("X-Claude-Code-Parent-Agent-Id", "main")
	requireBackendAPIBackupPick(t, selector, child, auths, "api-backup")
	child.OriginalRequest = plain.OriginalRequest
	requireBackendAPIBackupPick(t, selector, child, auths, "subscription-a")
	requireBackendAPIBackupPick(t, selector, plain, auths, "subscription-a")
}

func TestBackendAPIBackupDoesNotPolluteSubscriptionQuota(t *testing.T) {
	selector, _, reset := backendAPIBackupTestSelector(t)
	selector.observeQuota("api-backup", backendWeeklyQuota{known: true, used: 1, resetsAt: reset})
	selector.OnResult(backendSeriesQuotaResult("api-backup"))
	selector.mu.Lock()
	_, observed := selector.quota["api-backup"]
	selector.mu.Unlock()
	if observed {
		t.Fatal("API rate limit entered the subscription weekly quota ledger")
	}
}

func TestBackendAPIBackupReleasesFiveHourQuotaWithoutResettingWeeklyUsage(t *testing.T) {
	selector, auths, reset := backendAPIBackupTestSelector(t)
	now := reset.Add(-24 * time.Hour)
	selector.now = func() time.Time { return now }
	for _, id := range selector.authIDs {
		selector.observeQuota(id, backendWeeklyQuota{known: true, used: 0.2, resetsAt: reset})
	}
	opts := backendAPIBackupTestOptions("five-hour-reset", false)
	requireBackendAPIBackupPick(t, selector, opts, auths, "subscription-a")
	block := func(id string, duration time.Duration) {
		t.Helper()
		result := backendSeriesQuotaResult(id)
		result.Model = "claude-test"
		result.RetryAfter = &duration
		selector.OnResult(result)
	}
	block("subscription-a", time.Hour)
	requireBackendAPIBackupPick(t, selector, opts, auths, "subscription-b")
	block("subscription-b", 2*time.Hour)
	requireBackendAPIBackupPick(t, selector, opts, auths, "api-backup")
	selector.mu.Lock()
	for _, id := range selector.authIDs {
		quota := selector.quota[id]
		if !quota.known || quota.used != 0.2 || !quota.resetsAt.Equal(reset) {
			selector.mu.Unlock()
			t.Fatalf("short-window rejection poisoned weekly meter for %q: %+v", id, quota)
		}
	}
	selector.mu.Unlock()
	now = now.Add(time.Hour)
	requireBackendAPIBackupPick(t, selector, backendAPIBackupTestOptions("five-hour-reset", true), auths, "api-backup")
	requireBackendAPIBackupPick(t, selector, opts, auths, "subscription-a")
}
