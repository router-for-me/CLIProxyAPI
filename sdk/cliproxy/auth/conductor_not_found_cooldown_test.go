package auth

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestMarkResultNotFoundCooldownPolicy(t *testing.T) {
	previousTransient := transientErrorCooldownSeconds.Load()
	previousDisabled := quotaCooldownDisabled.Load()
	SetTransientErrorCooldownSeconds(0)
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() {
		transientErrorCooldownSeconds.Store(previousTransient)
		quotaCooldownDisabled.Store(previousDisabled)
	})

	tests := []struct {
		name            string
		err             *Error
		disableCooling  bool
		initialBackoff  int
		wantMinCooldown time.Duration
		wantMaxCooldown time.Duration
		wantBackoff     int
	}{
		{
			name:            "generic 404 starts short retry",
			err:             &Error{HTTPStatus: http.StatusNotFound, Message: "Not Found"},
			wantMinCooldown: 59 * time.Second,
			wantMaxCooldown: 61 * time.Second,
			wantBackoff:     1,
		},
		{
			name:            "repeated generic 404 escalates",
			err:             &Error{HTTPStatus: http.StatusNotFound, Message: "Not Found"},
			initialBackoff:  1,
			wantMinCooldown: 119 * time.Second,
			wantMaxCooldown: 121 * time.Second,
			wantBackoff:     2,
		},
		{
			name:            "explicit model not found stays long",
			err:             &Error{Code: "model_not_found", HTTPStatus: http.StatusNotFound, Message: "model unavailable"},
			wantMinCooldown: 12*time.Hour - time.Second,
			wantMaxCooldown: 12*time.Hour + time.Second,
		},
		{
			name:           "disable cooling leaves retry unset",
			err:            &Error{HTTPStatus: http.StatusNotFound, Message: "Not Found"},
			disableCooling: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			quotaCooldownDisabled.Store(tc.disableCooling)
			manager := NewManager(nil, nil, nil)
			auth := &Auth{
				ID:       "model-not-found-policy",
				Provider: "codex",
				ModelStates: map[string]*ModelState{
					"gpt-5": {Quota: QuotaState{BackoffLevel: tc.initialBackoff, NextRecoverAt: time.Now().Add(-time.Second)}},
				},
			}
			if _, err := manager.Register(WithSkipPersist(context.Background()), auth); err != nil {
				t.Fatalf("Register() error = %v", err)
			}
			before := time.Now()
			manager.MarkResult(context.Background(), Result{AuthID: auth.ID, Provider: "codex", Model: "gpt-5", Error: tc.err})
			updated, ok := manager.GetByID(auth.ID)
			if !ok || updated.ModelStates["gpt-5"] == nil {
				t.Fatal("MarkResult() did not retain model cooldown state")
			}
			state := updated.ModelStates["gpt-5"]
			if tc.wantMinCooldown == 0 {
				if !state.NextRetryAfter.IsZero() {
					t.Fatalf("NextRetryAfter = %v, want zero", state.NextRetryAfter)
				}
				return
			}
			cooldown := state.NextRetryAfter.Sub(before)
			if cooldown < tc.wantMinCooldown || cooldown > tc.wantMaxCooldown {
				t.Fatalf("cooldown = %v, want within [%v, %v]", cooldown, tc.wantMinCooldown, tc.wantMaxCooldown)
			}
			if state.Quota.BackoffLevel != tc.wantBackoff {
				t.Fatalf("BackoffLevel = %d, want %d", state.Quota.BackoffLevel, tc.wantBackoff)
			}
			if tc.initialBackoff > 0 {
				manager.MarkResult(context.Background(), Result{AuthID: auth.ID, Provider: "codex", Model: "gpt-5", Success: true})
				reset, _ := manager.GetByID(auth.ID)
				if got := reset.ModelStates["gpt-5"].Quota.BackoffLevel; got != 0 {
					t.Fatalf("successful result left BackoffLevel = %d, want 0", got)
				}
			}
		})
	}
}

