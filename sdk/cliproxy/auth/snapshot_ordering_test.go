package auth

import (
	"context"
	"testing"
	"time"
)

// slowPushStore writes its local file, marks the write, then does slower remote
// work, like the git, object and Postgres stores.
type slowPushStore struct {
	afterLocalWrite func()
}

func (slowPushStore) List(context.Context) ([]*Auth, error) { return nil, nil }

func (s slowPushStore) Save(ctx context.Context, auth *Auth) (string, error) {
	MarkAuthFileWritten(ctx)
	if s.afterLocalWrite != nil {
		s.afterLocalWrite()
	}
	return auth.ID, nil
}

func (slowPushStore) Delete(context.Context, string) error { return nil }

// An external edit observed after the store's local write is newer than the save,
// even when the store is still pushing; it must not be dropped as stale.
func TestUpdateFromSnapshotAppliesEditObservedAfterLocalWrite(t *testing.T) {
	ctx := context.Background()
	var observedAt time.Time
	manager := NewManager(slowPushStore{afterLocalWrite: func() {
		// Observe strictly after the marked write, and finish Save strictly after
		// the observation, whatever the clock resolution.
		observedAt = instantAfter(time.Now())
		instantAfter(observedAt)
	}}, nil, nil)

	registered, err := manager.Register(ctx, &Auth{
		ID:       "snapshot-after-local-write.json",
		Provider: "codex",
		Prefix:   "pfx-1",
		Metadata: map[string]any{"type": "codex", "prefix": "pfx-1"},
	})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	observedAt = time.Time{}

	saved := registered.Clone()
	saved.Prefix = "pfx-2"
	saved.Metadata = map[string]any{"type": "codex", "prefix": "pfx-2"}
	if _, err = manager.Update(ctx, saved); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	if observedAt.IsZero() {
		t.Fatal("store did not run its post-write step")
	}

	edited := registered.Clone()
	edited.Generation = 0
	edited.Prefix = "pfx-external"
	edited.Metadata = map[string]any{"type": "codex", "prefix": "pfx-external"}
	edited.UpdatedAt = observedAt

	current, applied, err := manager.UpdateFromSnapshot(ctx, edited)
	if err != nil {
		t.Fatalf("UpdateFromSnapshot() error = %v", err)
	}
	if !applied || current == nil || current.Prefix != "pfx-external" {
		t.Fatalf("UpdateFromSnapshot() applied=%v prefix=%q, want the external edit applied", applied, current.Prefix)
	}
}

// Without the mark the save end stays the ordering point, so a snapshot observed
// during Save is still dropped.
func TestUpdateFromSnapshotWithoutMarkUsesSaveEnd(t *testing.T) {
	ctx := context.Background()
	var observedAt time.Time
	store := &unmarkedStore{afterWrite: func() { observedAt = time.Now() }}
	manager := NewManager(store, nil, nil)

	registered, err := manager.Register(ctx, &Auth{
		ID:       "snapshot-unmarked.json",
		Provider: "codex",
		Prefix:   "pfx-1",
		Metadata: map[string]any{"type": "codex", "prefix": "pfx-1"},
	})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	saved := registered.Clone()
	saved.Prefix = "pfx-2"
	saved.Metadata = map[string]any{"type": "codex", "prefix": "pfx-2"}
	if _, err = manager.Update(ctx, saved); err != nil {
		t.Fatalf("Update() error = %v", err)
	}

	stale := registered.Clone()
	stale.Generation = 0
	stale.UpdatedAt = observedAt
	if _, applied, errSnapshot := manager.UpdateFromSnapshot(ctx, stale); errSnapshot != nil || applied {
		t.Fatalf("UpdateFromSnapshot() applied=%v err=%v, want the snapshot dropped", applied, errSnapshot)
	}
}

// instantAfter spins until the clock reads later than t and returns that reading.
func instantAfter(t time.Time) time.Time {
	for {
		if now := time.Now(); now.After(t) {
			return now
		}
	}
}

type unmarkedStore struct {
	afterWrite func()
}

func (*unmarkedStore) List(context.Context) ([]*Auth, error) { return nil, nil }

func (s *unmarkedStore) Save(_ context.Context, auth *Auth) (string, error) {
	if s.afterWrite != nil {
		s.afterWrite()
	}
	return auth.ID, nil
}

func (*unmarkedStore) Delete(context.Context, string) error { return nil }

// RegisterIfAbsent registers a missing auth, but leaves a registered one alone so
// an unversioned snapshot cannot replace it or bump its registration epoch.
func TestRegisterIfAbsentLeavesRegisteredAuthUntouched(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, nil, nil)

	first, registered, err := manager.RegisterIfAbsent(ctx, &Auth{ID: "absent.json", Provider: "codex", Prefix: "pfx-first"})
	if err != nil || !registered || first == nil {
		t.Fatalf("RegisterIfAbsent(absent) = %v, %v, %v; want it registered", first, registered, err)
	}

	again, registered, err := manager.RegisterIfAbsent(ctx, &Auth{ID: "absent.json", Provider: "codex", Prefix: "pfx-snapshot"})
	if err != nil || registered || again != nil {
		t.Fatalf("RegisterIfAbsent(present) = %v, %v, %v; want nothing registered", again, registered, err)
	}
	current, ok := manager.GetByID("absent.json")
	if !ok || current.Prefix != "pfx-first" || current.RegistrationEpoch != first.RegistrationEpoch {
		t.Fatalf("registered auth = %+v, want pfx-first at epoch %d unchanged", current, first.RegistrationEpoch)
	}
}
