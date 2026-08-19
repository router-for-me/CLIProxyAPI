package auth

import (
	"testing"
	"time"
)

// Registry reconciliation runs on every token refresh. These tests pin the
// boundary between "clear transient errors" and "clear quota accounting",
// because conflating the two returned exhausted credentials to rotation and
// reset the backoff exponent so it never escalated past a few seconds.

func TestResetModelStateKeepingQuotaPreservesOpenCooldown(t *testing.T) {
	now := time.Now()
	recover := now.Add(45 * time.Minute)
	state := &ModelState{
		Status:         StatusError,
		Unavailable:    true,
		NextRetryAfter: recover,
		LastError:      &Error{Code: "rate_limit", Message: "quota"},
		Quota: QuotaState{
			Exceeded:      true,
			Reason:        "quota",
			NextRecoverAt: recover,
			BackoffLevel:  7,
		},
	}

	resetModelStateKeepingQuota(state, now)

	if !state.Quota.Exceeded {
		t.Fatal("open cooldown was cleared; credential would rejoin rotation while still exhausted")
	}
	if !state.Quota.NextRecoverAt.Equal(recover) {
		t.Fatalf("NextRecoverAt = %v, want %v", state.Quota.NextRecoverAt, recover)
	}
	if state.Quota.BackoffLevel != 7 {
		t.Fatalf("BackoffLevel = %d, want 7", state.Quota.BackoffLevel)
	}
	if !state.Unavailable {
		t.Fatal("state should stay unavailable until the cooldown expires")
	}
	if state.LastError != nil {
		t.Fatal("transient error should still be cleared")
	}
}

func TestResetModelStateKeepingQuotaCarriesLadderPastExpiry(t *testing.T) {
	now := time.Now()
	state := &ModelState{
		Status:      StatusError,
		Unavailable: true,
		LastError:   &Error{Code: "rate_limit", Message: "quota"},
		Quota: QuotaState{
			Exceeded:      true,
			Reason:        "quota",
			NextRecoverAt: now.Add(-time.Minute), // window already elapsed
			BackoffLevel:  6,
		},
	}

	resetModelStateKeepingQuota(state, now)

	if state.Quota.Exceeded {
		t.Fatal("expired window should leave the credential retryable")
	}
	if state.Unavailable || state.Status != StatusActive {
		t.Fatalf("state should be active again, got status %v unavailable %v", state.Status, state.Unavailable)
	}
	// The ladder must survive, otherwise the next failure restarts at one second.
	if state.Quota.BackoffLevel != 6 {
		t.Fatalf("BackoffLevel = %d, want 6 carried forward", state.Quota.BackoffLevel)
	}
}

func TestResetModelStateStillClearsQuotaOnSuccess(t *testing.T) {
	now := time.Now()
	state := &ModelState{
		Status:      StatusError,
		Unavailable: true,
		Quota: QuotaState{
			Exceeded:      true,
			Reason:        "quota",
			NextRecoverAt: now.Add(time.Hour),
			BackoffLevel:  9,
		},
	}

	resetModelState(state, now)

	if state.Quota != (QuotaState{}) {
		t.Fatalf("success path must fully clear quota, got %+v", state.Quota)
	}
	if state.Unavailable || state.Status != StatusActive {
		t.Fatalf("state should be fully reset, got status %v unavailable %v", state.Status, state.Unavailable)
	}
}

func TestApplyProviderQuotaFloorRaisesClaudeOnly(t *testing.T) {
	now := time.Now()
	short := now.Add(30 * time.Second)

	// Anthropic sends no reset hint on 429, so a 30s guess must be raised.
	got := applyProviderQuotaFloor("claude", short, now)
	minFloor := now.Add(anthropicQuotaFloor - anthropicQuotaFloor/5)
	maxFloor := now.Add(anthropicQuotaFloor + anthropicQuotaFloor/5)
	if got.Before(minFloor) || got.After(maxFloor) {
		t.Fatalf("claude floor = %v, want within jitter band [%v, %v]", got, minFloor, maxFloor)
	}

	// Providers that report a real reset must not be touched.
	if other := applyProviderQuotaFloor("codex", short, now); !other.Equal(short) {
		t.Fatalf("codex cooldown = %v, want untouched %v", other, short)
	}
}

func TestApplyProviderQuotaFloorNeverShortensLongerCooldown(t *testing.T) {
	now := time.Now()
	long := now.Add(3 * time.Hour)
	if got := applyProviderQuotaFloor("anthropic", long, now); !got.Equal(long) {
		t.Fatalf("floor shortened an escalated cooldown: got %v, want %v", got, long)
	}
	if got := applyProviderQuotaFloor("claude", time.Time{}, now); !got.IsZero() {
		t.Fatal("zero cooldown (cooling disabled) must stay zero")
	}
}