func TestApplyAuthFailureStateNotFoundCooldownPolicy(t *testing.T) {
	previousTransient := transientErrorCooldownSeconds.Load()
	SetTransientErrorCooldownSeconds(0)
	t.Cleanup(func() { transientErrorCooldownSeconds.Store(previousTransient) })

	now := time.Date(2026, 9, 3, 14, 42, 0, 0, time.UTC)
	generic := &Error{HTTPStatus: http.StatusNotFound, Message: "Not Found"}
	auth := &Auth{ID: "auth-not-found-policy"}
	applyAuthFailureState(auth, generic, nil, "", now, false)
	if want := now.Add(time.Minute); !auth.NextRetryAfter.Equal(want) {
		t.Fatalf("first generic 404 NextRetryAfter = %v, want %v", auth.NextRetryAfter, want)
	}
	if auth.Quota.BackoffLevel != 1 {
		t.Fatalf("first generic 404 BackoffLevel = %d, want 1", auth.Quota.BackoffLevel)
	}

	auth.Quota.NextRecoverAt = now.Add(-time.Second)
	applyAuthFailureState(auth, generic, nil, "", now, false)
	if want := now.Add(2 * time.Minute); !auth.NextRetryAfter.Equal(want) {
		t.Fatalf("repeated generic 404 NextRetryAfter = %v, want %v", auth.NextRetryAfter, want)
	}
	if auth.Quota.BackoffLevel != 2 {
		t.Fatalf("repeated generic 404 BackoffLevel = %d, want 2", auth.Quota.BackoffLevel)
	}
	clearAuthStateOnSuccess(auth, now)
	if auth.Quota.BackoffLevel != 0 || !auth.Quota.NextRecoverAt.IsZero() {
		t.Fatalf("success left auth retry state = %#v, want reset", auth.Quota)
	}

	explicit := &Auth{ID: "auth-explicit-model-not-found"}
	applyAuthFailureState(explicit, &Error{Code: "model_not_found", HTTPStatus: http.StatusNotFound}, nil, "", now, false)
	if want := now.Add(12 * time.Hour); !explicit.NextRetryAfter.Equal(want) {
		t.Fatalf("explicit model-not-found NextRetryAfter = %v, want %v", explicit.NextRetryAfter, want)
	}

	disabled := &Auth{ID: "auth-not-found-disabled"}
	applyAuthFailureState(disabled, generic, nil, "", now, true)
	if !disabled.NextRetryAfter.IsZero() {
		t.Fatalf("disabled cooling NextRetryAfter = %v, want zero", disabled.NextRetryAfter)
	}
}

func TestMarkResultAliasUsesAttemptedUpstreamModelForExplicitNotFound(t *testing.T) {
	previousDisabled := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previousDisabled) })

	manager := NewManager(nil, nil, nil)
	auth := &Auth{ID: "alias-explicit-model-not-found", Provider: "openai-compatibility"}
	if _, err := manager.Register(WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	before := time.Now()
	manager.MarkResult(context.Background(), Result{
		AuthID:        auth.ID,
		Provider:      auth.Provider,
		Model:         "public-alias",
		UpstreamModel: "provider-model",
		RouteModel:    "public-alias",
		Error: &Error{
			HTTPStatus: http.StatusNotFound,
			Message:    `{"error":{"type":"not_found_error","message":"model provider-model was not found"}}`,
		},
	})
	updated, ok := manager.GetByID(auth.ID)
	if !ok || updated.ModelStates["public-alias"] == nil {
		t.Fatal("MarkResult() did not retain alias-keyed model state")
	}
	cooldown := updated.ModelStates["public-alias"].NextRetryAfter.Sub(before)
	if cooldown < 12*time.Hour-time.Second || cooldown > 12*time.Hour+time.Second {
		t.Fatalf("alias explicit model-not-found cooldown = %v, want about 12h", cooldown)
	}
}

func TestApplyAuthFailureStateUsesAttemptedUpstreamModelForExplicitNotFound(t *testing.T) {
	now := time.Date(2026, 9, 3, 14, 42, 0, 0, time.UTC)
	auth := &Auth{ID: "auth-alias-explicit-model-not-found"}
	err := &Error{
		HTTPStatus: http.StatusNotFound,
		Message:    `{"error":{"type":"not_found_error","message":"model provider-model was not found"}}`,
	}
	applyAuthFailureState(auth, err, nil, "provider-model", now, false)
	if want := now.Add(12 * time.Hour); !auth.NextRetryAfter.Equal(want) {
		t.Fatalf("alias explicit model-not-found NextRetryAfter = %v, want %v", auth.NextRetryAfter, want)
	}
}
