package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

// Tests in this file cover the refresh lifecycle half of issue #6690:
// refreshed Devin quota observations must reach the stored auth, and a
// refresh that confirms restored daily+weekly quota must release the
// credential-wide cooldown early.

func devinRefreshAuthFixture(t *testing.T, id string) (*Manager, *Auth, string, string) {
	t.Helper()
	m := NewManager(nil, nil, nil)
	model := "claude-opus-5-5"
	sibling := "claude-haiku-5-5"
	auth := &Auth{ID: id, Provider: "devin", Status: StatusActive}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, "devin", []*registry.ModelInfo{{ID: model}, {ID: sibling}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID) })
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("register: %v", err)
	}
	return m, auth, model, sibling
}

func devinExhaustedAuth(t *testing.T, m *Manager, id, model, sibling string, recoverAt time.Time) *Auth {
	t.Helper()
	stored, ok := m.GetByID(id)
	if !ok || stored == nil {
		t.Fatal("auth missing")
	}
	stored.Unavailable = true
	stored.Status = StatusError
	stored.NextRetryAfter = recoverAt
	stored.Quota = QuotaState{
		Exceeded: true, Reason: "credential_quota", NextRecoverAt: recoverAt,
		ObservedAt: time.Now().Add(-2 * time.Hour),
		Signals: map[string]string{
			"daily_quota_remaining_percent":  "0%",
			"weekly_quota_remaining_percent": "0%",
		},
	}
	stored.ModelStates = map[string]*ModelState{
		model: {
			Status: StatusError, Unavailable: true, NextRetryAfter: recoverAt,
			Quota: QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: recoverAt},
		},
		sibling: {
			Status: StatusError, Unavailable: true, NextRetryAfter: recoverAt,
			Quota: QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: recoverAt},
		},
	}
	if _, err := m.Update(context.Background(), stored); err != nil {
		t.Fatalf("seed exhausted auth: %v", err)
	}
	base, _ := m.GetByID(id)
	return base
}

func devinRefreshedObservation(base *Auth, dailyPercent, weeklyPercent string, dailyReset, weeklyReset time.Time) *Auth {
	updated := base.Clone()
	updated.Quota.Signals = map[string]string{
		"daily_quota_remaining_percent":  dailyPercent,
		"weekly_quota_remaining_percent": weeklyPercent,
	}
	if !dailyReset.IsZero() {
		updated.Quota.Signals["daily_quota_reset_at"] = dailyReset.Format(time.RFC3339)
	}
	if !weeklyReset.IsZero() {
		updated.Quota.Signals["weekly_quota_reset_at"] = weeklyReset.Format(time.RFC3339)
	}
	updated.Quota.ObservedAt = time.Now()
	updated.LastRefreshedAt = time.Now()
	return updated
}

func TestDevinRefresh_ObservationSignalsReachStoredAuth(t *testing.T) {
	withQuotaCooldownEnabled(t)
	m, auth, _, _ := devinRefreshAuthFixture(t, "devin-refresh-signals-6690")
	base, _ := m.GetByID(auth.ID)

	weeklyReset := time.Now().Add(72 * time.Hour).UTC().Truncate(time.Second)
	updated := devinRefreshedObservation(base, "0%", "0%", time.Time{}, weeklyReset)
	if _, err := m.UpdateRefreshedAuth(context.Background(), base, updated); err != nil {
		t.Fatalf("UpdateRefreshedAuth: %v", err)
	}

	stored, _ := m.GetByID(auth.ID)
	if stored == nil {
		t.Fatal("stored auth missing")
	}
	if got := stored.Quota.Signals["weekly_quota_reset_at"]; got != weeklyReset.Format(time.RFC3339) {
		t.Fatalf("weekly_quota_reset_at = %q, want %q (refreshed observation must survive the refresh merge)", got, weeklyReset.Format(time.RFC3339))
	}
	if got := stored.Quota.Signals["weekly_quota_remaining_percent"]; got != "0%" {
		t.Fatalf("weekly_quota_remaining_percent = %q, want 0%%", got)
	}
}

func TestDevinQuotaExhaustionSchedulesRevalidation(t *testing.T) {
	m, auth, model, sibling := devinRefreshAuthFixture(t, "devin-refresh-revalidate-6690")
	recoverAt := time.Now().Add(72 * time.Hour)
	base := devinExhaustedAuth(t, m, auth.ID, model, sibling, recoverAt)
	// Devin session tokens are permanent (no expiry, nil refresh lead), so a
	// plain token-refresh decision must not depend on those paths.
	// Fresh last refresh: not yet due, but kept in the schedule.
	base.LastRefreshedAt = time.Now()
	if m.shouldRefresh(base, time.Now()) {
		t.Fatal("revalidation must respect its interval right after a refresh")
	}
	next, ok := nextRefreshCheckAt(time.Now(), base, 5*time.Second)
	if !ok {
		t.Fatal("exhausted Devin credential must remain in the auto-refresh schedule")
	}
	if !next.After(time.Now()) || next.After(time.Now().Add(2*time.Hour)) {
		t.Fatalf("revalidation check = %v, want within a bounded near-term horizon", next)
	}
	// Stale last refresh: due now.
	base.LastRefreshedAt = time.Now().Add(-2 * time.Hour)
	if !m.shouldRefresh(base, time.Now()) {
		t.Fatal("exhausted Devin credential past its revalidation interval must refresh")
	}

	// A healthy Devin credential must not start polling: revalidation is
	// scoped to the active credential-quota state only.
	healthy := &Auth{ID: "devin-healthy", Provider: "devin", Status: StatusActive, LastRefreshedAt: time.Now()}
	if m.shouldRefresh(healthy, time.Now()) {
		t.Fatal("healthy Devin credential must not be refreshed for quota revalidation")
	}
}

func TestDevinRefresh_RestoredQuotaReleasesCredentialCooldown(t *testing.T) {
	withQuotaCooldownEnabled(t)
	m, auth, model, sibling := devinRefreshAuthFixture(t, "devin-refresh-restored-6690")
	base := devinExhaustedAuth(t, m, auth.ID, model, sibling, time.Now().Add(72*time.Hour))

	updated := devinRefreshedObservation(base, "80%", "55%", time.Time{}, time.Time{})
	if _, err := m.UpdateRefreshedAuth(context.Background(), base, updated); err != nil {
		t.Fatalf("UpdateRefreshedAuth: %v", err)
	}

	stored, _ := m.GetByID(auth.ID)
	if stored == nil {
		t.Fatal("stored auth missing")
	}
	if stored.Quota.Exceeded || stored.Quota.Reason == "credential_quota" {
		t.Fatalf("credential quota must be released after restored refresh, got %+v", stored.Quota)
	}
	if stored.Unavailable || !stored.NextRetryAfter.IsZero() {
		t.Fatalf("auth must be available after restored refresh, got unavailable=%v nextRetry=%v", stored.Unavailable, stored.NextRetryAfter)
	}
	for _, mID := range []string{model, sibling} {
		state := stored.ModelStates[mID]
		if state == nil || state.Unavailable || !state.NextRetryAfter.IsZero() || state.Quota.Exceeded {
			t.Fatalf("model %s must be released after restored refresh, got %+v", mID, state)
		}
		if blocked, _, _ := isAuthBlockedForModel(stored, mID, time.Now()); blocked {
			t.Fatalf("model %s still blocked for selection after restored refresh", mID)
		}
	}
}

