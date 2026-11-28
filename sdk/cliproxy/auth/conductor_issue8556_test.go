package auth

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type issue8556CountingHook struct {
	NoopHook
	registered atomic.Int32
}

func (h *issue8556CountingHook) OnAuthRegistered(context.Context, *Auth) {
	h.registered.Add(1)
}

func TestManager_RegisterFileSnapshotIfAbsent_DropsStaleAndDeletedSnapshot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "stale.json")
	oldData := []byte(`{"type":"codex","prefix":"old"}`)
	newData := []byte(`{"type":"codex","prefix":"new"}`)
	if errWrite := os.WriteFile(path, oldData, 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}

	hook := &issue8556CountingHook{}
	m := NewManager(nil, nil, hook)
	ctx := WithSkipPersist(context.Background())

	// Case 1: file changed on disk before registration.
	if errWrite := os.WriteFile(path, newData, 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}
	stale := &Auth{
		ID:         "stale.json",
		Provider:   "codex",
		Prefix:     "old",
		Status:     StatusActive,
		Attributes: map[string]string{AttributePath: path},
	}
	stale.BindFileSnapshot(path, oldData)

	registered, exists, errReg := m.RegisterFileSnapshotIfAbsent(ctx, stale)
	if errReg != nil || exists || registered != nil {
		t.Fatalf("RegisterFileSnapshotIfAbsent(changed) = (%+v, %v, %v), want (nil, false, nil)", registered, exists, errReg)
	}
	if _, ok := m.GetByID("stale.json"); ok {
		t.Fatal("stale snapshot must not be inserted into manager")
	}
	if !stale.HasFileSnapshot() {
		t.Fatal("discarded snapshot should retain file snapshot binding")
	}

	// Case 2: file removed from disk before registration.
	if errRemove := os.Remove(path); errRemove != nil {
		t.Fatal(errRemove)
	}
	deleted := &Auth{
		ID:         "stale.json",
		Provider:   "codex",
		Prefix:     "new",
		Status:     StatusActive,
		Attributes: map[string]string{AttributePath: path},
	}
	deleted.BindFileSnapshot(path, newData)

	registered, exists, errReg = m.RegisterFileSnapshotIfAbsent(ctx, deleted)
	if errReg != nil || exists || registered != nil {
		t.Fatalf("RegisterFileSnapshotIfAbsent(deleted) = (%+v, %v, %v), want (nil, false, nil)", registered, exists, errReg)
	}
	if _, ok := m.GetByID("stale.json"); ok {
		t.Fatal("deleted snapshot must not be inserted into manager")
	}
	if got := hook.registered.Load(); got != 0 {
		t.Fatalf("OnAuthRegistered calls = %d, want 0", got)
	}
}

func TestManager_RegisterFileSnapshotIfAbsent_ReturnsExistingWithoutBumpingEpoch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "existing.json")
	oldData := []byte(`{"type":"codex","prefix":"old"}`)
	if errWrite := os.WriteFile(path, oldData, 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}

	hook := &issue8556CountingHook{}
	m := NewManager(nil, nil, hook)
	ctx := WithSkipPersist(context.Background())

	initial, errReg := m.Register(ctx, &Auth{
		ID:       "existing.json",
		Provider: "codex",
		Prefix:   "live",
		Status:   StatusActive,
	})
	if errReg != nil {
		t.Fatal(errReg)
	}
	if hook.registered.Load() != 1 {
		t.Fatalf("initial OnAuthRegistered calls = %d, want 1", hook.registered.Load())
	}

	snapshot := &Auth{
		ID:         "existing.json",
		Provider:   "codex",
		Prefix:     "old",
		Status:     StatusActive,
		Attributes: map[string]string{AttributePath: path},
	}
	snapshot.BindFileSnapshot(path, oldData)

	got, exists, errSnap := m.RegisterFileSnapshotIfAbsent(ctx, snapshot)
	if errSnap != nil || !exists || got == nil {
		t.Fatalf("RegisterFileSnapshotIfAbsent(existing) = (%+v, %v, %v), want (existing, true, nil)", got, exists, errSnap)
	}
	if got.Prefix != "live" {
		t.Fatalf("returned existing prefix = %q, want %q", got.Prefix, "live")
	}
	if got.RegistrationEpoch != initial.RegistrationEpoch {
		t.Fatalf("RegistrationEpoch = %d, want %d", got.RegistrationEpoch, initial.RegistrationEpoch)
	}
	current, ok := m.GetByID("existing.json")
	if !ok || current.Prefix != "live" || current.RegistrationEpoch != initial.RegistrationEpoch {
		t.Fatalf("manager state mutated: %+v", current)
	}
	if hook.registered.Load() != 1 {
		t.Fatalf("OnAuthRegistered calls = %d, want 1", hook.registered.Load())
	}
}

func TestManager_RegisterFileSnapshotIfAbsent_DiskReadDoesNotHoldGlobalLock(t *testing.T) {
	dir := t.TempDir()
	slowPath := filepath.Join(dir, "slow-absent.json")
	slowData := []byte(`{"type":"codex","prefix":"pfx-slow"}`)
	if errWrite := os.WriteFile(slowPath, slowData, 0o600); errWrite != nil {
		t.Fatal(errWrite)
	}

	ctx := WithSkipPersist(context.Background())
	m := NewManager(nil, nil, nil)
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
		ID:         "slow-absent.json",
		Provider:   "codex",
		Prefix:     "pfx-slow",
		Status:     StatusActive,
		Attributes: map[string]string{AttributePath: slowPath},
	}
	slowSnapshot.BindFileSnapshot(slowPath, slowData)

	go func() {
		_, _, errReg := m.RegisterFileSnapshotIfAbsent(ctx, slowSnapshot)
		slowDone <- errReg
	}()
	select {
	case <-readStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for slow snapshot read to start")
	}

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
			t.Fatalf("slow RegisterFileSnapshotIfAbsent failed: %v", errSlow)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for slow RegisterFileSnapshotIfAbsent to complete")
	}
	slowAfter, ok := m.GetByID("slow-absent.json")
	if !ok || slowAfter == nil || slowAfter.Prefix != "pfx-slow" {
		t.Fatalf("slow-absent auth = %+v (ok=%v), want prefix pfx-slow", slowAfter, ok)
	}
}
