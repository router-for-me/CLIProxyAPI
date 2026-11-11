package auth

import (
	"context"
	"os"
	"path/filepath"
	"sync"
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

func TestManager_Update_FileSnapshotReadDoesNotHoldGlobalLock(t *testing.T) {
	dir := t.TempDir()
	slowPath := filepath.Join(dir, "slow.json")
	slowData := []byte(`{"type":"codex","prefix":"pfx-slow"}`)
	if errWrite := os.WriteFile(slowPath, slowData, 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}

	ctx := WithSkipPersist(context.Background())
	m := NewManager(nil, nil, nil)
	if _, errReg := m.Register(ctx, &Auth{ID: "slow.json", Provider: "codex", Status: StatusActive}); errReg != nil {
		t.Fatal(errReg)
	}
	if _, errReg := m.Register(ctx, &Auth{ID: "other.json", Provider: "codex", Status: StatusActive}); errReg != nil {
		t.Fatal(errReg)
	}

	readStarted := make(chan struct{})
	releaseRead := make(chan struct{})
	slowDone := make(chan error, 1)
	var releaseOnce sync.Once
	releaseSlowRead := func() {
		releaseOnce.Do(func() {
			close(releaseRead)
		})
	}
	origRead := readAuthSnapshotFileFunc
	t.Cleanup(func() {
		releaseSlowRead()
		<-slowDone
		readAuthSnapshotFileFunc = origRead
	})
	var startedOnce sync.Once
	readAuthSnapshotFileFunc = func(path string) ([]byte, error) {
		if path == slowPath {
			startedOnce.Do(func() {
				close(readStarted)
			})
			<-releaseRead
		}
		return origRead(path)
	}

	slowSnapshot := &Auth{
		ID:       "slow.json",
		Provider: "codex",
		Prefix:   "pfx-slow",
		Status:   StatusActive,
		Attributes: map[string]string{
			AttributePath: slowPath,
		},
	}
	slowSnapshot.BindFileSnapshot(slowPath, slowData)

	go func() {
		_, errUpdate := m.Update(ctx, slowSnapshot)
		slowDone <- errUpdate
	}()
	select {
	case <-readStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for slow snapshot read to start")
	}

	// While slow.json is blocked reading its snapshot file, operations on other.json
	// must not be blocked on m.mu.
	otherDone := make(chan error, 1)
	go func() {
		if _, ok := m.GetByID("other.json"); !ok {
			otherDone <- os.ErrNotExist
			return
		}
		_, errUpdate := m.Update(ctx, &Auth{
			ID:       "other.json",
			Provider: "codex",
			Prefix:   "pfx-other",
			Status:   StatusActive,
		})
		otherDone <- errUpdate
	}()
	select {
	case errOther := <-otherDone:
		if errOther != nil {
			t.Fatalf("other auth update failed while slow snapshot read was in flight: %v", errOther)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("other auth update blocked while slow snapshot read was in flight")
	}

	releaseSlowRead()
	select {
	case errSlow := <-slowDone:
		slowDone <- errSlow
		if errSlow != nil {
			t.Fatalf("slow auth update failed: %v", errSlow)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for slow auth update to complete")
	}
	slowAfter, _ := m.GetByID("slow.json")
	if slowAfter == nil || slowAfter.Prefix != "pfx-slow" {
		t.Fatalf("slow auth prefix = %v, want pfx-slow", slowAfter)
	}
}
