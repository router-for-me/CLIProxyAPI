package auth

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// recordingStore saves without touching the auth file, like a save that failed or
// skipped the write; tests create or omit the file themselves.
type recordingStore struct{}

func (recordingStore) List(context.Context) ([]*Auth, error) { return nil, nil }

func (recordingStore) Save(_ context.Context, auth *Auth) (string, error) { return auth.ID, nil }

func (recordingStore) Delete(context.Context, string) error { return nil }

// registerWithFile registers id with its local auth file at dir/id, creating the
// file only when exists is true. Register saves through the store.
func registerWithFile(t *testing.T, manager *Manager, dir, id string, exists bool) {
	t.Helper()
	path := filepath.Join(dir, id)
	if exists {
		if err := os.WriteFile(path, []byte(`{"type":"codex"}`), 0o600); err != nil {
			t.Fatalf("write auth file: %v", err)
		}
	}
	if _, err := manager.Register(context.Background(), &Auth{
		ID:         id,
		Provider:   "codex",
		Metadata:   map[string]any{"type": "codex"},
		Attributes: map[string]string{AttributePath: path},
	}); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
}

// A delete observed before a re-create of the account, whose file exists again,
// must not remove the re-created account.
func TestRemoveUnlessRecreatedDropsDeleteObservedBeforeRecreate(t *testing.T) {
	manager := NewManager(recordingStore{}, nil, nil)
	observedAt := time.Now().Add(-time.Hour)
	registerWithFile(t, manager, t.TempDir(), "recreated.json", true)

	if manager.RemoveUnlessRecreated(context.Background(), "recreated.json", observedAt) {
		t.Fatal("applied a delete observed before the account was re-created")
	}
	if _, ok := manager.GetByID("recreated.json"); !ok {
		t.Fatal("re-created account removed by a delete observed before it")
	}
}

// A save after the observation that left the file missing (a failed save, or one
// that wrote nothing) is no re-create: the file is gone, so the delete applies.
func TestRemoveUnlessRecreatedAppliesWhenFileStillMissing(t *testing.T) {
	manager := NewManager(recordingStore{}, nil, nil)
	observedAt := time.Now().Add(-time.Hour)
	registerWithFile(t, manager, t.TempDir(), "still-missing.json", false)

	if !manager.RemoveUnlessRecreated(context.Background(), "still-missing.json", observedAt) {
		t.Fatal("dropped a delete although the account's file is missing")
	}
	if _, ok := manager.GetByID("still-missing.json"); ok {
		t.Fatal("account with a missing file kept after its delete")
	}
}

// A delete observed after the latest save is a real removal, even while the file
// exists: the file only matters for a delete that predates a save.
func TestRemoveUnlessRecreatedAppliesDeleteObservedAfterSave(t *testing.T) {
	manager := NewManager(recordingStore{}, nil, nil)
	registerWithFile(t, manager, t.TempDir(), "removed.json", true)
	observedAt := time.Now().Add(time.Hour)

	if !manager.RemoveUnlessRecreated(context.Background(), "removed.json", observedAt) {
		t.Fatal("dropped a delete observed after the latest save")
	}
	if _, ok := manager.GetByID("removed.json"); ok {
		t.Fatal("account kept after a delete observed after its latest save")
	}
}

// The boundary is inclusive: a delete observed exactly at the save time may
// predate the write, so it is dropped; one observed a nanosecond later applies.
func TestRemoveUnlessRecreatedBoundaryAtSaveTime(t *testing.T) {
	manager := NewManager(recordingStore{}, nil, nil)
	registerWithFile(t, manager, t.TempDir(), "boundary.json", true)
	lockVal, ok := manager.persistLocks.Load("boundary.json")
	if !ok {
		t.Fatal("Register() recorded no save")
	}
	savedAt := lockVal.(*authPersistLock).lastSavedAt.Load()
	if savedAt == nil {
		t.Fatal("Register() recorded no save time")
	}

	if manager.RemoveUnlessRecreated(context.Background(), "boundary.json", *savedAt) {
		t.Fatal("applied a delete observed exactly at the save time")
	}
	if _, ok := manager.GetByID("boundary.json"); !ok {
		t.Fatal("account removed by a delete observed exactly at the save time")
	}
	if !manager.RemoveUnlessRecreated(context.Background(), "boundary.json", savedAt.Add(time.Nanosecond)) {
		t.Fatal("dropped a delete observed after the save time")
	}
	if _, ok := manager.GetByID("boundary.json"); ok {
		t.Fatal("account kept after a delete observed after the save time")
	}
}

// A zero observation time is unknown and always removes, like Remove.
func TestRemoveUnlessRecreatedZeroTimeAlwaysRemoves(t *testing.T) {
	manager := NewManager(recordingStore{}, nil, nil)
	registerWithFile(t, manager, t.TempDir(), "unknown-time.json", true)

	if !manager.RemoveUnlessRecreated(context.Background(), "unknown-time.json", time.Time{}) {
		t.Fatal("dropped a delete without an observation time")
	}
	if _, ok := manager.GetByID("unknown-time.json"); ok {
		t.Fatal("account kept after a delete without an observation time")
	}
}
