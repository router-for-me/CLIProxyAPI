package auth

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// A provider-reported reset deadline is a prediction made at the moment of failure, so
// it must never park a credential indefinitely: quota can be restored ahead of it by a
// window rollover or an operator-initiated reset. These tests pin the ceiling behaviour
// and, just as importantly, the cases where the clamp must NOT interfere.

func TestClampQuotaCooldown(t *testing.T) {
	now := time.Date(2026, 9, 8, 3, 0, 0, 0, time.UTC)
	ceiling := now.Add(maxQuotaCooldownCeiling)

	tests := []struct {
		name string
		next time.Time
		want time.Time
	}{
		{name: "zero stays zero", next: time.Time{}, want: time.Time{}},
		{name: "exactly at ceiling is untouched", next: ceiling, want: ceiling},
		{name: "one nanosecond under ceiling is untouched", next: ceiling.Add(-time.Nanosecond), want: ceiling.Add(-time.Nanosecond)},
		{name: "one nanosecond over ceiling is clamped", next: ceiling.Add(time.Nanosecond), want: ceiling},
		{name: "seven day reset is clamped", next: now.Add(7 * 24 * time.Hour), want: ceiling},
		{name: "absurd far future does not overflow", next: time.Date(9999, 12, 31, 23, 59, 59, 0, time.UTC), want: ceiling},
		// A deadline already in the past is expired. Clamping must never push it
		// forward, which would resurrect a cooldown that had already elapsed.
		{name: "past deadline is not extended", next: now.Add(-time.Hour), want: now.Add(-time.Hour)},
		{name: "far past deadline is not extended", next: now.Add(-30 * 24 * time.Hour), want: now.Add(-30 * 24 * time.Hour)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := clampQuotaCooldown(tc.next, now); !got.Equal(tc.want) {
				t.Fatalf("clampQuotaCooldown(%v) = %v, want %v", tc.next, got, tc.want)
			}
		})
	}
}

// The floor and the ceiling must not invert, otherwise a cooldown could be clamped
// below the minimum it was just raised to.
func TestQuotaCooldownBoundsAreOrdered(t *testing.T) {
	if minQuotaCooldownFloor >= maxQuotaCooldownCeiling {
		t.Fatalf("minQuotaCooldownFloor (%v) must be below maxQuotaCooldownCeiling (%v)", minQuotaCooldownFloor, maxQuotaCooldownCeiling)
	}
	if quotaBackoffMax > maxQuotaCooldownCeiling {
		t.Fatalf("quotaBackoffMax (%v) exceeds maxQuotaCooldownCeiling (%v), so backoff would always clamp", quotaBackoffMax, maxQuotaCooldownCeiling)
	}
}

func TestApplyAuthFailureState_ClampsLongProviderReset(t *testing.T) {
	now := time.Date(2026, 9, 8, 3, 0, 0, 0, time.UTC)
	sevenDays := 7 * 24 * time.Hour
	auth := &Auth{ID: "codex-ceiling", Provider: "codex"}

	applyAuthFailureState(auth, &Error{HTTPStatus: http.StatusTooManyRequests, Message: "usage_limit_reached"}, &sevenDays, now, false)

	want := now.Add(maxQuotaCooldownCeiling)
	if !auth.Quota.NextRecoverAt.Equal(want) {
		t.Fatalf("Quota.NextRecoverAt = %v, want clamped %v", auth.Quota.NextRecoverAt, want)
	}
	if !auth.NextRetryAfter.Equal(want) {
		t.Fatalf("NextRetryAfter = %v, want clamped %v", auth.NextRetryAfter, want)
	}
	if !auth.Quota.Exceeded {
		t.Fatal("Quota.Exceeded = false, want the credential to remain in cooldown")
	}
}

// Regression guard for the reason the clamp is applied to the final deadline rather
// than to the incoming retry-after: both quota paths raise the new deadline to the
// stored NextRecoverAt to keep a cooldown monotonic. If only the incoming value were
// clamped, a previously stored multi-day deadline would win and re-extend forever.
func TestApplyAuthFailureState_ClampsPreservedLongDeadlineOnReArm(t *testing.T) {
	now := time.Date(2026, 9, 8, 3, 0, 0, 0, time.UTC)
	short := 5 * time.Minute
	auth := &Auth{ID: "codex-rearm", Provider: "codex"}
	auth.Quota.Exceeded = true
	auth.Quota.Reason = "quota"
	auth.Quota.NextRecoverAt = now.Add(7 * 24 * time.Hour)

	applyAuthFailureState(auth, &Error{HTTPStatus: http.StatusTooManyRequests, Message: "usage_limit_reached"}, &short, now, false)

	want := now.Add(maxQuotaCooldownCeiling)
	if !auth.Quota.NextRecoverAt.Equal(want) {
		t.Fatalf("re-armed Quota.NextRecoverAt = %v, want clamped %v", auth.Quota.NextRecoverAt, want)
	}
}

