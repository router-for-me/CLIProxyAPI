package auth

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestTemporaryUnavailabilityPreservesRecoveryDeadline(t *testing.T) {
	now := time.Now()
	temporary := &Auth{ID: "temporary", Provider: "codex", Unavailable: true, NextRetryAfter: now.Add(time.Minute)}
	disabled := &Auth{ID: "disabled", Provider: "codex", Disabled: true, NextRetryAfter: now.Add(time.Second)}
	for _, auths := range [][]*Auth{{temporary}, {temporary, disabled}} {
		for _, selectAuths := range []func() ([]*Auth, error){
			func() ([]*Auth, error) { return getAvailableAuths(auths, "codex", "gpt-5.6-luna", now) },
			func() ([]*Auth, error) {
				return (&Manager{}).availableAuthsForRouteModelWithPriorityMode(auths, "codex", "gpt-5.6-luna", now, false)
			},
		} {
			_, err := selectAuths()
			var authErr *Error
			if !errors.As(err, &authErr) || authErr.Code != "auth_unavailable" || !authErr.Retryable {
				t.Fatalf("expected recoverable auth_unavailable, got %T %v", err, err)
			}
			if got := SafeResponseHeaders(fmt.Errorf("outer: %w", err)).Get("Retry-After"); got != "60" {
				t.Fatalf("Retry-After = %q, want 60", got)
			}
		}
	}
	_, err := getAvailableAuths([]*Auth{disabled}, "codex", "gpt-5.6-luna", now)
	if got := SafeResponseHeaders(err).Get("Retry-After"); got != "" {
		t.Fatalf("disabled credential must not advertise recovery, got %q", got)
	}
}

func TestSchedulerPreservesTemporaryDeadlineWithoutChangingQuotaClassification(t *testing.T) {
	now := time.Now()
	shard := &modelScheduler{entries: map[string]*scheduledAuth{
		"temporary": {auth: &Auth{ID: "temporary"}, state: scheduledStateBlocked, nextRetryAt: now.Add(time.Minute)},
		"disabled":  {auth: &Auth{ID: "disabled"}, state: scheduledStateDisabled, nextRetryAt: now.Add(time.Second)},
	}}
	total, quota, _, earliest := shard.availabilitySummaryLocked(nil)
	if total != 2 || quota != 0 || !earliest.Equal(now.Add(time.Minute)) {
		t.Fatalf("unexpected summary: total=%d quota=%d earliest=%v", total, quota, earliest)
	}
	err := shard.unavailableErrorLocked("codex", "gpt-5.6-luna", nil)
	if got := SafeResponseHeaders(err).Get("Retry-After"); got != "60" {
		t.Fatalf("scheduler Retry-After = %q, want 60", got)
	}
	delete(shard.entries, "disabled")
	shard.entries["temporary"].state = scheduledStateCooldown
	err = shard.unavailableErrorLocked("codex", "gpt-5.6-luna", nil)
	var cooldown *modelCooldownError
	if !errors.As(err, &cooldown) || cooldown.StatusCode() != 429 {
		t.Fatalf("quota cooldown contract changed: %T %v", err, err)
	}
}
