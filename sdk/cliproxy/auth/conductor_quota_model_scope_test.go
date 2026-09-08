package auth

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// Codex reports usage_limit_reached without naming the exhausted bucket, and ChatGPT
// plans meter several independent buckets per account. These tests pin the opt-in
// model-scoped behaviour: one model's limit must not park a sibling whose own bucket is
// untouched, while a genuinely account-wide limit still escalates.

func withModelScopedQuota(t *testing.T, enabled bool) {
	t.Helper()
	prevScope := modelScopedQuotaCooldown.Load()
	modelScopedQuotaCooldown.Store(enabled)
	prevCool := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() {
		modelScopedQuotaCooldown.Store(prevScope)
		quotaCooldownDisabled.Store(prevCool)
	})
}

func markCredentialScopedQuota(m *Manager, auth *Auth, model string, retry time.Duration) {
	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: auth.Provider, Model: model,
		Success: false, RetryAfter: &retry, CredentialScope: true,
		Error: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "usage_limit_reached"},
	})
}

// Default (flag off) keeps the historical behaviour: one model's usage limit parks the
// whole credential immediately.
func TestMarkResult_CredentialScopePropagatesWhenModelScopingDisabled(t *testing.T) {
	withModelScopedQuota(t, false)
	m, auth := newCooldownMonotonicManager(t, "model-a", "model-b")

	markCredentialScopedQuota(m, auth, "model-a", 30*time.Minute)

	updated, ok := m.GetByID(auth.ID)
	if !ok {
		t.Fatal("auth not found")
	}
	if updated.Quota.Reason != "credential_quota" {
		t.Fatalf("Quota.Reason = %q, want credential_quota with model scoping disabled", updated.Quota.Reason)
	}
	if blocked, _, _ := isAuthBlockedForModel(updated, "model-b", time.Now()); !blocked {
		t.Fatal("model-b should be parked when model scoping is disabled")
	}
}

// Flag on: the first usage limit cools only the model that produced it.
func TestMarkResult_ModelScopedQuotaLeavesSiblingServing(t *testing.T) {
	withModelScopedQuota(t, true)
	m, auth := newCooldownMonotonicManager(t, "model-a", "model-b")

	markCredentialScopedQuota(m, auth, "model-a", 30*time.Minute)

	updated, ok := m.GetByID(auth.ID)
	if !ok {
		t.Fatal("auth not found")
	}
	now := time.Now()
	if blocked, _, _ := isAuthBlockedForModel(updated, "model-a", now); !blocked {
		t.Fatal("model-a produced the usage limit and must be cooling")
	}
	if blocked, reason, next := isAuthBlockedForModel(updated, "model-b", now); blocked {
		t.Fatalf("model-b was parked by model-a's usage limit: reason=%v next=%v", reason, next)
	}
	if updated.Quota.Reason == "credential_quota" && updated.Quota.NextRecoverAt.After(now) {
		t.Fatalf("credential-wide quota applied on the first model failure: %+v", updated.Quota)
	}
}

// Flag on: a second distinct model reporting a live quota cooldown escalates to
// credential-wide, so a genuinely account-wide limit still converges.
func TestMarkResult_ModelScopedQuotaEscalatesOnSecondModel(t *testing.T) {
	withModelScopedQuota(t, true)
	m, auth := newCooldownMonotonicManager(t, "model-a", "model-b")

	markCredentialScopedQuota(m, auth, "model-a", 30*time.Minute)
	markCredentialScopedQuota(m, auth, "model-b", 30*time.Minute)

	updated, ok := m.GetByID(auth.ID)
	if !ok {
		t.Fatal("auth not found")
	}
	if updated.Quota.Reason != "credential_quota" {
		t.Fatalf("Quota.Reason = %q, want credential_quota after a second model failed", updated.Quota.Reason)
	}
	now := time.Now()
	for _, model := range []string{"model-a", "model-b"} {
		if blocked, _, _ := isAuthBlockedForModel(updated, model, now); !blocked {
			t.Fatalf("model %q should be parked after escalation", model)
		}
	}
}

// Escalation must not be triggered by a sibling whose cooldown has already expired,
// otherwise an old failure would keep re-escalating forever.
func TestQuotaCredentialEscalationIgnoresExpiredSiblingCooldown(t *testing.T) {
	now := time.Date(2026, 9, 8, 3, 0, 0, 0, time.UTC)
	current := &ModelState{}
	auth := &Auth{ID: "expired-sibling", Provider: "codex", ModelStates: map[string]*ModelState{
		"current": current,
		"stale":   {Quota: QuotaState{Exceeded: true, NextRecoverAt: now.Add(-time.Hour)}},
	}}
	if quotaCredentialEscalationWarranted(auth, current, now) {
		t.Fatal("an expired sibling cooldown must not warrant escalation")
	}
	auth.ModelStates["stale"].Quota.NextRecoverAt = now.Add(time.Hour)
	if !quotaCredentialEscalationWarranted(auth, current, now) {
		t.Fatal("a live sibling cooldown must warrant escalation")
	}
}

// An existing credential-wide quota on the auth itself is also evidence, so a restored
// or reloaded credential does not lose its escalated state.
func TestQuotaCredentialEscalationHonorsExistingCredentialQuota(t *testing.T) {
	now := time.Date(2026, 9, 8, 3, 0, 0, 0, time.UTC)
	current := &ModelState{}
	auth := &Auth{ID: "already-escalated", Provider: "codex", ModelStates: map[string]*ModelState{"current": current}}
	auth.Quota = QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: now.Add(time.Hour)}
	if !quotaCredentialEscalationWarranted(auth, current, now) {
		t.Fatal("a live credential-wide quota must warrant escalation")
	}
	// A per-model reason must not count as credential-wide evidence.
	auth.Quota.Reason = "quota"
	if quotaCredentialEscalationWarranted(auth, current, now) {
		t.Fatal("a model-scoped quota reason must not warrant escalation")
	}
}

func TestQuotaCredentialEscalationNilAuth(t *testing.T) {
	if quotaCredentialEscalationWarranted(nil, nil, time.Now()) {
		t.Fatal("nil auth must not warrant escalation")
	}
}