func TestDevinRefresh_StillExhaustedPreservesCooldown(t *testing.T) {
	withQuotaCooldownEnabled(t)
	m, auth, model, sibling := devinRefreshAuthFixture(t, "devin-refresh-exhausted-6690")
	recoverAt := time.Now().Add(72 * time.Hour)
	base := devinExhaustedAuth(t, m, auth.ID, model, sibling, recoverAt)

	updated := devinRefreshedObservation(base, "0%", "0%", time.Time{}, recoverAt)
	if _, err := m.UpdateRefreshedAuth(context.Background(), base, updated); err != nil {
		t.Fatalf("UpdateRefreshedAuth: %v", err)
	}

	stored, _ := m.GetByID(auth.ID)
	if stored == nil || !stored.Quota.Exceeded || stored.Quota.Reason != "credential_quota" {
		t.Fatalf("credential quota must be preserved while still exhausted, got %+v", stored)
	}
	if !stored.NextRetryAfter.After(time.Now()) {
		t.Fatalf("cooldown deadline must remain in the future, got %v", stored.NextRetryAfter)
	}
	if got := stored.Quota.Signals["weekly_quota_reset_at"]; got == "" {
		t.Fatal("refreshed weekly reset signal must survive even while cooldown is preserved")
	}
}

func TestDevinRefresh_StaleObservationCannotReleaseCooldown(t *testing.T) {
	withQuotaCooldownEnabled(t)
	m, auth, model, sibling := devinRefreshAuthFixture(t, "devin-refresh-stale-6690")
	base := devinExhaustedAuth(t, m, auth.ID, model, sibling, time.Now().Add(72*time.Hour))

	stale := devinRefreshedObservation(base, "90%", "90%", time.Time{}, time.Time{})
	// Older than the stored snapshot: must be rejected as a release signal.
	storedSnap, _ := m.GetByID(auth.ID)
	stale.Quota.ObservedAt = storedSnap.Quota.ObservedAt.Add(-time.Hour)
	if _, err := m.UpdateRefreshedAuth(context.Background(), base, stale); err != nil {
		t.Fatalf("UpdateRefreshedAuth: %v", err)
	}

	stored, _ := m.GetByID(auth.ID)
	if stored == nil || !stored.Quota.Exceeded || stored.Quota.Reason != "credential_quota" {
		t.Fatalf("stale restored observation must not release the cooldown, got %+v", stored)
	}
	if got := stored.Quota.Signals["weekly_quota_remaining_percent"]; got != "0%" {
		t.Fatalf("stale observation must not overwrite stored signals, weekly percent = %q", got)
	}
	if blocked, _, _ := isAuthBlockedForModel(stored, model, time.Now()); !blocked {
		t.Fatal("model must remain blocked under a stale refresh")
	}
}

func TestDevinMarkResult_EnrollsExhaustedCredentialForRevalidation(t *testing.T) {
	withQuotaCooldownEnabled(t)
	m, auth, model, _ := devinRefreshAuthFixture(t, "devin-enroll-6690")

	loop := newAuthAutoRefreshLoop(m, 5*time.Second, 1)
	m.refreshLoop = loop

	// Healthy permanent-token credentials start unscheduled.
	loop.applyDirty(time.Now())
	loop.mu.Lock()
	_, scheduled := loop.index[auth.ID]
	loop.mu.Unlock()
	if scheduled {
		t.Fatal("healthy Devin credential must start unscheduled")
	}

	retryAfter := 72 * time.Hour
	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: "devin", Model: model, Success: false,
		Error:           &Error{Code: "rate_limit", Message: "weekly quota", HTTPStatus: http.StatusTooManyRequests},
		RetryAfter:      &retryAfter,
		CredentialScope: true,
	})

	loop.applyDirty(time.Now())
	loop.mu.Lock()
	_, scheduled = loop.index[auth.ID]
	loop.mu.Unlock()
	if !scheduled {
		t.Fatal("newly exhausted Devin credential must be enrolled for quota revalidation after MarkResult")
	}
}

func TestDevinRefresh_PropagatedSiblingKeepsPreExistingBlock(t *testing.T) {
	withQuotaCooldownEnabled(t)
	m, auth, model, sibling := devinRefreshAuthFixture(t, "devin-propagated-sibling-6690")

	// Sibling first earns an unrelated long block (404 -> 12h).
	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: "devin", Model: sibling, Success: false,
		Error: &Error{Code: "not_found", Message: "model missing", HTTPStatus: http.StatusNotFound},
	})
	seeded, _ := m.GetByID(auth.ID)
	sibState := seeded.ModelStates[sibling]
	if sibState == nil || !sibState.Unavailable || !sibState.NextRetryAfter.After(time.Now().Add(time.Hour)) {
		t.Fatalf("sibling 404 block not established: %+v", sibState)
	}
	unrelatedDeadline := sibState.NextRetryAfter

	// The reporting model then exhausts the weekly quota (short 2h reset).
	retryAfter := 2 * time.Hour
	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: "devin", Model: model, Success: false,
		Error:           &Error{Code: "rate_limit", Message: "weekly quota", HTTPStatus: http.StatusTooManyRequests},
		RetryAfter:      &retryAfter,
		CredentialScope: true,
	})
	base, _ := m.GetByID(auth.ID)
	if base.Quota.Reason != "credential_quota" {
		t.Fatalf("expected credential_quota after propagation, got %+v", base.Quota)
	}

	// Restored quota must release only the quota component.
	updated := devinRefreshedObservation(base, "80%", "55%", time.Time{}, time.Time{})
	if _, err := m.UpdateRefreshedAuth(context.Background(), base, updated); err != nil {
		t.Fatalf("UpdateRefreshedAuth: %v", err)
	}

	stored, _ := m.GetByID(auth.ID)
	if stored.Quota.Reason == "credential_quota" {
		t.Fatalf("credential quota must be released, got %+v", stored.Quota)
	}
	if blocked, _, _ := isAuthBlockedForModel(stored, model, time.Now()); blocked {
		t.Fatal("reporting model must be released after restoration")
	}
	if blocked, _, _ := isAuthBlockedForModel(stored, sibling, time.Now()); !blocked {
		t.Fatal("pre-existing unrelated sibling block must survive quota restoration")
	}
	if got := stored.ModelStates[sibling].NextRetryAfter; got.Before(unrelatedDeadline.Add(-time.Minute)) {
		t.Fatalf("sibling deadline shortened by restoration: got %v, want ~%v", got, unrelatedDeadline)
	}
}

func TestDevinRefresh_PreservesSiblingModelScoped429Block(t *testing.T) {
	withQuotaCooldownEnabled(t)
	m, auth, model, sibling := devinRefreshAuthFixture(t, "devin-sibling-429-6690")

	// Sibling earns an independent model-scoped 429 block (12h).
	longRetry := 12 * time.Hour
	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: "devin", Model: sibling, Success: false,
		Error:      &Error{Code: "rate_limit", Message: "model rate limit", HTTPStatus: http.StatusTooManyRequests},
		RetryAfter: &longRetry,
	})
	// Reporting model exhausts the weekly quota (2h reset).
	quotaRetry := 2 * time.Hour
	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: "devin", Model: model, Success: false,
		Error:           &Error{Code: "rate_limit", Message: "weekly quota", HTTPStatus: http.StatusTooManyRequests},
		RetryAfter:      &quotaRetry,
		CredentialScope: true,
	})
	base, _ := m.GetByID(auth.ID)

	updated := devinRefreshedObservation(base, "80%", "55%", time.Time{}, time.Time{})
	if _, err := m.UpdateRefreshedAuth(context.Background(), base, updated); err != nil {
		t.Fatalf("UpdateRefreshedAuth: %v", err)
	}
	stored, _ := m.GetByID(auth.ID)
	if blocked, _, _ := isAuthBlockedForModel(stored, model, time.Now()); blocked {
		t.Fatal("reporting model must be released after restoration")
	}
	if blocked, _, _ := isAuthBlockedForModel(stored, sibling, time.Now()); !blocked {
		t.Fatal("independent model-scoped 429 block must survive quota restoration")
	}
}

