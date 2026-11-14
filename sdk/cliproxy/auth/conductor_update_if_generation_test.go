package auth

import (
	"context"
	"testing"
	"time"
)

type rotatingRefreshExecutor struct {
	schedulerProviderTestExecutor
}

func (e rotatingRefreshExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	auth.Metadata["access_token"] = "access-token-rotated"
	auth.Metadata["refresh_token"] = "refresh-token-rotated"
	auth.Metadata["expired"] = time.Now().Add(time.Hour).Format(time.RFC3339)
	return auth, nil
}

func registerRotatingRefreshAuth(t *testing.T) (*Manager, string) {
	t.Helper()
	manager := NewManager(newMemoryAuthTestStore(), &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(rotatingRefreshExecutor{schedulerProviderTestExecutor{provider: "claude"}})
	auth := &Auth{
		ID:       "update-if-generation-auth",
		Provider: "claude",
		Status:   StatusActive,
		Metadata: map[string]any{
			"access_token":  "access-token-original",
			"refresh_token": "refresh-token-original",
			"expired":       time.Now().Add(-time.Hour).Format(time.RFC3339),
		},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	return manager, auth.ID
}

// A management PATCH commits through Modify and hands the committed auth to the
// runtime sync hook, which checks it for staleness outside the mutation gate. A
// refresh that commits between that check and the replace must keep its rotated
// credentials.
func TestManager_UpdateIfGenerationCurrent_KeepsRefreshCommittedAfterCheck(t *testing.T) {
	ctx := WithSkipPersist(context.Background())
	manager, id := registerRotatingRefreshAuth(t)

	snapshot, committed, errModify := manager.Modify(context.Background(), id, 0, func(current *Auth) error {
		current.Metadata["note"] = "patched"
		return nil
	})
	if errModify != nil || !committed {
		t.Fatalf("modify: committed=%v err=%v", committed, errModify)
	}
	checked, ok := manager.GetByID(id)
	if !ok {
		t.Fatalf("auth %q not found", id)
	}

	manager.refreshAuth(context.Background(), id)
	refreshed, ok := manager.GetByID(id)
	if !ok {
		t.Fatalf("auth %q not found after refresh", id)
	}
	if got := refreshed.Metadata["refresh_token"]; got != "refresh-token-rotated" {
		t.Fatalf("refresh did not rotate: refresh_token = %v", got)
	}

	updated, didCommit, errUpdate := manager.UpdateIfGenerationCurrent(ctx, checked, snapshot)
	if errUpdate != nil || didCommit || updated != nil {
		t.Fatalf("stale snapshot committed: updated=%v committed=%v err=%v", updated, didCommit, errUpdate)
	}
	current, ok := manager.GetByID(id)
	if !ok {
		t.Fatalf("auth %q not found after conditional update", id)
	}
	if got := current.Metadata["refresh_token"]; got != "refresh-token-rotated" {
		t.Fatalf("refresh_token = %v, want refresh-token-rotated", got)
	}
	if got := current.Metadata["note"]; got != "patched" {
		t.Fatalf("note = %v, want the committed PATCH value", got)
	}
}

func TestManager_UpdateIfGenerationCurrent_ReplacesWhenUnchanged(t *testing.T) {
	ctx := context.Background()
	manager, id := registerRotatingRefreshAuth(t)
	base, ok := manager.GetByID(id)
	if !ok {
		t.Fatalf("auth %q not found", id)
	}
	next := base.Clone()
	next.Metadata["note"] = "applied"

	updated, didCommit, errUpdate := manager.UpdateIfGenerationCurrent(ctx, base, next)
	if errUpdate != nil || !didCommit || updated == nil {
		t.Fatalf("unchanged snapshot not committed: committed=%v err=%v", didCommit, errUpdate)
	}
	if updated.Generation <= base.Generation {
		t.Fatalf("generation = %d, want > %d", updated.Generation, base.Generation)
	}
	current, ok := manager.GetByID(id)
	if !ok {
		t.Fatalf("auth %q not found after update", id)
	}
	if got := current.Metadata["note"]; got != "applied" {
		t.Fatalf("note = %v, want applied", got)
	}
}

func TestManager_UpdateIfGenerationCurrent_SkipsMissingAuth(t *testing.T) {
	manager, _ := registerRotatingRefreshAuth(t)
	missing := &Auth{ID: "missing-auth"}
	updated, didCommit, errUpdate := manager.UpdateIfGenerationCurrent(context.Background(), missing, missing)
	if updated != nil || didCommit || errUpdate != nil {
		t.Fatalf("missing auth: updated=%v committed=%v err=%v", updated, didCommit, errUpdate)
	}
}
