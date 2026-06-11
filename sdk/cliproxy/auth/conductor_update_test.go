package auth

import (
	"context"
	"testing"
	"time"
)

func TestManager_Update_PreservesModelStates(t *testing.T) {
	m := NewManager(nil, nil, nil)

	model := "test-model"
	backoffLevel := 7

	if _, errRegister := m.Register(context.Background(), &Auth{
		ID:       "auth-1",
		Provider: "claude",
		Metadata: map[string]any{"k": "v"},
		ModelStates: map[string]*ModelState{
			model: {
				Quota: QuotaState{BackoffLevel: backoffLevel},
			},
		},
	}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	if _, errUpdate := m.Update(context.Background(), &Auth{
		ID:       "auth-1",
		Provider: "claude",
		Metadata: map[string]any{"k": "v2"},
	}); errUpdate != nil {
		t.Fatalf("update auth: %v", errUpdate)
	}

	updated, ok := m.GetByID("auth-1")
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	if len(updated.ModelStates) == 0 {
		t.Fatalf("expected ModelStates to be preserved")
	}
	state := updated.ModelStates[model]
	if state == nil {
		t.Fatalf("expected model state to be present")
	}
	if state.Quota.BackoffLevel != backoffLevel {
		t.Fatalf("expected BackoffLevel to be %d, got %d", backoffLevel, state.Quota.BackoffLevel)
	}
}

func TestManager_Update_DisabledExistingDoesNotInheritModelStates(t *testing.T) {
	m := NewManager(nil, nil, nil)

	// Register a disabled auth with existing ModelStates.
	if _, err := m.Register(context.Background(), &Auth{
		ID:       "auth-disabled",
		Provider: "claude",
		Disabled: true,
		Status:   StatusDisabled,
		ModelStates: map[string]*ModelState{
			"stale-model": {
				Quota: QuotaState{BackoffLevel: 5},
			},
		},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	// Update with empty ModelStates — should NOT inherit stale states.
	if _, err := m.Update(context.Background(), &Auth{
		ID:       "auth-disabled",
		Provider: "claude",
		Disabled: true,
		Status:   StatusDisabled,
	}); err != nil {
		t.Fatalf("update auth: %v", err)
	}

	updated, ok := m.GetByID("auth-disabled")
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	if len(updated.ModelStates) != 0 {
		t.Fatalf("expected disabled auth NOT to inherit ModelStates, got %d entries", len(updated.ModelStates))
	}
}

func TestManager_Update_ActiveToDisabledDoesNotInheritModelStates(t *testing.T) {
	m := NewManager(nil, nil, nil)

	// Register an active auth with ModelStates (simulates existing live auth).
	if _, err := m.Register(context.Background(), &Auth{
		ID:       "auth-a2d",
		Provider: "claude",
		Status:   StatusActive,
		ModelStates: map[string]*ModelState{
			"stale-model": {
				Quota: QuotaState{BackoffLevel: 9},
			},
		},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	// File watcher deletes config → synthesizes Disabled=true auth → Update.
	// Even though existing is active, incoming auth is disabled → skip inheritance.
	if _, err := m.Update(context.Background(), &Auth{
		ID:       "auth-a2d",
		Provider: "claude",
		Disabled: true,
		Status:   StatusDisabled,
	}); err != nil {
		t.Fatalf("update auth: %v", err)
	}

	updated, ok := m.GetByID("auth-a2d")
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	if len(updated.ModelStates) != 0 {
		t.Fatalf("expected active→disabled transition NOT to inherit ModelStates, got %d entries", len(updated.ModelStates))
	}
}

func TestManager_Update_DisabledToActiveDoesNotInheritStaleModelStates(t *testing.T) {
	m := NewManager(nil, nil, nil)

	// Register a disabled auth with stale ModelStates.
	if _, err := m.Register(context.Background(), &Auth{
		ID:       "auth-d2a",
		Provider: "claude",
		Disabled: true,
		Status:   StatusDisabled,
		ModelStates: map[string]*ModelState{
			"stale-model": {
				Quota: QuotaState{BackoffLevel: 4},
			},
		},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	// Re-enable: incoming auth is active, existing is disabled → skip inheritance.
	if _, err := m.Update(context.Background(), &Auth{
		ID:       "auth-d2a",
		Provider: "claude",
		Status:   StatusActive,
	}); err != nil {
		t.Fatalf("update auth: %v", err)
	}

	updated, ok := m.GetByID("auth-d2a")
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	if len(updated.ModelStates) != 0 {
		t.Fatalf("expected disabled→active transition NOT to inherit stale ModelStates, got %d entries", len(updated.ModelStates))
	}
}

func TestManager_Update_ActiveInheritsModelStates(t *testing.T) {
	m := NewManager(nil, nil, nil)

	model := "active-model"
	backoffLevel := 3

	// Register an active auth with ModelStates.
	if _, err := m.Register(context.Background(), &Auth{
		ID:       "auth-active",
		Provider: "claude",
		Status:   StatusActive,
		ModelStates: map[string]*ModelState{
			model: {
				Quota: QuotaState{BackoffLevel: backoffLevel},
			},
		},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	// Update with empty ModelStates — both sides active → SHOULD inherit.
	if _, err := m.Update(context.Background(), &Auth{
		ID:       "auth-active",
		Provider: "claude",
		Status:   StatusActive,
	}); err != nil {
		t.Fatalf("update auth: %v", err)
	}

	updated, ok := m.GetByID("auth-active")
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	if len(updated.ModelStates) == 0 {
		t.Fatalf("expected active auth to inherit ModelStates")
	}
	state := updated.ModelStates[model]
	if state == nil {
		t.Fatalf("expected model state to be present")
	}
	if state.Quota.BackoffLevel != backoffLevel {
		t.Fatalf("expected BackoffLevel to be %d, got %d", backoffLevel, state.Quota.BackoffLevel)
	}
}

func TestManager_Update_PlanChangeDoesNotInheritQuotaModelStates(t *testing.T) {
	m := NewManager(nil, nil, nil)

	model := "codex-model"
	nextRetry := time.Now().Add(30 * time.Minute)

	if _, err := m.Register(context.Background(), &Auth{
		ID:       "auth-plan-upgrade",
		Provider: "codex",
		Status:   StatusActive,
		Attributes: map[string]string{
			"plan_type": "free",
		},
		ModelStates: map[string]*ModelState{
			model: {
				Status:         StatusError,
				StatusMessage:  "quota exhausted",
				Unavailable:    true,
				NextRetryAfter: nextRetry,
				Quota: QuotaState{
					Exceeded:      true,
					Reason:        "quota",
					NextRecoverAt: nextRetry,
					BackoffLevel:  5,
				},
			},
		},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	if _, err := m.Update(context.Background(), &Auth{
		ID:       "auth-plan-upgrade",
		Provider: "codex",
		Status:   StatusActive,
		Attributes: map[string]string{
			"plan_type": "plus",
		},
	}); err != nil {
		t.Fatalf("update auth: %v", err)
	}

	updated, ok := m.GetByID("auth-plan-upgrade")
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	if len(updated.ModelStates) != 0 {
		t.Fatalf("expected plan change to clear stale ModelStates, got %d entries", len(updated.ModelStates))
	}
}

func TestManager_Update_PlanChangeClearsClonedQuotaModelStates(t *testing.T) {
	m := NewManager(nil, nil, nil)

	model := "codex-model"
	nextRetry := time.Now().Add(30 * time.Minute)

	registered, err := m.Register(context.Background(), &Auth{
		ID:       "auth-cloned-plan-upgrade",
		Provider: "codex",
		Status:   StatusActive,
		Attributes: map[string]string{
			"plan_type": "free",
		},
		ModelStates: map[string]*ModelState{
			model: {
				Status:         StatusError,
				StatusMessage:  "quota exhausted",
				Unavailable:    true,
				NextRetryAfter: nextRetry,
				Quota: QuotaState{
					Exceeded:      true,
					Reason:        "quota",
					NextRecoverAt: nextRetry,
					BackoffLevel:  5,
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("register auth: %v", err)
	}

	refreshed := registered.Clone()
	refreshed.Attributes["plan_type"] = "plus"
	refreshed.Status = StatusError
	refreshed.StatusMessage = "quota exhausted"
	refreshed.Unavailable = true
	refreshed.NextRetryAfter = nextRetry
	refreshed.Quota = QuotaState{
		Exceeded:      true,
		Reason:        "quota",
		NextRecoverAt: nextRetry,
		BackoffLevel:  5,
	}
	if len(refreshed.ModelStates) == 0 {
		t.Fatalf("expected cloned refresh auth to carry old ModelStates")
	}
	if _, err := m.Update(context.Background(), refreshed); err != nil {
		t.Fatalf("update auth: %v", err)
	}

	updated, ok := m.GetByID("auth-cloned-plan-upgrade")
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	if len(updated.ModelStates) != 0 {
		t.Fatalf("expected cloned plan change to clear stale ModelStates, got %d entries", len(updated.ModelStates))
	}
	if updated.Unavailable || !updated.NextRetryAfter.IsZero() || updated.Quota.Exceeded || updated.Quota.BackoffLevel != 0 || updated.StatusMessage != "" {
		t.Fatalf("expected cloned plan change to clear aggregate cooldown, got unavailable=%v next=%v quota=%+v message=%q", updated.Unavailable, updated.NextRetryAfter, updated.Quota, updated.StatusMessage)
	}
	if updated.Status != StatusActive {
		t.Fatalf("expected cloned plan change to restore active status, got %s", updated.Status)
	}
}

func TestManager_Update_SamePlanInheritsQuotaModelStates(t *testing.T) {
	m := NewManager(nil, nil, nil)

	model := "codex-model"
	nextRetry := time.Now().Add(30 * time.Minute)

	if _, err := m.Register(context.Background(), &Auth{
		ID:       "auth-same-plan",
		Provider: "codex",
		Status:   StatusActive,
		Attributes: map[string]string{
			"plan_type": "plus",
		},
		ModelStates: map[string]*ModelState{
			model: {
				Status:         StatusError,
				StatusMessage:  "quota exhausted",
				Unavailable:    true,
				NextRetryAfter: nextRetry,
				Quota: QuotaState{
					Exceeded:      true,
					Reason:        "quota",
					NextRecoverAt: nextRetry,
					BackoffLevel:  5,
				},
			},
		},
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	if _, err := m.Update(context.Background(), &Auth{
		ID:       "auth-same-plan",
		Provider: "codex",
		Status:   StatusActive,
		Attributes: map[string]string{
			"plan_type": "plus",
		},
	}); err != nil {
		t.Fatalf("update auth: %v", err)
	}

	updated, ok := m.GetByID("auth-same-plan")
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	if len(updated.ModelStates) == 0 {
		t.Fatalf("expected same plan to inherit ModelStates")
	}
	state := updated.ModelStates[model]
	if state == nil || !state.Quota.Exceeded || state.Quota.BackoffLevel != 5 {
		t.Fatalf("expected inherited quota state, got %+v", state)
	}
}

func TestManager_Update_SamePlanWithBackfilledCapacityMetadataKeepsClonedModelStates(t *testing.T) {
	m := NewManager(nil, nil, nil)

	model := "codex-model"
	nextRetry := time.Now().Add(30 * time.Minute)

	registered, err := m.Register(context.Background(), &Auth{
		ID:       "auth-backfilled-same-plan",
		Provider: "codex",
		Status:   StatusActive,
		Attributes: map[string]string{
			"plan_type": "plus",
		},
		Metadata: map[string]any{
			"email": "user@example.com",
		},
		ModelStates: map[string]*ModelState{
			model: {
				Status:         StatusError,
				StatusMessage:  "quota exhausted",
				Unavailable:    true,
				NextRetryAfter: nextRetry,
				Quota: QuotaState{
					Exceeded:      true,
					Reason:        "quota",
					NextRecoverAt: nextRetry,
					BackoffLevel:  5,
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("register auth: %v", err)
	}

	refreshed := registered.Clone()
	refreshed.Metadata["account_id"] = "acct-123"
	refreshed.Metadata["chatgpt_account_id"] = "acct-123"
	refreshed.Metadata["chatgpt_plan_type"] = "plus"
	refreshed.Metadata["chatgpt_subscription_active_until"] = "2026-07-12T00:00:00Z"
	if _, err := m.Update(context.Background(), refreshed); err != nil {
		t.Fatalf("update auth: %v", err)
	}

	updated, ok := m.GetByID("auth-backfilled-same-plan")
	if !ok || updated == nil {
		t.Fatalf("expected auth to be present")
	}
	state := updated.ModelStates[model]
	if state == nil || !state.Quota.Exceeded || state.Quota.BackoffLevel != 5 {
		t.Fatalf("expected same plan metadata backfill to keep cloned quota state, got %+v", state)
	}
}

func TestCapacityIdentitySignatureNormalizesPointersAndIgnoresEmptyValues(t *testing.T) {
	planType := "plus"
	subscriptionUntil := time.Date(2026, 6, 12, 0, 0, 0, 0, time.UTC)

	left := authCapacityIdentitySignature(&Auth{
		Attributes: map[string]string{
			"plan_type": "",
		},
		Metadata: map[string]any{
			"chatgpt_plan_type":                 &planType,
			"chatgpt_subscription_active_until": &subscriptionUntil,
			"subscription_status":               "",
		},
	})
	right := authCapacityIdentitySignature(&Auth{
		Metadata: map[string]any{
			"chatgpt_plan_type":                 "plus",
			"chatgpt_subscription_active_until": "2026-06-12T00:00:00Z",
		},
	})

	if !capacityIdentitySignatureEqual(left, right) {
		t.Fatalf("expected normalized capacity signatures to match, left=%v right=%v", left, right)
	}
}