func TestDevinRefresh_PreservesSibling404UnderLongerQuota(t *testing.T) {
	withQuotaCooldownEnabled(t)
	m, auth, model, sibling := devinRefreshAuthFixture(t, "devin-sibling-404-long-6690")

	// Sibling earns a 404 block (12h); the credential quota runs longer (72h),
	// so propagation extends the sibling deadline. Restoration must restore
	// the sibling to its original 404 block, not release it.
	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: "devin", Model: sibling, Success: false,
		Error: &Error{Code: "not_found", Message: "model missing", HTTPStatus: http.StatusNotFound},
	})
	quotaRetry := 72 * time.Hour
	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: "devin", Model: model, Success: false,
		Error:           &Error{Code: "rate_limit", Message: "weekly quota", HTTPStatus: http.StatusTooManyRequests},
		RetryAfter:      &quotaRetry,
		CredentialScope: true,
	})
	base, _ := m.GetByID(auth.ID)

	updated := devinRefreshedObservation(base, "80%", "55%", time.Time{}, time.Time{})
	if _, err := m.UpdateRefreshedAuth(context.Background(), base, updated); err != nil {
		t.Fatalf("UpdateRefreshedAuth: %v", err)
	}
	stored, _ := m.GetByID(auth.ID)
	if blocked, _, _ := isAuthBlockedForModel(stored, model, time.Now()); blocked {
		t.Fatal("reporting model must be released after restoration")
	}
	if blocked, _, _ := isAuthBlockedForModel(stored, sibling, time.Now()); !blocked {
		t.Fatal("pre-existing 404 block must survive under a longer quota window")
	}
	if got := stored.ModelStates[sibling].NextRetryAfter; got.After(time.Now().Add(24 * time.Hour)) {
		t.Fatalf("sibling deadline = %v, want restored to the original ~12h 404 block", got)
	}
}

func TestDevinRefresh_RepeatedQuotaExtensionsFullyRelease(t *testing.T) {
	withQuotaCooldownEnabled(t)
	m, auth, model, sibling := devinRefreshAuthFixture(t, "devin-repeated-quota-6690")

	for _, retry := range []time.Duration{2 * time.Hour, 6 * time.Hour, 24 * time.Hour} {
		r := retry
		m.MarkResult(context.Background(), Result{
			AuthID: auth.ID, Provider: "devin", Model: model, Success: false,
			Error:           &Error{Code: "rate_limit", Message: "weekly quota", HTTPStatus: http.StatusTooManyRequests},
			RetryAfter:      &r,
			CredentialScope: true,
		})
	}
	base, _ := m.GetByID(auth.ID)
	updated := devinRefreshedObservation(base, "80%", "55%", time.Time{}, time.Time{})
	if _, err := m.UpdateRefreshedAuth(context.Background(), base, updated); err != nil {
		t.Fatalf("UpdateRefreshedAuth: %v", err)
	}
	stored, _ := m.GetByID(auth.ID)
	for _, mID := range []string{model, sibling} {
		if blocked, _, _ := isAuthBlockedForModel(stored, mID, time.Now()); blocked {
			t.Fatalf("model %s must fully release after repeated-quota restoration", mID)
		}
	}
}

func TestDevinRefresh_RepeatedPropagationReleasesHealthySibling(t *testing.T) {
	withQuotaCooldownEnabled(t)
	m, auth, model, sibling := devinRefreshAuthFixture(t, "devin-repeat-prop-6690")

	// Sibling gains a healthy ModelState from a successful request.
	m.MarkResult(context.Background(), Result{AuthID: auth.ID, Provider: "devin", Model: sibling, Success: true})
	// Two successive credential quota failures propagate to the sibling.
	for _, retry := range []time.Duration{2 * time.Hour, 6 * time.Hour} {
		r := retry
		m.MarkResult(context.Background(), Result{
			AuthID: auth.ID, Provider: "devin", Model: model, Success: false,
			Error:           &Error{Code: "rate_limit", Message: "weekly quota", HTTPStatus: http.StatusTooManyRequests},
			RetryAfter:      &r,
			CredentialScope: true,
		})
	}
	base, _ := m.GetByID(auth.ID)
	updated := devinRefreshedObservation(base, "80%", "55%", time.Time{}, time.Time{})
	if _, err := m.UpdateRefreshedAuth(context.Background(), base, updated); err != nil {
		t.Fatalf("UpdateRefreshedAuth: %v", err)
	}
	stored, _ := m.GetByID(auth.ID)
	if blocked, _, _ := isAuthBlockedForModel(stored, sibling, time.Now()); blocked {
		t.Fatal("healthy sibling must fully release after repeated propagation (earlier propagated layer is not an independent block)")
	}
}

func TestDevinRefresh_OwnFailureAfterPropagationUpdatesIndependentLayer(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
	}{
		{name: "own model 429", status: http.StatusTooManyRequests},
		{name: "own 404", status: http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withQuotaCooldownEnabled(t)
			m, auth, model, sibling := devinRefreshAuthFixture(t, "devin-own-after-prop-"+tc.name+"-6690")

			// Credential quota propagates to the sibling first.
			quotaRetry := 2 * time.Hour
			m.MarkResult(context.Background(), Result{
				AuthID: auth.ID, Provider: "devin", Model: model, Success: false,
				Error:           &Error{Code: "rate_limit", Message: "weekly quota", HTTPStatus: http.StatusTooManyRequests},
				RetryAfter:      &quotaRetry,
				CredentialScope: true,
			})
			// An in-flight sibling request then reports its own failure.
			own := Result{
				AuthID: auth.ID, Provider: "devin", Model: sibling, Success: false,
				Error: &Error{Code: "own_failure", Message: "own", HTTPStatus: tc.status},
			}
			if tc.status == http.StatusTooManyRequests {
				longRetry := 12 * time.Hour
				own.RetryAfter = &longRetry
			}
			m.MarkResult(context.Background(), own)
			base, _ := m.GetByID(auth.ID)

			updated := devinRefreshedObservation(base, "80%", "55%", time.Time{}, time.Time{})
			if _, err := m.UpdateRefreshedAuth(context.Background(), base, updated); err != nil {
				t.Fatalf("UpdateRefreshedAuth: %v", err)
			}
			stored, _ := m.GetByID(auth.ID)
			if blocked, _, _ := isAuthBlockedForModel(stored, model, time.Now()); blocked {
				t.Fatal("reporting model must be released after restoration")
			}
			if blocked, _, _ := isAuthBlockedForModel(stored, sibling, time.Now()); !blocked {
				t.Fatalf("sibling own failure (%s) must survive quota restoration", tc.name)
			}
		})
	}
}

