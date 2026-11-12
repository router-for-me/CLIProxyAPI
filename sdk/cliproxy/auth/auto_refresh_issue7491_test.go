package auth

import (
	"context"
	"testing"
	"time"
)

// Regression test for issue #7491: an auth whose refresh token is unchanged and
// was rejected upstream, with an expired access token and without being
// disabled, used to be scheduled for an immediate refresh (zero delay) over and
// over, so the auto refresh loop spun on the CPU. The scheduler must treat the
// credential as unschedulable until the credential is updated instead.

func newIssue7491RejectedAuth(now time.Time) *Auth {
	return &Auth{
		ID:       "issue7491-spin",
		Provider: "issue7491",
		Status:   StatusActive,
		Metadata: map[string]any{
			"access_token":  "expired-access",
			"refresh_token": "rejected-refresh",
			"expired":       now.Add(-time.Hour).Format(time.RFC3339),
		},
		RequiresSignIn:       true,
		RejectedRefreshToken: "rejected-refresh",
		LastRefreshedAt:      now.Add(-2 * time.Hour),
		NextRefreshAfter:     time.Time{},
		Disabled:             false,
	}
}

func newIssue7491Manager(t *testing.T, a *Auth) (*Manager, *authAutoRefreshLoop) {
	t.Helper()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	if _, errRegister := manager.Register(context.Background(), a); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	loop := newAuthAutoRefreshLoop(manager, refreshCheckInterval, 1)
	manager.refreshLoop = loop
	return manager, loop
}

func TestIssue7491RejectedRefreshTokenWithExpiredAccessTokenIsUnschedulable(t *testing.T) {
	now := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	a := newIssue7491RejectedAuth(now)
	manager, _ := newIssue7491Manager(t, a)

	stored, ok := manager.GetByID(a.ID)
	if !ok || stored == nil {
		t.Fatal("credential missing after register")
	}
	if !authHasUnchangedRejectedRefreshToken(stored) {
		t.Fatalf("expected unchanged rejected refresh token state: %+v", stored)
	}
	if stored.HasValidAccessToken(now) {
		t.Fatal("access token must be expired for this regression")
	}

	if next, due := manager.nextRefreshCheckAt(now, stored, refreshCheckInterval); due || !next.IsZero() {
		t.Fatalf("nextRefreshCheckAt = (%v, %v), want zero-due for rejected refresh token with expired access token", next, due)
	}
	if next, due := nextRefreshCheckAt(now, stored, refreshCheckInterval); due || !next.IsZero() {
		t.Fatalf("nextRefreshCheckAt = (%v, %v), want zero-due", next, due)
	}
	if manager.shouldRefresh(stored, now) {
		t.Fatal("shouldRefresh = true, want false to avoid the zero-delay refresh spin")
	}
}

func TestIssue7491AutoRefreshLoopDoesNotRescheduleRejectedAuth(t *testing.T) {
	now := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	a := newIssue7491RejectedAuth(now)
	manager, loop := newIssue7491Manager(t, a)
	ctx := context.Background()

	loop.rebuild(now)
	if got := len(loop.jobs); got != 0 {
		t.Fatalf("rebuild scheduled %d jobs for rejected credential, want 0", got)
	}

	// Even if a job was already pending (e.g. queued before the rejection was
	// recorded), finishing it with a zero retry time must not make the loop
	// pick the auth up again immediately.
	registered, _ := manager.GetByID(a.ID)
	job := manager.markRefreshPending(loop, registered.ID, registered.RegistrationEpoch, now)
	if job == nil {
		t.Fatal("expected pending refresh job")
	}
	if !manager.beginRefreshJob(ctx, job) {
		t.Fatal("expected refresh job to start")
	}
	if _, errRefresh := manager.refreshAuthForRequestAtEpoch(ctx, job.id, "", job.registrationEpoch); errRefresh == nil {
		t.Fatal("expected refresh of rejected credential to fail")
	}
	manager.finishRefreshJob(job, time.Time{}, false)

	loop.rebuild(now)
	if got := len(loop.jobs); got != 0 {
		t.Fatalf("after finishRefreshJob with zero retryAt, rebuild scheduled %d jobs, want 0 (no zero-delay spin)", got)
	}
}
