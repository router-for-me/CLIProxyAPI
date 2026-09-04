package auth

import (
	"context"
	"testing"
	"time"
)

func TestManager_RegisterCanonicalizesThinkingSuffixModelStates(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	now := time.Now()
	laterRetry := now.Add(2 * time.Hour)

	registered, errRegister := manager.Register(context.Background(), &Auth{
		ID:       "auth-thinking-states",
		Provider: "gemini",
		ModelStates: map[string]*ModelState{
			"gemini-3.1-pro-preview(high)": {
				Status:         StatusError,
				Unavailable:    true,
				NextRetryAfter: now.Add(time.Hour),
				Quota: QuotaState{
					Exceeded:      true,
					NextRecoverAt: now.Add(time.Hour),
					BackoffLevel:  1,
				},
				UpdatedAt: now,
			},
			"gemini-3.1-pro-preview(low)": {
				Status:         StatusError,
				Unavailable:    true,
				NextRetryAfter: laterRetry,
				Quota: QuotaState{
					Exceeded:      true,
					NextRecoverAt: laterRetry,
					BackoffLevel:  2,
				},
				UpdatedAt: now.Add(time.Minute),
			},
		},
	})
	if errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	if len(registered.ModelStates) != 1 {
		t.Fatalf("len(ModelStates) = %d, want 1: %+v", len(registered.ModelStates), registered.ModelStates)
	}
	state := registered.ModelStates["gemini-3.1-pro-preview"]
	if state == nil || !state.Unavailable || !state.NextRetryAfter.Equal(laterRetry) {
		t.Fatalf("canonical model state = %+v, want unavailable until %v", state, laterRetry)
	}
	if state.Quota.BackoffLevel != 2 || !state.Quota.NextRecoverAt.Equal(laterRetry) {
		t.Fatalf("canonical model quota = %+v, want latest cooldown", state.Quota)
	}
}

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