func TestDevinQuotaShadowSurvivesCooldownPersistenceReload(t *testing.T) {
	withQuotaCooldownEnabled(t)
	m, auth, model, sibling := devinRefreshAuthFixture(t, "devin-shadow-persist-6690")

	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: "devin", Model: sibling, Success: false,
		Error: &Error{Code: "not_found", Message: "model missing", HTTPStatus: http.StatusNotFound},
	})
	quotaRetry := 72 * time.Hour
	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: "devin", Model: model, Success: false,
		Error:           &Error{Code: "rate_limit", Message: "weekly quota", HTTPStatus: http.StatusTooManyRequests},
		RetryAfter:      &quotaRetry,
		CredentialScope: true,
	})

	// Simulate a restart: records persist, then restore into a fresh manager.
	m.mu.RLock()
	records := m.cooldownStateRecordsForAuthLocked(m.auths[auth.ID], time.Now())
	m.mu.RUnlock()
	m2 := NewManager(nil, nil, nil)
	registry.GetGlobalRegistry().RegisterClient(auth.ID+"-reloaded", "devin", []*registry.ModelInfo{{ID: model}, {ID: sibling}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID + "-reloaded") })
	reloaded := &Auth{ID: auth.ID, Provider: "devin", Status: StatusActive}
	if _, err := m2.Register(context.Background(), reloaded); err != nil {
		t.Fatalf("register reloaded: %v", err)
	}
	m2.mu.Lock()
	for _, record := range records {
		m2.restoreCooldownRecordLocked(record, time.Now())
	}
	m2.mu.Unlock()

	base, _ := m2.GetByID(auth.ID)
	updated := devinRefreshedObservation(base, "80%", "55%", time.Time{}, time.Time{})
	if _, err := m2.UpdateRefreshedAuth(context.Background(), base, updated); err != nil {
		t.Fatalf("UpdateRefreshedAuth: %v", err)
	}
	stored, _ := m2.GetByID(auth.ID)
	if blocked, _, _ := isAuthBlockedForModel(stored, model, time.Now()); blocked {
		t.Fatal("reporting model must be released after reload+restoration")
	}
	if blocked, _, _ := isAuthBlockedForModel(stored, sibling, time.Now()); !blocked {
		t.Fatal("independent sibling block must survive restart and restoration")
	}
}

func TestDevinRefresh_AlternatingCredentialReportsFullyRelease(t *testing.T) {
	withQuotaCooldownEnabled(t)
	m, auth, model, sibling := devinRefreshAuthFixture(t, "devin-alternating-6690")

	// Model reports credential exhaustion first; an in-flight sibling request
	// then reports the same credential exhaustion. Neither is independent.
	for _, mID := range []string{model, sibling} {
		r := 2 * time.Hour
		m.MarkResult(context.Background(), Result{
			AuthID: auth.ID, Provider: "devin", Model: mID, Success: false,
			Error:           &Error{Code: "rate_limit", Message: "weekly quota", HTTPStatus: http.StatusTooManyRequests},
			RetryAfter:      &r,
			CredentialScope: true,
		})
	}
	base, _ := m.GetByID(auth.ID)
	updated := devinRefreshedObservation(base, "80%", "55%", time.Time{}, time.Time{})
	if _, err := m.UpdateRefreshedAuth(context.Background(), base, updated); err != nil {
		t.Fatalf("UpdateRefreshedAuth: %v", err)
	}
	stored, _ := m.GetByID(auth.ID)
	for _, mID := range []string{model, sibling} {
		if blocked, _, _ := isAuthBlockedForModel(stored, mID, time.Now()); blocked {
			t.Fatalf("model %s must fully release (credential reports are never independent)", mID)
		}
	}
}

func TestDevinRefresh_OwnFailureUnderLongQuotaKeepsOwnDeadline(t *testing.T) {
	withQuotaCooldownEnabled(t)
	m, auth, model, sibling := devinRefreshAuthFixture(t, "devin-own-deadline-6690")

	// Sibling starts with a healthy state, then a 72h credential quota wraps it.
	m.MarkResult(context.Background(), Result{AuthID: auth.ID, Provider: "devin", Model: sibling, Success: true})
	qr := 72 * time.Hour
	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: "devin", Model: model, Success: false,
		Error:           &Error{Code: "rate_limit", Message: "weekly quota", HTTPStatus: http.StatusTooManyRequests},
		RetryAfter:      &qr,
		CredentialScope: true,
	})
	// In-flight sibling 404 arrives under the wrap (own 12h block).
	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: "devin", Model: sibling, Success: false,
		Error: &Error{Code: "not_found", Message: "missing", HTTPStatus: http.StatusNotFound},
	})
	base, _ := m.GetByID(auth.ID)
	updated := devinRefreshedObservation(base, "80%", "55%", time.Time{}, time.Time{})
	if _, err := m.UpdateRefreshedAuth(context.Background(), base, updated); err != nil {
		t.Fatalf("UpdateRefreshedAuth: %v", err)
	}
	stored, _ := m.GetByID(auth.ID)
	if blocked, _, _ := isAuthBlockedForModel(stored, sibling, time.Now()); !blocked {
		t.Fatal("own 404 must survive quota restoration")
	}
	if got := stored.ModelStates[sibling].NextRetryAfter; got.After(time.Now().Add(24 * time.Hour)) {
		t.Fatalf("own deadline = %v, want ~12h (must not inherit the 72h propagated deadline)", got)
	}
}

func TestDevinClearCooldown_ClearsIndependentShadow(t *testing.T) {
	st := &ModelState{Unavailable: true, NextRetryAfter: time.Now().Add(time.Hour)}
	st.credentialQuotaShadow = &ModelState{Unavailable: true}
	st.credentialQuotaPropagated = true
	authFixture := &Auth{ID: "x", Provider: "devin", Unavailable: true, NextRetryAfter: time.Now().Add(time.Hour), ModelStates: map[string]*ModelState{"m": st}}
	clearCooldownStateForAuth(authFixture, time.Now())
	if st.credentialQuotaShadow != nil || st.credentialQuotaPropagated {
		t.Fatal("deliberate cooldown clear must drop the independent shadow and wrap marker")
	}
}

func TestDevinRestoredIndependent_KeepsStaleSuccessWatermark(t *testing.T) {
	withQuotaCooldownEnabled(t)
	m, auth, model, sibling := devinRefreshAuthFixture(t, "devin-watermark-6690")

	qr := 2 * time.Hour
	seqQuota := m.nextExecutionSeq()
	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: "devin", Model: model, Success: false,
		Error:           &Error{Code: "rate_limit", Message: "weekly quota", HTTPStatus: http.StatusTooManyRequests},
		RetryAfter:      &qr,
		CredentialScope: true,
		ExecutionSeq:    seqQuota,
	})
	// Sibling own failure dispatched after the quota failure.
	seqOwn := m.nextExecutionSeq()
	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: "devin", Model: sibling, Success: false,
		Error:        &Error{Code: "not_found", Message: "missing", HTTPStatus: http.StatusNotFound},
		ExecutionSeq: seqOwn,
	})
	base, _ := m.GetByID(auth.ID)
	updated := devinRefreshedObservation(base, "80%", "55%", time.Time{}, time.Time{})
	if _, err := m.UpdateRefreshedAuth(context.Background(), base, updated); err != nil {
		t.Fatalf("UpdateRefreshedAuth: %v", err)
	}
	// A stale success dispatched before the own failure must not clear it.
	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: "devin", Model: sibling, Success: true, ExecutionSeq: seqQuota,
	})
	stored, _ := m.GetByID(auth.ID)
	if blocked, _, _ := isAuthBlockedForModel(stored, sibling, time.Now()); !blocked {
		t.Fatal("restored independent block must keep its stale-success watermark")
	}
}

func TestDevinRefresh_ReportingModelKeepsIndependentBlock(t *testing.T) {
	withQuotaCooldownEnabled(t)
	m, auth, model, sibling := devinRefreshAuthFixture(t, "devin-reporting-own-6690")

	// Model reports credential exhaustion; sibling then reports its own 404;
	// finally the sibling itself reports credential exhaustion (in-flight).
	qr := 2 * time.Hour
	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: "devin", Model: model, Success: false,
		Error:           &Error{Code: "rate_limit", Message: "weekly quota", HTTPStatus: http.StatusTooManyRequests},
		RetryAfter:      &qr,
		CredentialScope: true,
	})
	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: "devin", Model: sibling, Success: false,
		Error: &Error{Code: "not_found", Message: "missing", HTTPStatus: http.StatusNotFound},
	})
	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: "devin", Model: sibling, Success: false,
		Error:           &Error{Code: "rate_limit", Message: "weekly quota", HTTPStatus: http.StatusTooManyRequests},
		RetryAfter:      &qr,
		CredentialScope: true,
	})
	base, _ := m.GetByID(auth.ID)
	updated := devinRefreshedObservation(base, "80%", "55%", time.Time{}, time.Time{})
	if _, err := m.UpdateRefreshedAuth(context.Background(), base, updated); err != nil {
		t.Fatalf("UpdateRefreshedAuth: %v", err)
	}
	stored, _ := m.GetByID(auth.ID)
	if blocked, _, _ := isAuthBlockedForModel(stored, model, time.Now()); blocked {
		t.Fatal("first reporting model must be released after restoration")
	}
	if blocked, _, _ := isAuthBlockedForModel(stored, sibling, time.Now()); !blocked {
		t.Fatal("reporting model's earlier independent 404 must survive restoration")
	}
}

