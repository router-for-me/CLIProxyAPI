package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
)

// Later failure writes must never shorten a still-live cooldown; they may only
// extend it (#5501). A deliberate zero write (disableCooling) still clears.

func newCooldownMonotonicManager(t *testing.T, models ...string) (*Manager, *Auth) {
	t.Helper()
	m := NewManager(nil, nil, nil)
	auth := &Auth{ID: "auth-monotonic-" + models[0], Provider: "claude"}
	reg := registry.GetGlobalRegistry()
	infos := make([]*registry.ModelInfo, 0, len(models))
	now := time.Now().Unix()
	for _, model := range models {
		infos = append(infos, &registry.ModelInfo{ID: model, Created: now})
	}
	reg.RegisterClient(auth.ID, auth.Provider, infos)
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	return m, auth
}

func TestManager_MarkResult_CredentialScopeKeepsLongerSiblingDeadline(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	m, auth := newCooldownMonotonicManager(t, "model-a", "model-b")

	// Long per-model deadline on model-b: a credential-wide 401 (~30m).
	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: auth.Provider, Model: "model-b",
		Success: false, Error: &Error{HTTPStatus: http.StatusUnauthorized, Message: "long 401"},
	})
	before := time.Now()
	sibling, _ := m.GetByID(auth.ID)
	bState := existingModelState(sibling, canonicalModelKey("model-b"))
	if bState == nil || bState.NextRetryAfter.Before(before.Add(25*time.Minute)) {
		t.Fatalf("precondition failed: model-b long deadline missing: %+v", bState)
	}

	// Shorter credential-scoped 429 on model-a must not shorten model-b.
	short := 5 * time.Minute
	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: auth.Provider, Model: "model-a",
		Success: false, RetryAfter: &short, CredentialScope: true,
		Error: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "credential 429"},
	})

	updated, _ := m.GetByID(auth.ID)
	bStateAfter := existingModelState(updated, canonicalModelKey("model-b"))
	if bStateAfter == nil {
		t.Fatal("model-b state missing after sibling failure")
	}
	if bStateAfter.NextRetryAfter.Before(before.Add(25 * time.Minute)) {
		t.Fatalf("credential-scoped failure shortened model-b deadline to %v", bStateAfter.NextRetryAfter.Sub(before))
	}
}

func TestManager_MarkResult_LaterShorterFailureKeepsLongerModelDeadline(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	tests := []struct {
		name   string
		second func(m *Manager, authID string)
	}{
		{
			name: "401_then_short_429",
			second: func(m *Manager, authID string) {
				short := 2 * time.Minute
				m.MarkResult(context.Background(), Result{
					AuthID: authID, Provider: "claude", Model: "model-a",
					Success: false, RetryAfter: &short,
					Error: &Error{HTTPStatus: http.StatusTooManyRequests, Message: "short 429"},
				})
			},
		},
		{
			name: "401_then_transient_500",
			second: func(m *Manager, authID string) {
				m.MarkResult(context.Background(), Result{
					AuthID: authID, Provider: "claude", Model: "model-a",
					Success: false, Error: &Error{HTTPStatus: http.StatusInternalServerError, Message: "transient 500"},
				})
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m, auth := newCooldownMonotonicManager(t, "model-a")
			m.MarkResult(context.Background(), Result{
				AuthID: auth.ID, Provider: auth.Provider, Model: "model-a",
				Success: false, Error: &Error{HTTPStatus: http.StatusUnauthorized, Message: "long 401"},
			})
			before := time.Now()
			snap, _ := m.GetByID(auth.ID)
			state := existingModelState(snap, canonicalModelKey("model-a"))
			if state == nil || state.NextRetryAfter.Before(before.Add(25*time.Minute)) {
				t.Fatalf("precondition failed: 401 deadline missing: %+v", state)
			}

			tc.second(m, auth.ID)

			updated, _ := m.GetByID(auth.ID)
			stateAfter := existingModelState(updated, canonicalModelKey("model-a"))
			if stateAfter == nil {
				t.Fatal("model state missing after second writer")
			}
			if stateAfter.NextRetryAfter.Before(before.Add(25 * time.Minute)) {
				t.Fatalf("second writer shortened live deadline to %v", stateAfter.NextRetryAfter.Sub(before))
			}
		})
	}
}

// The client-facing projection must agree with scheduling: a live credential-wide
// cooldown (auth.Unavailable + auth.NextRetryAfter) suspends models that carry no
// per-model state (#5501).
func TestManager_ClientModelProjection_ReflectsAuthLevelCooldown(t *testing.T) {
	previous := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previous) })

	m, auth := newCooldownMonotonicManager(t, "model-a")

	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: auth.Provider, Model: "",
		Success: false, Error: &Error{HTTPStatus: http.StatusUnauthorized, Message: "credential-wide 401"},
	})

	snap, _ := m.GetByID(auth.ID)
	if !snap.Unavailable || snap.NextRetryAfter.Before(time.Now().Add(25*time.Minute)) {
		t.Fatalf("precondition failed: expected live auth-level cooldown, got Unavailable=%v NextRetryAfter=%v", snap.Unavailable, snap.NextRetryAfter)
	}

	blocked, _, _ := isAuthBlockedForModel(snap, "model-a", time.Now())
	projection := m.clientModelProjectionForAuth(snap, "model-a", time.Now())
	if blocked && !projection.Suspended {
		t.Fatalf("scheduling blocks the model while projection reports Suspended=false: %+v", projection)
	}
}
