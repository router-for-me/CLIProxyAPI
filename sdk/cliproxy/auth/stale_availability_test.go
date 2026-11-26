package auth

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestClearStaleUnavailableState_ClearsAuthWithoutDeadline(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	auth := &Auth{
		ID:          "stale-auth",
		Provider:    "test",
		Status:      StatusActive,
		Unavailable: true,
		Generation:  7,
		UpdatedAt:   now.Add(-time.Hour),
	}
	if !clearStaleUnavailableState(auth, now) {
		t.Fatal("clearStaleUnavailableState() = false, want true for stale auth")
	}
	if auth.Unavailable {
		t.Fatal("Unavailable was not cleared")
	}
	if !auth.NextRetryAfter.IsZero() {
		t.Fatalf("NextRetryAfter = %v, want zero", auth.NextRetryAfter)
	}
	if auth.Generation != 8 {
		t.Fatalf("Generation = %d, want 8", auth.Generation)
	}
	if !auth.UpdatedAt.Equal(now) {
		t.Fatalf("UpdatedAt = %v, want %v", auth.UpdatedAt, now)
	}
	if !IsAuthAvailableForModel(auth, "", now) {
		t.Fatal("IsAuthAvailableForModel() = false after clearing stale unavailable state")
	}
}

func TestClearStaleUnavailableState_PreservesRealBlocks(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)

	tests := []struct {
		name   string
		mutate func(*Auth)
	}{
		{
			name: "future NextRetryAfter",
			mutate: func(a *Auth) {
				a.NextRetryAfter = future
			},
		},
		{
			name: "future NextRefreshAfter",
			mutate: func(a *Auth) {
				a.NextRefreshAfter = future
			},
		},
		{
			name: "future credential quota",
			mutate: func(a *Auth) {
				a.Quota = QuotaState{Exceeded: true, Reason: "credential_quota", NextRecoverAt: future}
			},
		},
		{
			name: "credential quota without recovery deadline",
			mutate: func(a *Auth) {
				a.Quota = QuotaState{Exceeded: true, Reason: "credential_quota"}
			},
		},
		{
			name: "future credential retry",
			mutate: func(a *Auth) {
				a.credentialRetryAfter = future
			},
		},
		{
			name: "future non-auth credential retry",
			mutate: func(a *Auth) {
				a.nonAuthCredentialRetryAfter = future
			},
		},
		{
			name: "future force cooldown",
			mutate: func(a *Auth) {
				a.forceCooldownSeeded = true
				a.forceCooldownUntil = future
			},
		},
		{
			name: "disabled",
			mutate: func(a *Auth) {
				a.Disabled = true
			},
		},
		{
			name: "status disabled",
			mutate: func(a *Auth) {
				a.Status = StatusDisabled
			},
		},
		{
			name: "unauthorized auth failure",
			mutate: func(a *Auth) {
				a.Status = StatusError
				a.LastError = &Error{HTTPStatus: http.StatusUnauthorized, Code: "unauthorized", Message: "unauthorized"}
			},
		},
		{
			name: "requires sign in with invalid token",
			mutate: func(a *Auth) {
				a.RequiresSignIn = true
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			auth := &Auth{
				ID:          "blocked-auth",
				Provider:    "test",
				Status:      StatusActive,
				Unavailable: true,
				Generation:  4,
			}
			tc.mutate(auth)
			if clearStaleUnavailableState(auth, now) {
				t.Fatal("clearStaleUnavailableState() = true, want false for preserved block")
			}
			if !auth.Unavailable {
				t.Fatal("Unavailable was cleared for a real block")
			}
			if auth.Generation != 4 {
				t.Fatalf("Generation = %d, want 4", auth.Generation)
			}
		})
	}
}

func TestClearStaleUnavailableState_ClearsStaleModelStates(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)
	auth := &Auth{
		ID:          "model-auth",
		Provider:    "test",
		Status:      StatusActive,
		Unavailable: true,
		ModelStates: map[string]*ModelState{
			"stale":    {Status: StatusActive, Unavailable: true},
			"future":   {Status: StatusActive, Unavailable: true, NextRetryAfter: future},
			"disabled": {Status: StatusDisabled, Unavailable: true},
		},
	}
	if !clearStaleUnavailableState(auth, now) {
		t.Fatal("clearStaleUnavailableState() = false, want true")
	}
	if auth.Unavailable {
		t.Fatal("auth Unavailable was not cleared")
	}
	stale := auth.ModelStates["stale"]
	if stale.Unavailable || !stale.NextRetryAfter.IsZero() || stale.Status != StatusActive {
		t.Fatalf("stale model state not reset: %+v", stale)
	}
	blocked := auth.ModelStates["future"]
	if !blocked.Unavailable || !blocked.NextRetryAfter.Equal(future) {
		t.Fatalf("model state with future deadline was modified: %+v", blocked)
	}
	disabled := auth.ModelStates["disabled"]
	if !disabled.Unavailable || disabled.Status != StatusDisabled {
		t.Fatalf("disabled model state was modified: %+v", disabled)
	}
	if !IsAuthAvailableForModel(auth, "", now) {
		t.Fatal("IsAuthAvailableForModel() = false after clearing stale model states")
	}
}