func TestDevinRefresh_TwoWindowsNormalizeStaleWrap(t *testing.T) {
	withQuotaCooldownEnabled(t)
	m, auth, model, sibling := devinRefreshAuthFixture(t, "devin-two-windows-6690")

	// First quota window (short, expires immediately for the test clock).
	shortRetry := time.Minute
	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: "devin", Model: model, Success: false,
		Error:           &Error{Code: "rate_limit", Message: "weekly quota", HTTPStatus: http.StatusTooManyRequests},
		RetryAfter:      &shortRetry,
		CredentialScope: true,
	})
	// Force natural expiry of the first window without sleeping.
	seed, _ := m.GetByID(auth.ID)
	past := time.Now().Add(-time.Hour)
	seed.NextRetryAfter = past
	seed.Quota.NextRecoverAt = past
	for _, state := range seed.ModelStates {
		state.NextRetryAfter = past
		state.Quota.NextRecoverAt = past
	}
	if _, err := m.Update(context.Background(), seed); err != nil {
		t.Fatalf("expire seed: %v", err)
	}
	// After expiry the sibling earns a fresh independent block.
	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: "devin", Model: sibling, Success: false,
		Error: &Error{Code: "not_found", Message: "missing", HTTPStatus: http.StatusNotFound},
	})
	// A second credential quota window wraps the credential again.
	qr := 72 * time.Hour
	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: "devin", Model: model, Success: false,
		Error:           &Error{Code: "rate_limit", Message: "weekly quota", HTTPStatus: http.StatusTooManyRequests},
		RetryAfter:      &qr,
		CredentialScope: true,
	})
	base, _ := m.GetByID(auth.ID)
	updated := devinRefreshedObservation(base, "80%", "55%", time.Time{}, time.Time{})
	if _, err := m.UpdateRefreshedAuth(context.Background(), base, updated); err != nil {
		t.Fatalf("UpdateRefreshedAuth: %v", err)
	}
	stored, _ := m.GetByID(auth.ID)
	if blocked, _, _ := isAuthBlockedForModel(stored, model, time.Now()); blocked {
		t.Fatal("reporting model must be released in the second window")
	}
	if blocked, _, _ := isAuthBlockedForModel(stored, sibling, time.Now()); !blocked {
		t.Fatal("fresh independent block must survive the second window (stale wrap marker must not suppress snapshotting)")
	}
}

func TestDevinRefresh_AlternatingAfterReloadFullyReleases(t *testing.T) {
	withQuotaCooldownEnabled(t)
	m, auth, model, sibling := devinRefreshAuthFixture(t, "devin-alt-reload-6690")

	qr := 72 * time.Hour
	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: "devin", Model: model, Success: false,
		Error:           &Error{Code: "rate_limit", Message: "weekly quota", HTTPStatus: http.StatusTooManyRequests},
		RetryAfter:      &qr,
		CredentialScope: true,
	})
	m.mu.RLock()
	records := m.cooldownStateRecordsForAuthLocked(m.auths[auth.ID], time.Now())
	m.mu.RUnlock()

	m2 := NewManager(nil, nil, nil)
	registry.GetGlobalRegistry().RegisterClient(auth.ID+"-alt", "devin", []*registry.ModelInfo{{ID: model}, {ID: sibling}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(auth.ID + "-alt") })
	if _, err := m2.Register(context.Background(), &Auth{ID: auth.ID, Provider: "devin", Status: StatusActive}); err != nil {
		t.Fatalf("register: %v", err)
	}
	m2.mu.Lock()
	for _, record := range records {
		m2.restoreCooldownRecordLocked(record, time.Now())
	}
	m2.mu.Unlock()

	// Post-restart, the sibling reports credential exhaustion this time.
	m2.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: "devin", Model: sibling, Success: false,
		Error:           &Error{Code: "rate_limit", Message: "weekly quota", HTTPStatus: http.StatusTooManyRequests},
		RetryAfter:      &qr,
		CredentialScope: true,
	})
	base, _ := m2.GetByID(auth.ID)
	updated := devinRefreshedObservation(base, "80%", "55%", time.Time{}, time.Time{})
	if _, err := m2.UpdateRefreshedAuth(context.Background(), base, updated); err != nil {
		t.Fatalf("UpdateRefreshedAuth: %v", err)
	}
	stored, _ := m2.GetByID(auth.ID)
	for _, mID := range []string{model, sibling} {
		if blocked, _, _ := isAuthBlockedForModel(stored, mID, time.Now()); blocked {
			t.Fatalf("model %s must fully release (reporting provenance must survive reload)", mID)
		}
	}
}

func TestDevinRestoreDoesNotClobberNewerShadow(t *testing.T) {
	m := NewManager(nil, nil, nil)
	auth := &Auth{ID: "devin-restore-shadow-6690", Provider: "devin", Status: StatusActive}
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("register: %v", err)
	}
	// Live state already carries a newer independent shadow.
	live, _ := m.GetByID(auth.ID)
	newShadow := &ModelState{Unavailable: true, Status: StatusError, NextRetryAfter: time.Now().Add(12 * time.Hour), UpdatedAt: time.Now()}
	liveState := &ModelState{
		Unavailable: true, Status: StatusError, NextRetryAfter: time.Now().Add(72 * time.Hour),
		Quota:     QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: time.Now().Add(72 * time.Hour)},
		UpdatedAt: time.Now(),
	}
	liveState.credentialQuotaShadow = newShadow
	liveState.credentialQuotaPropagated = true
	live.ModelStates = map[string]*ModelState{"claude-opus-5-5": liveState}
	if _, err := m.Update(context.Background(), live); err != nil {
		t.Fatalf("seed live: %v", err)
	}

	staleShadow := &ModelState{Unavailable: true, Status: StatusError, NextRetryAfter: time.Now().Add(time.Hour), UpdatedAt: time.Now().Add(-2 * time.Hour)}
	record := CooldownStateRecord{
		Provider: "devin", AuthID: auth.ID, Model: "claude-opus-5-5", Status: "cooling",
		NextRetryAfter: time.Now().Add(72 * time.Hour), Reason: "credential_quota",
		Quota:     QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: time.Now().Add(72 * time.Hour)},
		UpdatedAt: time.Now().Add(-2 * time.Hour), IndependentState: staleShadow,
	}
	m.mu.Lock()
	m.restoreCooldownRecordLocked(record, time.Now())
	m.mu.Unlock()

	stored, _ := m.GetByID(auth.ID)
	got := stored.ModelStates["claude-opus-5-5"].credentialQuotaShadow
	if got == nil || !got.NextRetryAfter.After(time.Now().Add(6*time.Hour)) {
		t.Fatalf("newer live shadow clobbered by stale record, shadow=%+v", got)
	}
}

