package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

// A credential that recovers before the advertised deadline must be retried by
// the next real request rather than waiting out a multi-day hint. These cases
// pin the bound, the escalation and the cases that must keep trusting upstream.
func TestBoundedQuotaCooldown(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	const week = 168 * time.Hour
	const bound = time.Hour

	for _, tc := range []struct {
		name      string
		hinted    time.Duration
		quota     QuotaState
		maximum   time.Duration
		want      time.Duration
		wantLevel int
	}{
		{
			name:   "short hint is trusted unchanged",
			hinted: 30 * time.Second, maximum: bound,
			want: 30 * time.Second,
		},
		{
			name:   "hint equal to the bound is trusted unchanged",
			hinted: bound, maximum: bound,
			want: bound,
		},
		{
			name:   "first long hint is bounded without escalating",
			hinted: week, maximum: bound,
			want: bound,
		},
		{
			name:   "expired window escalates once",
			hinted: week, maximum: bound,
			quota: QuotaState{Exceeded: true, NextRecoverAt: now.Add(-time.Minute)},
			want:  2 * bound, wantLevel: 1,
		},
		{
			name:   "open window does not escalate",
			hinted: week, maximum: bound,
			quota: QuotaState{Exceeded: true, NextRecoverAt: now.Add(30 * time.Minute), BackoffLevel: 1},
			want:  2 * bound, wantLevel: 1,
		},
		{
			name:   "escalation never exceeds the advertised deadline",
			hinted: 90 * time.Minute, maximum: bound,
			quota: QuotaState{Exceeded: true, NextRecoverAt: now.Add(-time.Minute), BackoffLevel: 8},
			want:  90 * time.Minute, wantLevel: 9,
		},
		{
			// One caller sets Exceeded before the deadline is recomputed, so a
			// set flag with no recorded deadline must not look like a prior window.
			name:   "exceeded flag without a recorded deadline does not escalate",
			hinted: week, maximum: bound,
			quota: QuotaState{Exceeded: true},
			want:  bound,
		},
		{
			name:   "disabled bound trusts the upstream hint",
			hinted: week, maximum: 0,
			want: week,
		},
		{
			name:   "level is clamped so the shift cannot overflow",
			hinted: week, maximum: bound,
			quota: QuotaState{Exceeded: true, NextRecoverAt: now.Add(-time.Minute), BackoffLevel: 1 << 20},
			want:  week, wantLevel: boundedQuotaMaxLevel,
		},
		{
			name:   "negative stored level is treated as zero",
			hinted: week, maximum: bound,
			quota: QuotaState{Exceeded: true, NextRecoverAt: now.Add(time.Minute), BackoffLevel: -3},
			want:  bound,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, level := boundedQuotaCooldown(tc.hinted, tc.quota, now, tc.maximum)
			if got != tc.want {
				t.Fatalf("cooldown = %s, want %s", got, tc.want)
			}
			if level != tc.wantLevel {
				t.Fatalf("backoff level = %d, want %d", level, tc.wantLevel)
			}
		})
	}
}

// The bound must never lengthen a wait. Whatever it returns, a client waits no
// longer than the upstream itself asked for.
func TestBoundedQuotaCooldownNeverExceedsTheHint(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	for _, hinted := range []time.Duration{
		time.Second, time.Minute, time.Hour, 24 * time.Hour, 168 * time.Hour,
	} {
		for level := 0; level <= boundedQuotaMaxLevel+2; level++ {
			quota := QuotaState{Exceeded: true, NextRecoverAt: now.Add(-time.Minute), BackoffLevel: level}
			got, _ := boundedQuotaCooldown(hinted, quota, now, time.Hour)
			if got > hinted {
				t.Fatalf("hinted=%s level=%d: cooldown %s exceeds the advertised deadline", hinted, level, got)
			}
			if got <= 0 {
				t.Fatalf("hinted=%s level=%d: cooldown %s is not positive", hinted, level, got)
			}
		}
	}
}

// The behaviour #5404 reports: a multi-day upstream deadline made the credential
// unselectable for days, so a quota reset that happened earlier was never observed.
// The recorded deadline must now be bounded, which lets an ordinary request retry
// the credential and discover the recovery.
func TestMarkResultBoundsAMultiDayQuotaHint(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:         uuid.NewString() + "-bounded-quota",
		Provider:   "claude",
		Attributes: map[string]string{"api_key": "test-key"},
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "claude", []*registry.ModelInfo{{ID: "claude-3-5-sonnet-20241022"}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	before := time.Now()
	week := 168 * time.Hour
	manager.MarkResult(context.Background(), Result{
		AuthID:          auth.ID,
		Provider:        "claude",
		Model:           "claude-3-5-sonnet-20241022",
		Success:         false,
		RetryAfter:      &week,
		CredentialScope: true,
		Error:           &Error{HTTPStatus: http.StatusTooManyRequests, Message: "usage limit reached"},
	})

	updated, ok := manager.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatal("auth not found")
	}
	if !updated.Quota.Exceeded {
		t.Fatal("quota failure did not mark the credential as exceeded")
	}
	deadline := updated.Quota.NextRecoverAt
	if !deadline.After(before) {
		t.Fatalf("no live cooldown recorded: %s", deadline)
	}
	// Bounded, not the advertised week.
	if limit := before.Add(defaultMaxTrustedQuotaCooldown + time.Minute); deadline.After(limit) {
		t.Fatalf("recorded deadline %s exceeds the bound %s; the advertised week was trusted verbatim", deadline, limit)
	}
	// Still blocked right now: bounding must not make the credential selectable
	// while the window is open.
	blocked, reason, _ := isAuthBlockedForModel(updated, "claude-3-5-sonnet-20241022", time.Now())
	if !blocked || reason != blockReasonCooldown {
		t.Fatalf("credential is selectable during its bounded cooldown: blocked=%v reason=%v", blocked, reason)
	}
	// And selectable once the bounded window has passed, which is the recovery
	// path the issue asks for.
	blocked, _, _ = isAuthBlockedForModel(updated, "claude-3-5-sonnet-20241022", deadline.Add(time.Second))
	if blocked {
		t.Fatal("credential is still blocked after its bounded cooldown expired")
	}
}