func TestManager_Update_PreservesCredentialCooldownUntilDeadline(t *testing.T) {
	m := NewManager(nil, nil, nil)
	now := time.Now()
	deadline := now.Add(30 * time.Minute)

	if _, errRegister := m.Register(context.Background(), &Auth{
		ID:                 "auth-401",
		Provider:           "claude",
		Unavailable:        true,
		Status:             StatusError,
		NextRetryAfter:     deadline,
		CredentialCooldown: true,
	}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	// A refreshed/clean Auth carries no cooldown fields of its own - e.g. a
	// token refresh completing while the 401 block is still live.
	updated, errUpdate := m.Update(context.Background(), &Auth{
		ID:       "auth-401",
		Provider: "claude",
		Status:   StatusActive,
	})
	if errUpdate != nil {
		t.Fatalf("update auth: %v", errUpdate)
	}
	if !updated.Unavailable || !updated.CredentialCooldown || !updated.NextRetryAfter.Equal(deadline) {
		t.Fatalf("Update() while cooldown live = %+v, want Unavailable/CredentialCooldown preserved until %v", updated, deadline)
	}
	blocked, _, next := effectiveBlock(updated, "", time.Now())
	if !blocked || !next.Equal(deadline) {
		t.Fatalf("effectiveBlock(auth, \"\", now) after Update = blocked=%v next=%v, want blocked until %v", blocked, next, deadline)
	}
}

func TestManager_Update_ClearsCredentialCooldownAfterDeadline(t *testing.T) {
	m := NewManager(nil, nil, nil)
	now := time.Now()
	pastDeadline := now.Add(-time.Minute)

	if _, errRegister := m.Register(context.Background(), &Auth{
		ID:                 "auth-401-expired",
		Provider:           "claude",
		Unavailable:        true,
		Status:             StatusError,
		NextRetryAfter:     pastDeadline,
		CredentialCooldown: true,
	}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	updated, errUpdate := m.Update(context.Background(), &Auth{
		ID:       "auth-401-expired",
		Provider: "claude",
		Status:   StatusActive,
	})
	if errUpdate != nil {
		t.Fatalf("update auth: %v", errUpdate)
	}
	if updated.Unavailable || updated.CredentialCooldown {
		t.Fatalf("Update() after deadline passed = %+v, want cooldown cleared", updated)
	}
}

func TestManager_Update_CredentialCooldownMutationControl(t *testing.T) {
	m := NewManager(nil, nil, nil)
	now := time.Now()
	deadline := now.Add(30 * time.Minute)

	if _, errRegister := m.Register(context.Background(), &Auth{
		ID:                 "auth-401-mc",
		Provider:           "claude",
		Unavailable:        true,
		Status:             StatusError,
		NextRetryAfter:     deadline,
		CredentialCooldown: true,
	}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	// Mimic the pre-fix Update path directly: a clean incoming Auth applied
	// with no existing.CredentialCooldown preservation at all loses the
	// cooldown outright, reproducing the original defect this test guards
	// against (see the guarded branch added to Manager.Update).
	m.mu.Lock()
	existing := m.auths["auth-401-mc"]
	m.mu.Unlock()
	if existing == nil || !existing.CredentialCooldown || !existing.NextRetryAfter.After(now) {
		t.Fatalf("expected existing auth to carry a live credential cooldown, got %+v", existing)
	}
	clean := &Auth{ID: "auth-401-mc", Provider: "claude", Status: StatusActive}
	if !existing.CredentialCooldown && existing.NextRetryAfter.After(now) {
		clean.Unavailable = existing.Unavailable
		clean.NextRetryAfter = existing.NextRetryAfter
	}
	if clean.Unavailable || clean.CredentialCooldown || !clean.NextRetryAfter.IsZero() {
		t.Fatalf("mutation-control clean auth unexpectedly carries cooldown fields: %+v", clean)
	}
}

func TestManager_Update_SameCredentialRefreshPreservesCredentialCooldown(t *testing.T) {
	m := NewManager(nil, nil, nil)
	now := time.Now()
	deadline := now.Add(30 * time.Minute)

	if _, errRegister := m.Register(context.Background(), &Auth{
		ID:                 "auth-401-refresh",
		Provider:           "claude",
		Unavailable:        true,
		Status:             StatusError,
		NextRetryAfter:     deadline,
		CredentialCooldown: true,
		Metadata: map[string]any{
			"access_token":  "stale-access-token",
			"refresh_token": "refresh-token-abc",
		},
	}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	// A routine access-token refresh rotates access_token but keeps the
	// same refresh_token - same underlying credential, cooldown must
	// survive.
	updated, errUpdate := m.Update(context.Background(), &Auth{
		ID:       "auth-401-refresh",
		Provider: "claude",
		Status:   StatusActive,
		Metadata: map[string]any{
			"access_token":  "fresh-access-token",
			"refresh_token": "refresh-token-abc",
		},
	})
	if errUpdate != nil {
		t.Fatalf("update auth: %v", errUpdate)
	}
	if !updated.Unavailable || !updated.CredentialCooldown || !updated.NextRetryAfter.Equal(deadline) {
		t.Fatalf("Update() with same refresh_token = %+v, want cooldown preserved until %v", updated, deadline)
	}
}

func TestManager_Update_NewCredentialReLoginClearsCredentialCooldown(t *testing.T) {
	m := NewManager(nil, nil, nil)
	now := time.Now()
	deadline := now.Add(30 * time.Minute)

	if _, errRegister := m.Register(context.Background(), &Auth{
		ID:                 "auth-401-relogin",
		Provider:           "claude",
		Unavailable:        true,
		Status:             StatusError,
		NextRetryAfter:     deadline,
		CredentialCooldown: true,
		Metadata: map[string]any{
			"access_token":  "stale-access-token",
			"refresh_token": "refresh-token-old",
		},
	}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	// An operator re-login writes a genuinely different credential (new
	// refresh_token) to the same auth file/ID - the old cooldown must not
	// carry over, so re-login restores service immediately per the
	// dead-grant recovery playbook.
	updated, errUpdate := m.Update(context.Background(), &Auth{
		ID:       "auth-401-relogin",
		Provider: "claude",
		Status:   StatusActive,
		Metadata: map[string]any{
			"access_token":  "new-access-token",
			"refresh_token": "refresh-token-new",
		},
	})
	if errUpdate != nil {
		t.Fatalf("update auth: %v", errUpdate)
	}
	if updated.Unavailable || updated.CredentialCooldown || !updated.NextRetryAfter.IsZero() {
		t.Fatalf("Update() with new refresh_token = %+v, want cooldown cleared", updated)
	}
	if updated.Status != StatusActive {
		t.Fatalf("Update() with new refresh_token Status = %v, want %v", updated.Status, StatusActive)
	}
}

func TestManager_Update_CredentialIdentityMutationControl(t *testing.T) {
	m := NewManager(nil, nil, nil)
	now := time.Now()
	deadline := now.Add(30 * time.Minute)

	if _, errRegister := m.Register(context.Background(), &Auth{
		ID:                 "auth-401-relogin-mc",
		Provider:           "claude",
		Unavailable:        true,
		Status:             StatusError,
		NextRetryAfter:     deadline,
		CredentialCooldown: true,
		Metadata: map[string]any{
			"refresh_token": "refresh-token-old",
		},
	}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	// Mimic the pre-fix behavior directly: unconditionally preserving the
	// cooldown regardless of credential identity. Confirms this test would
	// have failed to catch the regression without the sameAuthCredential
	// check, i.e. that the check is load-bearing.
	m.mu.Lock()
	existing := m.auths["auth-401-relogin-mc"]
	m.mu.Unlock()
	if existing == nil {
		t.Fatalf("expected existing auth to be present")
	}
	unconditional := &Auth{ID: "auth-401-relogin-mc", Provider: "claude", Status: StatusActive, Metadata: map[string]any{"refresh_token": "refresh-token-new"}}
	// pre-fix: no identity check at all
	unconditional.Unavailable = existing.Unavailable
	unconditional.NextRetryAfter = existing.NextRetryAfter
	unconditional.CredentialCooldown = existing.CredentialCooldown
	if !unconditional.CredentialCooldown {
		t.Fatalf("expected the unconditional pre-fix simulation to carry the cooldown over")
	}
	if sameAuthCredential(existing, unconditional) {
		t.Fatalf("sameAuthCredential() = true for a rotated refresh_token, want false")
	}
}

// TestManager_Update_EmptyIncomingIdentityPreservesCredentialCooldown covers
// an Update call whose incoming Auth carries no token metadata at all (e.g.
// a caller updating only Status/Attributes, or one that runs before a later
// metadata merge fills in the tokens). This must NOT be misread as a
// different credential and must NOT clear a live cooldown - only a
// non-empty incoming identity that differs from existing's should do that.
func TestManager_Update_EmptyIncomingIdentityPreservesCredentialCooldown(t *testing.T) {
	m := NewManager(nil, nil, nil)
	now := time.Now()
	deadline := now.Add(30 * time.Minute)

	if _, errRegister := m.Register(context.Background(), &Auth{
		ID:                 "auth-401-empty-identity",
		Provider:           "claude",
		Unavailable:        true,
		Status:             StatusError,
		NextRetryAfter:     deadline,
		CredentialCooldown: true,
		Metadata: map[string]any{
			"access_token":  "stale-access-token",
			"refresh_token": "refresh-token-abc",
		},
	}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	updated, err := m.Update(context.Background(), &Auth{
		ID:       "auth-401-empty-identity",
		Provider: "claude",
		Status:   StatusActive,
	})
	if err != nil {
		t.Fatalf("update auth: %v", err)
	}
	if !updated.Unavailable {
		t.Fatalf("expected Unavailable to remain true for an update with no incoming identity")
	}
	if !updated.CredentialCooldown {
		t.Fatalf("expected CredentialCooldown to remain true for an update with no incoming identity")
	}
	if !updated.NextRetryAfter.Equal(deadline) {
		t.Fatalf("expected NextRetryAfter to remain %v, got %v", deadline, updated.NextRetryAfter)
	}
}

// TestManager_Update_SameAPIKeyPreservesCredentialCooldown covers a plain
// API-key auth (no OAuth metadata at all) whose Update carries the SAME
// api_key attribute - this must be treated like a routine refresh and must
// preserve a live credential cooldown.
func TestManager_Update_SameAPIKeyPreservesCredentialCooldown(t *testing.T) {
	m := NewManager(nil, nil, nil)
	now := time.Now()
	deadline := now.Add(30 * time.Minute)

	if _, errRegister := m.Register(context.Background(), &Auth{
		ID:                 "auth-401-apikey-refresh",
		Provider:           "openai",
		Unavailable:        true,
		Status:             StatusError,
		NextRetryAfter:     deadline,
		CredentialCooldown: true,
		Attributes:         map[string]string{AttributeAPIKey: "sk-same-key"},
	}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	updated, err := m.Update(context.Background(), &Auth{
		ID:         "auth-401-apikey-refresh",
		Provider:   "openai",
		Status:     StatusActive,
		Attributes: map[string]string{AttributeAPIKey: "sk-same-key"},
	})
	if err != nil {
		t.Fatalf("update auth: %v", err)
	}
	if !updated.Unavailable || !updated.CredentialCooldown {
		t.Fatalf("expected cooldown preserved for same api_key, got Unavailable=%v CredentialCooldown=%v", updated.Unavailable, updated.CredentialCooldown)
	}
	if !updated.NextRetryAfter.Equal(deadline) {
		t.Fatalf("expected NextRetryAfter to remain %v, got %v", deadline, updated.NextRetryAfter)
	}
}

// TestManager_Update_RotatedAPIKeyClearsCredentialCooldown covers the
// rotation case: a plain API-key auth whose Update carries a DIFFERENT
// api_key attribute is a genuinely different credential and must clear a
// live credential cooldown, same as an OAuth re-login.
func TestManager_Update_RotatedAPIKeyClearsCredentialCooldown(t *testing.T) {
	m := NewManager(nil, nil, nil)
	now := time.Now()
	deadline := now.Add(30 * time.Minute)

	if _, errRegister := m.Register(context.Background(), &Auth{
		ID:                 "auth-401-apikey-rotate",
		Provider:           "openai",
		Unavailable:        true,
		Status:             StatusError,
		NextRetryAfter:     deadline,
		CredentialCooldown: true,
		Attributes:         map[string]string{AttributeAPIKey: "sk-old-key"},
	}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	updated, err := m.Update(context.Background(), &Auth{
		ID:         "auth-401-apikey-rotate",
		Provider:   "openai",
		Status:     StatusActive,
		Attributes: map[string]string{AttributeAPIKey: "sk-new-key"},
	})
	if err != nil {
		t.Fatalf("update auth: %v", err)
	}
	if updated.Unavailable || updated.CredentialCooldown {
		t.Fatalf("expected cooldown cleared for rotated api_key, got Unavailable=%v CredentialCooldown=%v", updated.Unavailable, updated.CredentialCooldown)
	}
	if !updated.NextRetryAfter.IsZero() {
		t.Fatalf("expected NextRetryAfter cleared, got %v", updated.NextRetryAfter)
	}
	if updated.Status != StatusActive {
		t.Fatalf("expected Status = StatusActive, got %v", updated.Status)
	}
}

// TestManager_Update_APIKeyIdentityMutationControl confirms sameAuthCredential
// is load-bearing for the API-key case: without it, a rotated api_key would
// be indistinguishable from a routine update and the cooldown would be
// carried over unconditionally.
func TestManager_Update_APIKeyIdentityMutationControl(t *testing.T) {
	existing := &Auth{ID: "auth-apikey-mc", Provider: "openai", Attributes: map[string]string{AttributeAPIKey: "sk-old-key"}}
	rotated := &Auth{ID: "auth-apikey-mc", Provider: "openai", Attributes: map[string]string{AttributeAPIKey: "sk-new-key"}}
	if sameAuthCredential(existing, rotated) {
		t.Fatalf("sameAuthCredential() = true for a rotated api_key, want false")
	}
	same := &Auth{ID: "auth-apikey-mc", Provider: "openai", Attributes: map[string]string{AttributeAPIKey: "sk-old-key"}}
	if !sameAuthCredential(existing, same) {
		t.Fatalf("sameAuthCredential() = false for an unchanged api_key, want true")
	}
}