func TestCooldownRecord_IndependentStateJSONRoundTrip(t *testing.T) {
	deadline := time.Now().Add(12 * time.Hour).UTC().Truncate(time.Second)
	record := CooldownStateRecord{
		Provider: "devin", AuthID: "rt", Model: "m", Status: "cooling",
		NextRetryAfter: deadline, Reason: "credential_quota",
		Quota:                     QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: deadline},
		UpdatedAt:                 time.Now().UTC().Truncate(time.Second),
		CredentialQuotaPropagated: true,
		IndependentState: &ModelState{
			Status: StatusError, Unavailable: true, NextRetryAfter: deadline,
			Quota: QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: deadline},
		},
	}
	data, errMarshal := json.Marshal(record)
	if errMarshal != nil {
		t.Fatalf("marshal: %v", errMarshal)
	}
	var decoded CooldownStateRecord
	if errUnmarshal := json.Unmarshal(data, &decoded); errUnmarshal != nil {
		t.Fatalf("unmarshal: %v", errUnmarshal)
	}
	if !decoded.CredentialQuotaPropagated {
		t.Fatal("credential_quota_propagated lost in round-trip")
	}
	if decoded.IndependentState == nil || !decoded.IndependentState.NextRetryAfter.Equal(deadline) || decoded.IndependentState.Quota.Reason != "quota" {
		t.Fatalf("independent state lost in round-trip: %+v", decoded.IndependentState)
	}
	// Old records without the new fields must still load.
	var legacy CooldownStateRecord
	if errLegacy := json.Unmarshal([]byte(`{"auth_id":"old","next_retry_after":"2026-10-20T00:00:00Z"}`), &legacy); errLegacy != nil {
		t.Fatalf("legacy unmarshal: %v", errLegacy)
	}
	if legacy.IndependentState != nil || legacy.CredentialQuotaPropagated {
		t.Fatalf("legacy record must default cleanly, got %+v", legacy)
	}
}

func TestDevinRefresh_InFlightCompositionKeepsExactOwnDeadline(t *testing.T) {
	withQuotaCooldownEnabled(t)
	m, auth, model, sibling := devinRefreshAuthFixture(t, "devin-inflight-exact-6690")

	// 72h credential quota first; the (untracked) sibling then reports its
	// own in-flight 404, and finally reports credential exhaustion itself.
	qr := 72 * time.Hour
	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: "devin", Model: model, Success: false,
		Error:           &Error{Code: "rate_limit", Message: "weekly quota", HTTPStatus: http.StatusTooManyRequests},
		RetryAfter:      &qr,
		CredentialScope: true,
	})
	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: "devin", Model: sibling, Success: false,
		Error: &Error{Code: "not_found", Message: "missing", HTTPStatus: http.StatusNotFound},
	})
	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: "devin", Model: sibling, Success: false,
		Error:           &Error{Code: "rate_limit", Message: "weekly quota", HTTPStatus: http.StatusTooManyRequests},
		RetryAfter:      &qr,
		CredentialScope: true,
	})
	base, _ := m.GetByID(auth.ID)
	updated := devinRefreshedObservation(base, "80%", "55%", time.Time{}, time.Time{})
	if _, err := m.UpdateRefreshedAuth(context.Background(), base, updated); err != nil {
		t.Fatalf("UpdateRefreshedAuth: %v", err)
	}
	stored, _ := m.GetByID(auth.ID)
	if blocked, _, _ := isAuthBlockedForModel(stored, sibling, time.Now()); !blocked {
		t.Fatal("in-flight own 404 must survive restoration")
	}
	if got := stored.ModelStates[sibling].NextRetryAfter; got.After(time.Now().Add(24 * time.Hour)) {
		t.Fatalf("own deadline = %v, want ~12h (composed outer must not be snapshotted as independence)", got)
	}
}

func TestDevinRefresh_ReportingModelKeepsModelScoped429(t *testing.T) {
	withQuotaCooldownEnabled(t)
	m, auth, model, _ := devinRefreshAuthFixture(t, "devin-reporting-429-6690")

	// The model first earns an ordinary model-scoped 429 (12h), then reports
	// credential exhaustion itself. Restoration must keep the 12h block.
	longRetry := 12 * time.Hour
	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: "devin", Model: model, Success: false,
		Error:      &Error{Code: "rate_limit", Message: "model rate limit", HTTPStatus: http.StatusTooManyRequests},
		RetryAfter: &longRetry,
	})
	qr := 72 * time.Hour
	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: "devin", Model: model, Success: false,
		Error:           &Error{Code: "rate_limit", Message: "weekly quota", HTTPStatus: http.StatusTooManyRequests},
		RetryAfter:      &qr,
		CredentialScope: true,
	})
	base, _ := m.GetByID(auth.ID)
	updated := devinRefreshedObservation(base, "80%", "55%", time.Time{}, time.Time{})
	if _, err := m.UpdateRefreshedAuth(context.Background(), base, updated); err != nil {
		t.Fatalf("UpdateRefreshedAuth: %v", err)
	}
	stored, _ := m.GetByID(auth.ID)
	if blocked, _, _ := isAuthBlockedForModel(stored, model, time.Now()); !blocked {
		t.Fatal("genuine model-scoped 429 must survive quota restoration")
	}
	if got := stored.ModelStates[model].NextRetryAfter; got.After(time.Now().Add(24 * time.Hour)) {
		t.Fatalf("own deadline = %v, want ~12h", got)
	}
}

func TestDevinRefresh_LegacyLabelOnlySiblingReleases(t *testing.T) {
	withQuotaCooldownEnabled(t)
	m, auth, model, sibling := devinRefreshAuthFixture(t, "devin-legacy-label-6690")

	// Legacy persisted shape: propagated label, no marker/shadow fields.
	recoverAt := time.Now().Add(72 * time.Hour)
	seed, _ := m.GetByID(auth.ID)
	seed.Unavailable = true
	seed.Status = StatusError
	seed.NextRetryAfter = recoverAt
	seed.Quota = QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: recoverAt, ObservedAt: time.Now().Add(-2 * time.Hour)}
	legacySibling := &ModelState{Status: StatusError, Unavailable: true, NextRetryAfter: recoverAt, Quota: QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: recoverAt}, UpdatedAt: time.Now()}
	seed.ModelStates = map[string]*ModelState{
		model:   {Status: StatusError, Unavailable: true, NextRetryAfter: recoverAt, Quota: QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: recoverAt}},
		sibling: legacySibling,
	}
	if _, err := m.Update(context.Background(), seed); err != nil {
		t.Fatalf("seed legacy: %v", err)
	}
	// The legacy sibling now reports credential exhaustion itself.
	qr := 72 * time.Hour
	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: "devin", Model: sibling, Success: false,
		Error:           &Error{Code: "rate_limit", Message: "weekly quota", HTTPStatus: http.StatusTooManyRequests},
		RetryAfter:      &qr,
		CredentialScope: true,
	})
	base, _ := m.GetByID(auth.ID)
	updated := devinRefreshedObservation(base, "80%", "55%", time.Time{}, time.Time{})
	if _, err := m.UpdateRefreshedAuth(context.Background(), base, updated); err != nil {
		t.Fatalf("UpdateRefreshedAuth: %v", err)
	}
	stored, _ := m.GetByID(auth.ID)
	if blocked, _, _ := isAuthBlockedForModel(stored, sibling, time.Now()); blocked {
		t.Fatal("legacy propagated sibling must fully release (outer quota is not an independent block)")
	}
}