// A deadline already inside the ceiling must be honoured exactly, so the clamp cannot
// silently lengthen short provider cooldowns.
func TestApplyAuthFailureState_ShortResetIsUnchanged(t *testing.T) {
	now := time.Date(2026, 9, 8, 3, 0, 0, 0, time.UTC)
	short := 5 * time.Minute
	auth := &Auth{ID: "codex-short", Provider: "codex"}

	applyAuthFailureState(auth, &Error{HTTPStatus: http.StatusTooManyRequests, Message: "usage_limit_reached"}, &short, now, false)

	if want := now.Add(short); !auth.Quota.NextRecoverAt.Equal(want) {
		t.Fatalf("Quota.NextRecoverAt = %v, want untouched %v", auth.Quota.NextRecoverAt, want)
	}
}

// The existing floor must keep applying underneath the ceiling.
func TestApplyAuthFailureState_SubSecondResetStillRaisedToFloor(t *testing.T) {
	now := time.Date(2026, 9, 8, 3, 0, 0, 0, time.UTC)
	tiny := time.Nanosecond
	auth := &Auth{ID: "codex-floor", Provider: "codex"}

	applyAuthFailureState(auth, &Error{HTTPStatus: http.StatusTooManyRequests, Message: "usage_limit_reached"}, &tiny, now, false)

	if want := now.Add(minQuotaCooldownFloor); !auth.Quota.NextRecoverAt.Equal(want) {
		t.Fatalf("Quota.NextRecoverAt = %v, want floor %v", auth.Quota.NextRecoverAt, want)
	}
}

// With cooling disabled the deadlines stay zero; the clamp must not manufacture a
// cooldown out of "no cooldown".
func TestApplyAuthFailureState_DisableCoolingKeepsZeroDeadline(t *testing.T) {
	now := time.Date(2026, 9, 8, 3, 0, 0, 0, time.UTC)
	sevenDays := 7 * 24 * time.Hour
	auth := &Auth{ID: "codex-nocool", Provider: "codex"}

	applyAuthFailureState(auth, &Error{HTTPStatus: http.StatusTooManyRequests, Message: "usage_limit_reached"}, &sevenDays, now, true)

	if !auth.Quota.NextRecoverAt.IsZero() {
		t.Fatalf("Quota.NextRecoverAt = %v, want zero when cooling is disabled", auth.Quota.NextRecoverAt)
	}
	if !auth.NextRetryAfter.IsZero() {
		t.Fatalf("NextRetryAfter = %v, want zero when cooling is disabled", auth.NextRetryAfter)
	}
}

// Non-quota failures are deliberately outside the ceiling: a 404 keeps its 12h
// deadline, so the clamp must not leak into other status codes.
func TestApplyAuthFailureState_NonQuotaDeadlineIsNotClamped(t *testing.T) {
	now := time.Date(2026, 9, 8, 3, 0, 0, 0, time.UTC)
	auth := &Auth{ID: "codex-404", Provider: "codex"}

	applyAuthFailureState(auth, &Error{HTTPStatus: http.StatusNotFound, Message: "not found"}, nil, now, false)

	if !auth.NextRetryAfter.After(now.Add(maxQuotaCooldownCeiling)) {
		t.Fatalf("NextRetryAfter = %v, want a 404 deadline well beyond the quota ceiling", auth.NextRetryAfter)
	}
}

// A cooldown store written before the ceiling existed can hold a multi-day quota
// deadline. The selector blocks such a credential, so no 429 path can run to re-clamp
// it; unless the deadline is migrated on restore the credential stays latched for its
// original duration even after this fix ships.
func TestRestoreCooldownStates_ClampsPreFixMultiDayQuotaRecord(t *testing.T) {
	now := time.Now()
	sevenDaysOut := now.Add(7 * 24 * time.Hour)

	manager := NewManager(nil, nil, nil)
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), &Auth{ID: "auth-prefix-record", Provider: "codex"}); errRegister != nil {
		t.Fatalf("Register() returned error: %v", errRegister)
	}
	manager.SetCooldownStateStore(&mockCooldownStateStore{records: []CooldownStateRecord{
		{
			Provider:       "codex",
			AuthID:         "auth-prefix-record",
			NextRetryAfter: sevenDaysOut,
			Reason:         "quota exhausted",
			Quota: QuotaState{
				Exceeded:      true,
				Reason:        "credential_quota",
				NextRecoverAt: sevenDaysOut,
			},
			UpdatedAt: now,
		},
	}})

	if errRestore := manager.RestoreCooldownStates(context.Background()); errRestore != nil {
		t.Fatalf("RestoreCooldownStates() returned error: %v", errRestore)
	}

	restored, ok := manager.GetByID("auth-prefix-record")
	if !ok || restored == nil {
		t.Fatal("restored auth not found")
	}
	ceiling := now.Add(maxQuotaCooldownCeiling)
	if restored.Quota.NextRecoverAt.After(ceiling.Add(time.Minute)) {
		t.Fatalf("restored Quota.NextRecoverAt = %v, want clamped to ~%v", restored.Quota.NextRecoverAt, ceiling)
	}
	if restored.NextRetryAfter.After(ceiling.Add(time.Minute)) {
		t.Fatalf("restored NextRetryAfter = %v, want clamped to ~%v", restored.NextRetryAfter, ceiling)
	}
	// The credential must still be cooling; migrating the deadline must not clear it.
	if !restored.Quota.Exceeded {
		t.Fatal("restored Quota.Exceeded = false, want the cooldown preserved")
	}
	if blocked, _, _ := isAuthBlockedForModel(restored, "", now); !blocked {
		t.Fatal("restored credential is not blocked, want it still cooling until the ceiling")
	}
}

