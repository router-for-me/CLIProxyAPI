package auth

import (
	"context"
	"testing"
	"time"
)

func TestManager_Remove_DeletesRuntimeAuth(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()

	auth := &Auth{
		ID:       "remove-runtime-auth",
		Provider: "claude",
		Status:   StatusActive,
		Metadata: map[string]any{"email": "x@example.com"},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	manager.Remove(ctx, auth.ID)

	if _, ok := manager.GetByID(auth.ID); ok {
		t.Fatalf("expected auth %q to be removed", auth.ID)
	}
}

func TestManager_Update_MissingAuthIsNoOp(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	ctx := context.Background()

	auth := &Auth{
		ID:       "missing-update-auth",
		Provider: "claude",
		Status:   StatusActive,
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	manager.Remove(ctx, auth.ID)

	updated, errUpdate := manager.Update(ctx, &Auth{
		ID:       auth.ID,
		Provider: "claude",
		Status:   StatusDisabled,
		Disabled: true,
	})
	if errUpdate != nil {
		t.Fatalf("update removed auth: %v", errUpdate)
	}
	if updated != nil {
		t.Fatalf("expected update on removed auth to be no-op, got %#v", updated)
	}
	if _, ok := manager.GetByID(auth.ID); ok {
		t.Fatalf("expected removed auth to stay absent after late update")
	}
}

func TestManager_Remove_UnschedulesAutoRefresh(t *testing.T) {
	ctx := context.Background()

	manager := NewManager(nil, nil, nil)
	loop := newAuthAutoRefreshLoop(manager, time.Second, 1)
	manager.mu.Lock()
	manager.refreshLoop = loop
	manager.mu.Unlock()

	lead := 10 * time.Minute
	setRefreshLeadFactory(t, "provider-lead-expiry", func() *time.Duration {
		d := lead
		return &d
	})

	auth := &Auth{
		ID:       "remove-refresh-auth",
		Provider: "provider-lead-expiry",
		Metadata: map[string]any{
			"email":      "x@example.com",
			"expires_at": time.Now().Add(time.Hour).Format(time.RFC3339),
		},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	now := time.Now()
	if _, ok := nextRefreshCheckAt(now, auth, time.Second); !ok {
		t.Fatalf("expected auth to be scheduled before removal")
	}
	loop.applyDirty(now)
	loop.mu.Lock()
	if _, ok := loop.index[auth.ID]; !ok {
		loop.mu.Unlock()
		t.Fatalf("expected auth %q to be present in auto-refresh index before removal", auth.ID)
	}
	loop.mu.Unlock()

	manager.Remove(ctx, auth.ID)

	if _, ok := manager.GetByID(auth.ID); ok {
		t.Fatalf("expected auth to be removed")
	}
	loop.mu.Lock()
	if _, ok := loop.index[auth.ID]; ok {
		loop.mu.Unlock()
		t.Fatalf("expected auth %q to be removed from auto-refresh index", auth.ID)
	}
	loop.mu.Unlock()
}

func TestManager_Remove_PreservesSharedExecutorSessions(t *testing.T) {
	for _, provider := range []string{"codex", "xai"} {
		t.Run(provider, func(t *testing.T) {
			manager := NewManager(nil, nil, nil)
			executor := &replaceAwareExecutor{id: provider}
			manager.RegisterExecutor(executor)
			ctx := context.Background()
			for _, id := range []string{"removed-auth", "remaining-auth"} {
				if _, err := manager.Register(ctx, &Auth{ID: id, Provider: provider, Status: StatusActive}); err != nil {
					t.Fatalf("register auth: %v", err)
				}
			}
			manager.Remove(ctx, "removed-auth")
			if _, ok := manager.GetByID("removed-auth"); ok {
				t.Fatal("removed auth remains registered")
			}
			if _, ok := manager.GetByID("remaining-auth"); !ok {
				t.Fatal("unrelated auth was removed")
			}
			if closed := executor.ClosedSessionIDs(); len(closed) != 0 {
				t.Fatalf("removing one auth closed shared executor sessions: %v", closed)
			}
			if current, ok := manager.Executor(provider); !ok || current != executor {
				t.Fatal("removing one auth replaced the shared executor")
			}
		})
	}
}

type authRemovalAwareExecutor struct {
	replaceAwareExecutor
	closedAuthIDs []string
}

func (e *authRemovalAwareExecutor) CloseExecutionSessionsForAuth(authID string) {
	e.closedAuthIDs = append(e.closedAuthIDs, authID)
}

func TestManager_Remove_ClosesOnlyRemovedAuthSessions(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	executor := &authRemovalAwareExecutor{replaceAwareExecutor: replaceAwareExecutor{id: "codex"}}
	manager.RegisterExecutor(executor)
	ctx := context.Background()
	for _, id := range []string{"removed-auth", "remaining-auth"} {
		if _, err := manager.Register(ctx, &Auth{ID: id, Provider: "codex", Status: StatusActive}); err != nil {
			t.Fatalf("register auth: %v", err)
		}
	}
	manager.Remove(ctx, "removed-auth")
	manager.Remove(ctx, "removed-auth")
	manager.Remove(ctx, "unknown-auth")
	if len(executor.closedAuthIDs) != 1 || executor.closedAuthIDs[0] != "removed-auth" {
		t.Fatalf("closed auth IDs = %v, want only removed-auth", executor.closedAuthIDs)
	}
	if closed := executor.ClosedSessionIDs(); len(closed) != 0 {
		t.Fatalf("removing auth closed shared executor sessions: %v", closed)
	}
}