func TestClearStaleUnavailableState_PreservesModelStates(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	future := now.Add(time.Hour)
	tests := []struct {
		name   string
		mutate func(*ModelState)
	}{
		{
			name: "unauthorized model error",
			mutate: func(s *ModelState) {
				s.LastError = &Error{HTTPStatus: http.StatusUnauthorized, Code: "unauthorized", Message: "unauthorized"}
			},
		},
		{
			name: "future force cooldown",
			mutate: func(s *ModelState) {
				s.forceCooldownSeeded = true
				s.forceCooldownUntil = future
			},
		},
		{
			name: "active credential quota shadow",
			mutate: func(s *ModelState) {
				s.credentialQuotaShadow = &ModelState{Status: StatusActive, Unavailable: true, NextRetryAfter: future}
			},
		},
		{
			name: "quota exceeded without recovery deadline",
			mutate: func(s *ModelState) {
				s.Quota = QuotaState{Exceeded: true, Reason: "quota"}
			},
		},
		{
			name: "future NextRetryAfter",
			mutate: func(s *ModelState) {
				s.NextRetryAfter = future
			},
		},
		{
			name: "disabled model",
			mutate: func(s *ModelState) {
				s.Status = StatusDisabled
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			state := &ModelState{Status: StatusActive, Unavailable: true}
			tc.mutate(state)
			before := state.cloneShallow()
			beforeShadow := state.credentialQuotaShadow
			auth := &Auth{
				ID:          "protected-model-auth",
				Provider:    "test",
				Status:      StatusActive,
				Unavailable: true,
				ModelStates: map[string]*ModelState{"model": state},
			}
			_ = clearStaleUnavailableState(auth, now)
			if !cooldownIndependentStateEqual(before, state) {
				t.Fatalf("protected model state was reset: before=%+v after=%+v", before, state)
			}
			if state.credentialQuotaShadow != beforeShadow {
				t.Fatal("active credential quota shadow was replaced")
			}
		})
	}
}

func TestClearStaleUnavailableState_ClearsResidualQuotaWithoutModelStates(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	auth := &Auth{
		ID:          "residual-quota-auth",
		Provider:    "test",
		Status:      StatusActive,
		Unavailable: true,
		Quota:       QuotaState{NextRecoverAt: now.Add(time.Hour)},
	}
	if !clearStaleUnavailableState(auth, now) {
		t.Fatal("clearStaleUnavailableState() = false, want true")
	}
	if auth.Quota.Exceeded || !auth.Quota.NextRecoverAt.IsZero() {
		t.Fatalf("residual quota deadline was not cleared: %+v", auth.Quota)
	}
}

func TestClearStaleUnavailableState_NoChurnWhenAggregateReFlags(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	auth := &Auth{
		ID:          "no-churn-auth",
		Provider:    "test",
		Status:      StatusActive,
		Unavailable: true,
		Generation:  9,
		ModelStates: map[string]*ModelState{
			"disabled": {Status: StatusDisabled, Unavailable: true},
		},
	}
	if clearStaleUnavailableState(auth, now) {
		t.Fatal("clearStaleUnavailableState() = true without any net change")
	}
	if !auth.Unavailable {
		t.Fatal("live disabled model state must keep the auth unavailable")
	}
	if auth.Generation != 9 {
		t.Fatalf("Generation = %d, want 9", auth.Generation)
	}
}

func TestManagerModify_ReenableClearsStaleUnavailable(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()
	now := time.Now()
	const authID = "reenable-stale-unavailable"
	if _, errRegister := manager.Register(ctx, &Auth{
		ID:          authID,
		Provider:    "test",
		Status:      StatusDisabled,
		Disabled:    true,
		Unavailable: true,
	}); errRegister != nil {
		t.Fatalf("Register() error = %v", errRegister)
	}
	modified, ok, errModify := manager.Modify(ctx, authID, 0, func(a *Auth) error {
		a.Disabled = false
		a.Status = StatusActive
		return nil
	})
	if errModify != nil || !ok {
		t.Fatalf("Modify() ok=%v err=%v", ok, errModify)
	}
	if modified.Unavailable {
		t.Fatal("auth remained unavailable after re-enable")
	}
	if !IsAuthAvailableForModel(modified, "", now) {
		t.Fatal("re-enabled auth is still blocked for selection")
	}
}

type staleAvailabilityStore struct {
	auths []*Auth
}

func (s *staleAvailabilityStore) List(context.Context) ([]*Auth, error) {
	return s.auths, nil
}

func (s *staleAvailabilityStore) Save(context.Context, *Auth) (string, error) {
	return "", nil
}

func (s *staleAvailabilityStore) Delete(context.Context, string) error {
	return nil
}

func TestLoad_ClearsStaleUnavailable(t *testing.T) {
	future := time.Now().Add(time.Hour)
	store := &staleAvailabilityStore{auths: []*Auth{
		{ID: "stale-load", Provider: "test", Status: StatusActive, Unavailable: true},
		{ID: "blocked-load", Provider: "test", Status: StatusActive, Unavailable: true, NextRetryAfter: future},
	}}
	manager := NewManager(store, nil, nil)
	if errLoad := manager.Load(context.Background()); errLoad != nil {
		t.Fatalf("Load() error = %v", errLoad)
	}
	stale, ok := manager.GetByID("stale-load")
	if !ok {
		t.Fatal("stale auth was not loaded")
	}
	if stale.Unavailable {
		t.Fatal("Load() did not clear stale unavailable state")
	}
	blocked, ok := manager.GetByID("blocked-load")
	if !ok {
		t.Fatal("blocked auth was not loaded")
	}
	if !blocked.Unavailable {
		t.Fatal("Load() cleared a real future cooldown")
	}
}
