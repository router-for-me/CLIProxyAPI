package claudemaster

import (
	"net/http"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestBackendSeriesSelectorShortQuotaBlockPreservesWeeklyMeter(t *testing.T) {
	now := time.Date(2026, time.October, 1, 12, 0, 0, 0, time.UTC)
	selector := &backendSeriesSelector{
		authIDs: []string{"profile-a", "profile-b"}, provider: "claude",
		now: func() time.Time { return now },
	}
	t.Cleanup(selector.Stop)
	reset := now.Add(3 * 24 * time.Hour)
	selector.observeQuota("profile-a", backendWeeklyQuota{known: true, used: 0.2, resetsAt: reset})
	selector.observeQuota("profile-b", backendWeeklyQuota{known: true, used: 0.4, resetsAt: reset.Add(time.Hour)})
	auths := []*coreauth.Auth{backendSeriesTestAuth("profile-a", "claude"), backendSeriesTestAuth("profile-b", "claude")}
	requireBackendSeriesPick(t, selector, "claude", auths, "profile-a")
	retryAfter := 5 * time.Hour
	result := backendSeriesQuotaResult("profile-a")
	result.RetryAfter = &retryAfter
	selector.OnResult(result)
	if quota := selector.quota["profile-a"]; quota.used != 0.2 || !quota.resetsAt.Equal(reset) {
		t.Fatalf("short quota window overwrote weekly allocation: %+v", quota)
	}
	requireBackendSeriesPick(t, selector, "claude", auths, "profile-b")
	now = now.Add(retryAfter - time.Nanosecond)
	requireBackendSeriesPick(t, selector, "claude", auths, "profile-b")
	now = now.Add(time.Nanosecond)
	requireBackendSeriesPick(t, selector, "claude", auths, "profile-a")
	if _, blocked := selector.quotaBlockedUntil["profile-a"]; blocked {
		t.Fatal("expired short quota block was retained")
	}
}

func TestBackendSeriesSelectorShortResetDoesNotClearMeasuredWeeklyExhaustion(t *testing.T) {
	now := time.Date(2026, time.October, 1, 12, 0, 0, 0, time.UTC)
	selector := &backendSeriesSelector{authIDs: []string{"profile-a"}, provider: "claude", now: func() time.Time { return now }}
	t.Cleanup(selector.Stop)
	reset := now.Add(3 * 24 * time.Hour)
	selector.observeQuota("profile-a", backendWeeklyQuota{known: true, used: 1, resetsAt: reset})
	retryAfter := time.Hour
	result := backendSeriesQuotaResult("profile-a")
	result.RetryAfter = &retryAfter
	selector.OnResult(result)
	now = now.Add(retryAfter)
	auths := []*coreauth.Auth{backendSeriesTestAuth("profile-a", "claude")}
	if got, err := selector.Pick(t.Context(), "claude", "", coreexecutor.Options{}, auths); got != nil || err == nil {
		t.Fatalf("weekly-exhausted credential reopened at short reset: %#v, %v", got, err)
	}
	if quota := selector.quota["profile-a"]; quota.used != 1 || !quota.resetsAt.Equal(reset) {
		t.Fatalf("weekly allocation changed at short reset: %+v", quota)
	}
	now = reset
	requireBackendSeriesPick(t, selector, "claude", auths, "profile-a")
}

func TestBackendSeriesSelectorUnknownQuotaUsesRecoverableBackoff(t *testing.T) {
	now := time.Date(2026, time.October, 1, 12, 0, 0, 0, time.UTC)
	selector := &backendSeriesSelector{authIDs: []string{"profile-a"}, provider: "claude", now: func() time.Time { return now }}
	t.Cleanup(selector.Stop)
	auths := []*coreauth.Auth{backendSeriesTestAuth("profile-a", "claude")}
	result := backendSeriesQuotaResult("profile-a")
	for attempt := 0; attempt < 14; attempt++ {
		selector.OnResult(result)
		wantCooldown := 30 * time.Minute
		if attempt < 11 {
			wantCooldown = time.Second * time.Duration(1<<attempt)
		}
		deadline := selector.quotaBlockedUntil["profile-a"]
		if !deadline.Equal(now.Add(wantCooldown)) {
			t.Fatalf("attempt %d deadline = %v, want %v", attempt, deadline, now.Add(wantCooldown))
		}
		level := selector.quotaBackoffLevel["profile-a"]
		selector.OnResult(result)
		if !selector.quotaBlockedUntil["profile-a"].Equal(deadline) || selector.quotaBackoffLevel["profile-a"] != level {
			t.Fatal("concurrent rejection escalated an active backoff window")
		}
		if quota := selector.quota["profile-a"]; quota.known {
			t.Fatalf("unknown weekly allocation became exhausted: %+v", quota)
		}
		if got, err := selector.Pick(t.Context(), "claude", "", coreexecutor.Options{}, auths); got != nil || err == nil {
			t.Fatalf("active quota backoff accepted credential: %#v, %v", got, err)
		}
		now = deadline
		requireBackendSeriesPick(t, selector, "claude", auths, "profile-a")
	}
	selector.OnResult(coreauth.Result{AuthID: "profile-a", Success: true})
	selector.OnResult(result)
	if deadline := selector.quotaBlockedUntil["profile-a"]; !deadline.Equal(now.Add(time.Second)) {
		t.Fatalf("success did not reset the backoff ladder: %v", deadline)
	}
}

func TestBackendSeriesSelectorExplicitQuotaHintUsesManagerFloor(t *testing.T) {
	now := time.Date(2026, time.October, 1, 12, 0, 0, 0, time.UTC)
	selector := &backendSeriesSelector{authIDs: []string{"profile-a"}, provider: "claude", now: func() time.Time { return now }}
	t.Cleanup(selector.Stop)
	retryAfter := time.Millisecond
	result := backendSeriesQuotaResult("profile-a")
	result.RetryAfter = &retryAfter
	selector.OnResult(result)
	if until := selector.quotaBlockedUntil["profile-a"]; !until.Equal(now.Add(10 * time.Second)) {
		t.Fatalf("explicit quota hint deadline = %v, want manager-aligned ten-second floor", until)
	}
}

func TestBackendSeriesSelectorDelayedSuccessRetainsActiveQuotaBlock(t *testing.T) {
	now := time.Date(2026, time.October, 1, 12, 0, 0, 0, time.UTC)
	selector := &backendSeriesSelector{authIDs: []string{"profile-a"}, provider: "claude", now: func() time.Time { return now }}
	t.Cleanup(selector.Stop)
	result := backendSeriesQuotaResult("profile-a")
	selector.OnResult(result)
	deadline := selector.quotaBlockedUntil["profile-a"]
	now = now.Add(time.Millisecond)
	selector.OnResult(coreauth.Result{AuthID: "profile-a", Success: true})
	if !selector.quotaBlockedUntil["profile-a"].Equal(deadline) || selector.quotaBackoffLevel["profile-a"] != 1 {
		t.Fatal("delayed in-flight success canceled a newer credential quota rejection")
	}
	auths := []*coreauth.Auth{backendSeriesTestAuth("profile-a", "claude")}
	if got, err := selector.Pick(t.Context(), "claude", "", coreexecutor.Options{}, auths); got != nil || err == nil {
		t.Fatalf("delayed success reopened an active cooldown: %#v, %v", got, err)
	}
	now = deadline
	selector.OnResult(coreauth.Result{AuthID: "profile-a", Success: true})
	if _, blocked := selector.quotaBlockedUntil["profile-a"]; blocked || selector.quotaBackoffLevel["profile-a"] != 0 {
		t.Fatal("success after expiry did not reset cooldown state")
	}
	requireBackendSeriesPick(t, selector, "claude", auths, "profile-a")
}

func TestBackendSeriesSelectorReserveDoesNotMoveIntoShortQuotaBlock(t *testing.T) {
	now := time.Date(2026, time.October, 1, 12, 0, 0, 0, time.UTC)
	selector := &backendSeriesSelector{authIDs: []string{"profile-a", "profile-b"}, provider: "claude", now: func() time.Time { return now }}
	t.Cleanup(selector.Stop)
	reset := now.Add(3 * 24 * time.Hour)
	selector.observeQuota("profile-a", backendWeeklyQuota{known: true, used: 0.2, resetsAt: reset})
	selector.observeQuota("profile-b", backendWeeklyQuota{known: true, used: 0.4, resetsAt: reset.Add(time.Hour)})
	auths := []*coreauth.Auth{backendSeriesTestAuth("profile-a", "claude"), backendSeriesTestAuth("profile-b", "claude")}
	opts := coreexecutor.Options{Headers: http.Header{"X-Claude-Code-Session-Id": {"reserve-short-window"}}, OriginalRequest: []byte(`{"messages":[{"role":"user","content":"hello"}]}`)}
	if got, err := selector.Pick(t.Context(), "claude", "", opts, auths); err != nil || got == nil || got.ID != "profile-a" {
		t.Fatalf("initial binding = %#v, %v", got, err)
	}
	selector.observeQuota("profile-a", backendWeeklyQuota{known: true, used: 0.95, resetsAt: reset})
	selector.OnResult(backendSeriesQuotaResult("profile-b"))
	if got, err := selector.Pick(t.Context(), "claude", "", opts, auths); err != nil || got == nil || got.ID != "profile-a" {
		t.Fatalf("reserve moved into rejected subscription: %#v, %v", got, err)
	}
}