func TestDevinRefresh_ExpiredWindowThenNewOwnFailurePreserved(t *testing.T) {
	withQuotaCooldownEnabled(t)
	m, auth, model, sibling := devinRefreshAuthFixture(t, "devin-expire-success-6690")

	// Model earns a 12h independent model-scoped 429; sibling wraps it with
	// a short 2h credential quota.
	longRetry := 12 * time.Hour
	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: "devin", Model: model, Success: false,
		Error:      &Error{Code: "rate_limit", Message: "model rate limit", HTTPStatus: http.StatusTooManyRequests},
		RetryAfter: &longRetry,
	})
	shortRetry := 2 * time.Hour
	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: "devin", Model: sibling, Success: false,
		Error:           &Error{Code: "rate_limit", Message: "weekly quota", HTTPStatus: http.StatusTooManyRequests},
		RetryAfter:      &shortRetry,
		CredentialScope: true,
	})
	// Force the credential window to expire naturally (timestamps only):
	// the auth credential deadline and every propagated deadline pass,
	// while the model's own 12h deadline (max with the 2h window) survives.
	seed, _ := m.GetByID(auth.ID)
	past := time.Now().Add(-time.Hour)
	seed.NextRetryAfter = past
	seed.Quota.NextRecoverAt = past
	for _, state := range seed.ModelStates {
		if state.Quota.Reason == "credential_quota" || state.Quota.NextRecoverAt.Before(time.Now().Add(3*time.Hour)) {
			state.Quota.NextRecoverAt = past
		}
		if state.Quota.Reason == "credential_quota" {
			state.NextRetryAfter = past
		}
	}
	if rep := seed.ModelStates[sibling]; rep != nil {
		rep.NextRetryAfter = past
		rep.Quota.NextRecoverAt = past
	}
	if _, err := m.Update(context.Background(), seed); err != nil {
		t.Fatalf("expire seed: %v", err)
	}
	// Sibling succeeds after expiry; aggregation re-derives an ordinary
	// auth-level "quota" from the model's still-live deadline.
	m.MarkResult(context.Background(), Result{AuthID: auth.ID, Provider: "devin", Model: sibling, Success: true})
	// Model reports a fresh independent 24h model-scoped 429.
	freshRetry := 24 * time.Hour
	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: "devin", Model: model, Success: false,
		Error:      &Error{Code: "rate_limit", Message: "model rate limit again", HTTPStatus: http.StatusTooManyRequests},
		RetryAfter: &freshRetry,
	})
	// A new credential quota window wraps the credential again.
	qr := 72 * time.Hour
	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: "devin", Model: sibling, Success: false,
		Error:           &Error{Code: "rate_limit", Message: "weekly quota", HTTPStatus: http.StatusTooManyRequests},
		RetryAfter:      &qr,
		CredentialScope: true,
	})
	base, _ := m.GetByID(auth.ID)
	updated := devinRefreshedObservation(base, "80%", "55%", time.Time{}, time.Time{})
	if _, err := m.UpdateRefreshedAuth(context.Background(), base, updated); err != nil {
		t.Fatalf("UpdateRefreshedAuth: %v", err)
	}
	stored, _ := m.GetByID(auth.ID)
	if blocked, _, _ := isAuthBlockedForModel(stored, sibling, time.Now()); blocked {
		t.Fatal("reporting sibling must be released after restoration")
	}
	if blocked, _, _ := isAuthBlockedForModel(stored, model, time.Now()); !blocked {
		t.Fatal("fresh independent cooldown must survive the second window (expired wrap must be normalized even when aggregation re-derived an auth quota)")
	}
}

func TestDevinRestorePreservesLiveUnwrappedIndependent(t *testing.T) {
	m := NewManager(nil, nil, nil)
	auth := &Auth{ID: "devin-restore-unwrapped-6690", Provider: "devin", Status: StatusActive}
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("register: %v", err)
	}
	// Live state: a fresh unwrapped independent 404 block (post-restoration).
	live, _ := m.GetByID(auth.ID)
	liveState := &ModelState{
		Unavailable: true, Status: StatusError, NextRetryAfter: time.Now().Add(12 * time.Hour),
		LastError: &Error{Code: "not_found", Message: "missing", HTTPStatus: http.StatusNotFound},
		UpdatedAt: time.Now(),
	}
	live.ModelStates = map[string]*ModelState{"claude-opus-5-5": liveState}
	live.Unavailable = true
	live.NextRetryAfter = liveState.NextRetryAfter
	if _, err := m.Update(context.Background(), live); err != nil {
		t.Fatalf("seed live: %v", err)
	}

	// A stale wrapped record (loaded earlier, applied late) must not erase it.
	staleRecord := CooldownStateRecord{
		Provider: "devin", AuthID: auth.ID, Model: "claude-opus-5-5", Status: "cooling",
		NextRetryAfter: time.Now().Add(72 * time.Hour), Reason: "credential_quota",
		Quota:                     QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: time.Now().Add(72 * time.Hour)},
		UpdatedAt:                 time.Now().Add(-2 * time.Hour),
		CredentialQuotaPropagated: true,
	}
	m.mu.Lock()
	m.restoreCooldownRecordLocked(staleRecord, time.Now())
	m.mu.Unlock()

	stored, _ := m.GetByID(auth.ID)
	restored := stored.ModelStates["claude-opus-5-5"]
	if restored.credentialQuotaShadow == nil {
		t.Fatal("live unwrapped independent cooldown must be captured as the shadow when a wrapped record applies")
	}
	if got := restored.credentialQuotaShadow.NextRetryAfter; got.After(time.Now().Add(24*time.Hour)) || got.Before(time.Now().Add(6*time.Hour)) {
		t.Fatalf("captured shadow deadline = %v, want the ~12h independent block", got)
	}
}

func TestDevinRestoreDoesNotCaptureCredentialDerivedLegacyQuota(t *testing.T) {
	m := NewManager(nil, nil, nil)
	auth := &Auth{ID: "devin-restore-legacy-derived-6690", Provider: "devin", Status: StatusActive}
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatalf("register: %v", err)
	}
	// Legacy (pre-fix) reporting state: reason "quota" whose own error is the
	// explicit Devin weekly-exhaustion message — credential-derived, not an
	// independent rate limit.
	live, _ := m.GetByID(auth.ID)
	liveState := &ModelState{
		Unavailable: true, Status: StatusError, NextRetryAfter: time.Now().Add(5 * time.Minute),
		Quota:     QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: time.Now().Add(5 * time.Minute)},
		LastError: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "failed_precondition: Your weekly usage quota has been exhausted."},
		UpdatedAt: time.Now(),
	}
	live.ModelStates = map[string]*ModelState{"claude-opus-5-5": liveState}
	live.Unavailable = true
	live.NextRetryAfter = liveState.NextRetryAfter
	if _, err := m.Update(context.Background(), live); err != nil {
		t.Fatalf("seed live: %v", err)
	}

	wrappedRecord := CooldownStateRecord{
		Provider: "devin", AuthID: auth.ID, Model: "claude-opus-5-5", Status: "cooling",
		NextRetryAfter: time.Now().Add(72 * time.Hour), Reason: "credential_quota",
		Quota:     QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: time.Now().Add(72 * time.Hour)},
		UpdatedAt: time.Now().Add(-time.Hour),
	}
	m.mu.Lock()
	m.restoreCooldownRecordLocked(wrappedRecord, time.Now())
	m.mu.Unlock()

	stored, _ := m.GetByID(auth.ID)
	if got := stored.ModelStates["claude-opus-5-5"].credentialQuotaShadow; got != nil {
		t.Fatalf("credential-derived legacy quota must not be captured as independence, shadow=%+v", got)
	}
}

