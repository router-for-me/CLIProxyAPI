package cliproxy

import (
	"context"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// unversionedSnapshotFixture registers an auth and applies a newer persisted
// write (prefix "new") followed by an older unversioned file snapshot (prefix
// "old", read from disk before the write landed), reproducing the upstream
// #6559 interleaving. It returns the versioned persisted snapshot.
func unversionedSnapshotFixture(t *testing.T, authID string) (*Service, *coreauth.Manager, *coreauth.Auth) {
	t.Helper()
	manager := coreauth.NewManager(nil, nil, nil)
	service := &Service{cfg: &config.Config{}, coreManager: manager}
	base := &coreauth.Auth{ID: authID, Provider: "codex", Status: coreauth.StatusActive, Prefix: "base"}
	if _, errRegister := manager.Register(context.Background(), base); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	current, _ := manager.GetByID(authID)
	patched := current.Clone()
	patched.Prefix = "new"
	persisted, errUpdate := manager.Update(context.Background(), patched)
	if errUpdate != nil {
		t.Fatalf("persisted update: %v", errUpdate)
	}

	fileSnapshot := &coreauth.Auth{ID: authID, Provider: "codex", Status: coreauth.StatusActive, Prefix: "old"}
	snapshotUpdate := watcher.AuthUpdate{Action: watcher.AuthUpdateActionModify, ID: authID, Auth: fileSnapshot}
	snapshotUpdate.SetRevision(5)
	service.handleAuthUpdate(context.Background(), snapshotUpdate)
	if got, _ := manager.GetByID(authID); got == nil || got.Prefix != "old" {
		t.Fatalf("fixture: expected unversioned snapshot to apply, got prefix %q", authPrefix(got))
	}
	return service, manager, persisted
}

func TestHandleAuthUpdates_PersistedUpdateSupersedesOlderUnversionedSnapshot(t *testing.T) {
	authID := "codex-unversioned-supersede"
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(authID) })
	service, manager, persisted := unversionedSnapshotFixture(t, authID)

	versioned := watcher.AuthUpdate{Action: watcher.AuthUpdateActionModify, ID: authID, Auth: persisted}
	versioned.SetRevision(6)
	service.handleAuthUpdate(context.Background(), versioned)

	if got, _ := manager.GetByID(authID); got == nil || got.Prefix != "new" {
		t.Fatalf("expected newer persisted update to replace stale file snapshot, got prefix %q", authPrefix(got))
	}
}

func TestHandleAuthUpdates_OlderVersionedUpdateStillStaleAfterUnversionedSnapshot(t *testing.T) {
	authID := "codex-unversioned-older-versioned"
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(authID) })
	service, manager, persisted := unversionedSnapshotFixture(t, authID)

	// A versioned snapshot older than the state the file snapshot replaced must
	// stay stale even when dispatched after it.
	older := persisted.Clone()
	older.Prefix = "older"
	older.Generation = persisted.Generation - 1
	update := watcher.AuthUpdate{Action: watcher.AuthUpdateActionModify, ID: authID, Auth: older}
	update.SetRevision(6)
	service.handleAuthUpdate(context.Background(), update)

	if got, _ := manager.GetByID(authID); got == nil || got.Prefix != "old" {
		t.Fatalf("expected older versioned update to be rejected, got prefix %q", authPrefix(got))
	}
}

func TestHandleAuthUpdates_UnversionedSnapshotSupersedeRequiresNoInterveningWrite(t *testing.T) {
	authID := "codex-unversioned-intervening"
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(authID) })
	service, manager, persisted := unversionedSnapshotFixture(t, authID)

	// Any manager write after the file snapshot invalidates the supersede path.
	current, _ := manager.GetByID(authID)
	newer := current.Clone()
	newer.Prefix = "newest"
	if _, errUpdate := manager.Update(context.Background(), newer); errUpdate != nil {
		t.Fatalf("intervening update: %v", errUpdate)
	}

	update := watcher.AuthUpdate{Action: watcher.AuthUpdateActionModify, ID: authID, Auth: persisted}
	update.SetRevision(6)
	service.handleAuthUpdate(context.Background(), update)

	if got, _ := manager.GetByID(authID); got == nil || got.Prefix != "newest" {
		t.Fatalf("expected intervening newer write to win, got prefix %q", authPrefix(got))
	}
}

func TestHandleAuthUpdates_UnrevisionedVersionedUpdateDoesNotSupersede(t *testing.T) {
	authID := "codex-unversioned-unrevisioned"
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(authID) })
	service, manager, persisted := unversionedSnapshotFixture(t, authID)

	// Without a watcher revision ordering it after the snapshot, the normal
	// generation check applies.
	service.handleAuthUpdate(context.Background(), watcher.AuthUpdate{Action: watcher.AuthUpdateActionModify, ID: authID, Auth: persisted})

	if got, _ := manager.GetByID(authID); got == nil || got.Prefix != "old" {
		t.Fatalf("expected unrevisioned versioned update to stay stale, got prefix %q", authPrefix(got))
	}
}

func authPrefix(auth *coreauth.Auth) string {
	if auth == nil {
		return "<nil>"
	}
	return auth.Prefix
}