// A restored record whose retry deadline is longer than its quota deadline carries a
// non-quota cooldown (e.g. a 12h 404). The ceiling must not shorten that.
func TestRestoreCooldownStates_LeavesLongerNonQuotaDeadlineIntact(t *testing.T) {
	now := time.Now()
	quotaNext := now.Add(20 * time.Minute)
	notFoundNext := now.Add(12 * time.Hour)

	manager := NewManager(nil, nil, nil)
	if _, errRegister := manager.Register(WithSkipPersist(context.Background()), &Auth{ID: "auth-mixed-record", Provider: "codex"}); errRegister != nil {
		t.Fatalf("Register() returned error: %v", errRegister)
	}
	manager.SetCooldownStateStore(&mockCooldownStateStore{records: []CooldownStateRecord{
		{
			Provider:       "codex",
			AuthID:         "auth-mixed-record",
			NextRetryAfter: notFoundNext,
			Quota:          QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: quotaNext},
			UpdatedAt:      now,
		},
	}})

	if errRestore := manager.RestoreCooldownStates(context.Background()); errRestore != nil {
		t.Fatalf("RestoreCooldownStates() returned error: %v", errRestore)
	}

	restored, ok := manager.GetByID("auth-mixed-record")
	if !ok || restored == nil {
		t.Fatal("restored auth not found")
	}
	if !restored.NextRetryAfter.Equal(notFoundNext) {
		t.Fatalf("restored NextRetryAfter = %v, want the non-quota deadline %v untouched", restored.NextRetryAfter, notFoundNext)
	}
	if !restored.Quota.NextRecoverAt.Equal(quotaNext) {
		t.Fatalf("restored Quota.NextRecoverAt = %v, want %v untouched (already inside the ceiling)", restored.Quota.NextRecoverAt, quotaNext)
	}
}

// A credential-scoped 429 promotes a deadline to every model on the credential. If the
// stored aggregate deadline were carried through the maximum unclamped, it would become
// credential_quota and park the whole credential for the original multi-day duration.
func TestMarkResult_CredentialScopePropagationIsClamped(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	m, auth := newCooldownMonotonicManager(t, "model-a", "model-b")

	// Simulate a credential that already carries a pre-fix multi-day quota deadline.
	now := time.Now()
	stale := auth.Clone()
	stale.Quota.Exceeded = true
	stale.Quota.Reason = "quota"
	stale.Quota.NextRecoverAt = now.Add(7 * 24 * time.Hour)
	if _, errUpdate := m.Update(WithSkipPersist(context.Background()), stale); errUpdate != nil {
		t.Fatalf("Update() returned error: %v", errUpdate)
	}

	short := 5 * time.Minute
	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: auth.Provider, Model: "model-a",
		Success: false, RetryAfter: &short, CredentialScope: true,
		Error: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "credential 429"},
	})

	updated, ok := m.GetByID(auth.ID)
	if !ok || updated == nil {
		t.Fatal("auth not found")
	}
	ceiling := now.Add(maxQuotaCooldownCeiling).Add(time.Minute)
	if updated.Quota.NextRecoverAt.After(ceiling) {
		t.Fatalf("credential-wide Quota.NextRecoverAt = %v, want clamped to ~%v", updated.Quota.NextRecoverAt, ceiling)
	}
	for _, model := range []string{"model-a", "model-b"} {
		state := existingModelState(updated, canonicalModelKey(model))
		if state == nil {
			continue
		}
		if state.Quota.NextRecoverAt.After(ceiling) {
			t.Fatalf("model %q Quota.NextRecoverAt = %v, want clamped to ~%v", model, state.Quota.NextRecoverAt, ceiling)
		}
	}
}