func TestDevinRestoreIndependentRecordUnderLiveWrapPreserved(t *testing.T) {
	withQuotaCooldownEnabled(t)
	m, auth, model, sibling := devinRefreshAuthFixture(t, "devin-restore-under-wrap-6690")

	// Live credential quota wraps the credential (model has no state yet).
	qr := 72 * time.Hour
	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: "devin", Model: sibling, Success: false,
		Error:           &Error{Code: "rate_limit", Message: "weekly quota", HTTPStatus: http.StatusTooManyRequests},
		RetryAfter:      &qr,
		CredentialScope: true,
	})
	// A delayed unwrapped independent record for the model applies late.
	record := CooldownStateRecord{
		Provider: "devin", AuthID: auth.ID, Model: model, Status: "cooling",
		NextRetryAfter: time.Now().Add(12 * time.Hour), Reason: "quota",
		Quota:     QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: time.Now().Add(12 * time.Hour)},
		LastError: &Error{Code: "rate_limit", Message: "model rate limit", HTTPStatus: http.StatusTooManyRequests},
		UpdatedAt: time.Now().Add(-time.Minute),
	}
	m.mu.Lock()
	m.restoreCooldownRecordLocked(record, time.Now())
	m.mu.Unlock()

	base, _ := m.GetByID(auth.ID)
	updated := devinRefreshedObservation(base, "80%", "55%", time.Time{}, time.Time{})
	if _, err := m.UpdateRefreshedAuth(context.Background(), base, updated); err != nil {
		t.Fatalf("UpdateRefreshedAuth: %v", err)
	}
	stored, _ := m.GetByID(auth.ID)
	if blocked, _, _ := isAuthBlockedForModel(stored, sibling, time.Now()); blocked {
		t.Fatal("reporting sibling must be released after restoration")
	}
	if blocked, _, _ := isAuthBlockedForModel(stored, model, time.Now()); !blocked {
		t.Fatal("restored independent record beneath a live wrap must survive quota restoration")
	}
}

func TestDevinFirstWrapDoesNotCaptureLegacyExhaustion(t *testing.T) {
	t.Run("sibling", func(t *testing.T) {
		withQuotaCooldownEnabled(t)
		m, auth, model, sibling := devinRefreshAuthFixture(t, "devin-legacy-firstwrap-sib-6690")
		seed, _ := m.GetByID(auth.ID)
		seed.ModelStates = map[string]*ModelState{
			sibling: {
				Status: StatusError, Unavailable: true, NextRetryAfter: time.Now().Add(5 * time.Minute),
				Quota:     QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: time.Now().Add(5 * time.Minute)},
				LastError: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "failed_precondition: Your weekly usage quota has been exhausted."},
				UpdatedAt: time.Now(),
			},
		}
		if _, err := m.Update(context.Background(), seed); err != nil {
			t.Fatalf("seed: %v", err)
		}
		qr := 72 * time.Hour
		m.MarkResult(context.Background(), Result{
			AuthID: auth.ID, Provider: "devin", Model: model, Success: false,
			Error:           &Error{Code: "rate_limit", Message: "weekly quota", HTTPStatus: http.StatusTooManyRequests},
			RetryAfter:      &qr,
			CredentialScope: true,
		})
		base, _ := m.GetByID(auth.ID)
		updated := devinRefreshedObservation(base, "80%", "55%", time.Time{}, time.Time{})
		if _, err := m.UpdateRefreshedAuth(context.Background(), base, updated); err != nil {
			t.Fatalf("UpdateRefreshedAuth: %v", err)
		}
		stored, _ := m.GetByID(auth.ID)
		if blocked, _, _ := isAuthBlockedForModel(stored, sibling, time.Now()); blocked {
			t.Fatal("legacy credential-derived sibling must fully release")
		}
	})
	t.Run("reporting", func(t *testing.T) {
		withQuotaCooldownEnabled(t)
		m, auth, model, _ := devinRefreshAuthFixture(t, "devin-legacy-firstwrap-rep-6690")
		seed, _ := m.GetByID(auth.ID)
		seed.ModelStates = map[string]*ModelState{
			model: {
				Status: StatusError, Unavailable: true, NextRetryAfter: time.Now().Add(5 * time.Minute),
				Quota:     QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: time.Now().Add(5 * time.Minute)},
				LastError: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "failed_precondition: Your weekly usage quota has been exhausted."},
				UpdatedAt: time.Now(),
			},
		}
		if _, err := m.Update(context.Background(), seed); err != nil {
			t.Fatalf("seed: %v", err)
		}
		qr := 72 * time.Hour
		m.MarkResult(context.Background(), Result{
			AuthID: auth.ID, Provider: "devin", Model: model, Success: false,
			Error:           &Error{Code: "rate_limit", Message: "weekly quota", HTTPStatus: http.StatusTooManyRequests},
			RetryAfter:      &qr,
			CredentialScope: true,
		})
		base, _ := m.GetByID(auth.ID)
		updated := devinRefreshedObservation(base, "80%", "55%", time.Time{}, time.Time{})
		if _, err := m.UpdateRefreshedAuth(context.Background(), base, updated); err != nil {
			t.Fatalf("UpdateRefreshedAuth: %v", err)
		}
		stored, _ := m.GetByID(auth.ID)
		if blocked, _, _ := isAuthBlockedForModel(stored, model, time.Now()); blocked {
			t.Fatal("legacy credential-derived reporting state must fully release")
		}
	})
}

type devinFalseRefreshEvaluator struct{}

func (devinFalseRefreshEvaluator) ShouldRefresh(time.Time, *Auth) bool { return false }

func TestDevinRevalidate_EvaluatorFalseKeepsFutureSchedule(t *testing.T) {
	m, auth, model, sibling := devinRefreshAuthFixture(t, "devin-evaluator-6690")
	base := devinExhaustedAuth(t, m, auth.ID, model, sibling, time.Now().Add(72*time.Hour))
	base.LastRefreshedAt = time.Now().Add(-2 * time.Hour) // revalidation due
	base.Runtime = devinFalseRefreshEvaluator{}

	if m.shouldRefresh(base, time.Now()) {
		t.Fatal("custom evaluator false must keep precedence in shouldRefresh")
	}
	next, ok := nextRefreshCheckAt(time.Now(), base, 5*time.Second)
	if !ok {
		t.Fatal("evaluator-managed auth must remain scheduled")
	}
	if !next.After(time.Now()) {
		t.Fatalf("next check = %v, must be in the future to avoid a zero-delay scheduling loop", next)
	}
}

func TestDevinRefresh_RestoredKeepsUnrelatedModelCooldown(t *testing.T) {
	withQuotaCooldownEnabled(t)
	m, auth, model, sibling := devinRefreshAuthFixture(t, "devin-refresh-unrelated-6690")
	recoverAt := time.Now().Add(72 * time.Hour)
	base := devinExhaustedAuth(t, m, auth.ID, model, sibling, recoverAt)

	// Give one model an unrelated, longer non-quota block that must survive.
	// Mark it as a freshly written own state (not a stale in-flight result).
	seed, _ := m.GetByID(auth.ID)
	longBlock := time.Now().Add(120 * time.Hour)
	seed.ModelStates[model].Quota = QuotaState{Exceeded: true, Reason: "cloudflare challenge", NextRecoverAt: longBlock}
	seed.ModelStates[model].NextRetryAfter = longBlock
	seed.ModelStates[model].UpdatedAt = time.Now()
	if _, err := m.Update(context.Background(), seed); err != nil {
		t.Fatalf("seed unrelated block: %v", err)
	}

	updated := devinRefreshedObservation(base, "80%", "55%", time.Time{}, time.Time{})
	if _, err := m.UpdateRefreshedAuth(context.Background(), base, updated); err != nil {
		t.Fatalf("UpdateRefreshedAuth: %v", err)
	}

	stored, _ := m.GetByID(auth.ID)
	if stored == nil {
		t.Fatal("stored auth missing")
	}
	if stored.Quota.Reason == "credential_quota" {
		t.Fatalf("credential quota must be released, got %+v", stored.Quota)
	}
	if blocked, _, _ := isAuthBlockedForModel(stored, model, time.Now()); !blocked {
		t.Fatal("unrelated cloudflare block must survive quota restoration")
	}
	if blocked, _, _ := isAuthBlockedForModel(stored, sibling, time.Now()); blocked {
		t.Fatal("propagated sibling block must be released after quota restoration")
	}
}
