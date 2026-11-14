package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

func TestClientModelProjectionAuthForceDeadline(t *testing.T) {
	now := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name          string
		forceMinutes  int
		modelMinutes  int
		disabled      bool
		wantSuspended bool
		wantMinutes   int
	}{
		{"unmatched_model", 5, 0, false, true, 5},
		{"shorter_model_cooldown", 5, 1, false, true, 5},
		{"longer_model_cooldown", 1, 5, false, true, 5},
		{"expired_force", -1, 0, false, false, 0},
		{"ordinary_auth_aggregate", 0, 0, false, false, 0},
		{"disabled_stays_open_ended", 5, 1, true, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth := &Auth{
				ID: "force-projection", Provider: "codex", Status: StatusActive,
				Disabled: tc.disabled, Unavailable: true, NextRetryAfter: now.Add(5 * time.Minute),
				forceCooldownSeeded: true,
				ModelStates:         map[string]*ModelState{"other-model": {Status: StatusActive}},
			}
			if tc.forceMinutes != 0 {
				auth.forceCooldownUntil = now.Add(time.Duration(tc.forceMinutes) * time.Minute)
			}
			if tc.modelMinutes != 0 {
				auth.ModelStates["model"] = &ModelState{
					Status: StatusError, Unavailable: true,
					NextRetryAfter: now.Add(time.Duration(tc.modelMinutes) * time.Minute),
				}
			}
			m := NewManager(nil, nil, nil)
			projection := m.clientModelProjectionForAuth(auth, "model", now)
			blocked, _, _ := isAuthBlockedForModel(auth, "model", now)
			if projection.Suspended != tc.wantSuspended || projection.Suspended != blocked {
				t.Fatalf("projection suspended=%t, selector blocked=%t, want %t", projection.Suspended, blocked, tc.wantSuspended)
			}
			var until time.Time
			if tc.wantMinutes != 0 {
				until = now.Add(time.Duration(tc.wantMinutes) * time.Minute)
			}
			if !projection.SuspendUntil.Equal(until) {
				t.Fatalf("suspension ends at %v, want %v", projection.SuspendUntil, until)
			}
			if !until.IsZero() && m.clientModelProjectionForAuth(auth, "model", until.Add(time.Second)).Suspended {
				t.Fatal("expired force/model cooldown did not release the projection")
			}
		})
	}
}

func TestAuthForceCooldownHidesRegisteredModelsWithExistingState(t *testing.T) {
	now := time.Now()
	m := NewManager(nil, nil, nil)
	auth := &Auth{
		ID: "force-projection-registry", Provider: "codex", Status: StatusActive,
		ModelStates: map[string]*ModelState{
			"force-projection-b": {Status: StatusError, Unavailable: true, NextRetryAfter: now.Add(time.Minute)},
		},
	}
	if _, err := m.Register(context.Background(), auth); err != nil {
		t.Fatal(err)
	}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "force-projection-a"}, {ID: "force-projection-b"}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	if !openAIListedModelSet(reg.GetAvailableModels("openai"))["force-projection-a"] {
		t.Fatal("healthy model must be listed before the forced cooldown")
	}
	retryAfter := 5 * time.Minute
	m.MarkResult(context.Background(), Result{
		AuthID: auth.ID, Provider: auth.Provider, RetryAfter: &retryAfter,
		Error: &Error{Code: ErrorCodeForceCooldown, HTTPStatus: http.StatusServiceUnavailable, Message: "forced cooldown"},
	})
	snapshot, ok := m.GetByID(auth.ID)
	if !ok || len(snapshot.ModelStates) == 0 || !effectiveAuthForceUntil(snapshot).After(now) {
		t.Fatal("fixture did not establish an auth-level force deadline alongside model states")
	}
	listed := openAIListedModelSet(reg.GetAvailableModels("openai"))
	for _, model := range []string{"force-projection-a", "force-projection-b"} {
		if listed[model] {
			t.Errorf("force-cooled model %s remains advertised", model)
		}
	}
}
