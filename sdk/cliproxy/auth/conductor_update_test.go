package auth

import (
	"context"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
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

func TestManager_Remove_DropsStaleModelStatesForRecreatedAuth(t *testing.T) {
	m := NewManager(nil, nil, nil)
	model := "test-model"

	if _, errRegister := m.Register(context.Background(), &Auth{
		ID:       "auth-1",
		Provider: "codex",
		Metadata: map[string]any{"type": "codex"},
		ModelStates: map[string]*ModelState{
			model: {
				Status:         StatusError,
				Unavailable:    true,
				NextRetryAfter: time.Now().Add(30 * time.Minute),
			},
		},
	}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	if errRemove := m.Remove(context.Background(), "auth-1"); errRemove != nil {
		t.Fatalf("remove auth: %v", errRemove)
	}
	if _, ok := m.GetByID("auth-1"); ok {
		t.Fatal("expected auth to be removed")
	}

	if _, errRegister := m.Register(context.Background(), &Auth{
		ID:       "auth-1",
		Provider: "codex",
		Metadata: map[string]any{"type": "codex"},
	}); errRegister != nil {
		t.Fatalf("re-register auth: %v", errRegister)
	}

	updated, ok := m.GetByID("auth-1")
	if !ok || updated == nil {
		t.Fatalf("expected recreated auth to be present")
	}
	if len(updated.ModelStates) != 0 {
		t.Fatalf("expected stale ModelStates to be cleared, got %#v", updated.ModelStates)
	}

	available, errAvailable := getAvailableAuths([]*Auth{updated}, "codex", model, time.Now())
	if errAvailable != nil {
		t.Fatalf("getAvailableAuths returned error: %v", errAvailable)
	}
	if len(available) != 1 || available[0] == nil || available[0].ID != "auth-1" {
		t.Fatalf("expected recreated auth to be selectable, got %#v", available)
	}
}

func TestManager_Remove_PreviouslySelectedAuthIsNoLongerPickable(t *testing.T) {
	m := NewManager(nil, nil, nil)
	executor := &replaceAwareExecutor{id: "codex"}
	m.RegisterExecutor(executor)

	reg := registry.GetGlobalRegistry()
	auth1 := &Auth{ID: "auth-1", Provider: "codex"}
	auth2 := &Auth{ID: "auth-2", Provider: "codex"}
	reg.RegisterClient(auth1.ID, auth1.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	reg.RegisterClient(auth2.ID, auth2.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		reg.UnregisterClient(auth1.ID)
		reg.UnregisterClient(auth2.ID)
	})

	if _, errRegister := m.Register(context.Background(), auth1); errRegister != nil {
		t.Fatalf("register auth1: %v", errRegister)
	}
	if _, errRegister := m.Register(context.Background(), auth2); errRegister != nil {
		t.Fatalf("register auth2: %v", errRegister)
	}

	selected, _, errPick := m.pickNext(context.Background(), "codex", "test-model", cliproxyexecutor.Options{}, map[string]struct{}{})
	if errPick != nil {
		t.Fatalf("pickNext before remove: %v", errPick)
	}
	if selected == nil || selected.ID != "auth-1" {
		t.Fatalf("expected first pick to select auth-1, got %#v", selected)
	}

	if errRemove := m.Remove(context.Background(), "auth-1"); errRemove != nil {
		t.Fatalf("remove auth-1: %v", errRemove)
	}

	selected, _, errPick = m.pickNext(context.Background(), "codex", "test-model", cliproxyexecutor.Options{}, map[string]struct{}{})
	if errPick != nil {
		t.Fatalf("pickNext after remove: %v", errPick)
	}
	if selected == nil || selected.ID != "auth-2" {
		t.Fatalf("expected removed auth to stay unpickable, got %#v", selected)
	}
}
