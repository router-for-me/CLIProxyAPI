package auth

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
)

func TestManagerRegisterFileSnapshotFreshness(t *testing.T) {
	for _, name := range []string{"matching", "canonical_equivalent", "missing", "replaced", "unreadable", "unbound_runtime"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "snapshot.json")
			raw := []byte(`{"type":"codex","prefix":"before"}`)
			writeRegisterSnapshotFile(t, path, raw)
			snapshot := registerSnapshotAuth(path, raw)
			store, hook := &registerSnapshotStore{}, &registerSnapshotHook{}
			manager := NewManager(store, nil, hook)
			changes, unsubscribe := manager.WatchAuthChanges(snapshot.ID)
			defer unsubscribe()
			reject := name == "missing" || name == "replaced" || name == "unreadable"
			switch name {
			case "canonical_equivalent":
				equivalent := []byte("{\n  \"prefix\": \"before\", \"type\": \"codex\"\n}\n")
				if bytes.Equal(raw, equivalent) {
					t.Fatal("register snapshot fixture: equivalent bytes did not differ")
				}
				writeRegisterSnapshotFile(t, path, equivalent)
				disk, errRead := os.ReadFile(path)
				if errRead != nil || bytes.Equal(raw, disk) {
					t.Fatal("register snapshot fixture: equivalent disk bytes did not differ")
				}
			case "missing", "unbound_runtime":
				if os.Remove(path) != nil {
					t.Fatal("register snapshot fixture: remove failed")
				}
				if name == "unbound_runtime" {
					snapshot.ClearFileSnapshot()
				}
			case "replaced":
				writeRegisterSnapshotFile(t, path, []byte(`{"type":"codex","prefix":"after"}`))
			case "unreadable":
				// Use the existing read seam; chmod would depend on process privileges.
				read := readAuthSnapshotFileFunc
				readAuthSnapshotFileFunc = func(candidate string) ([]byte, error) {
					if candidate == path {
						return nil, os.ErrPermission
					}
					return read(candidate)
				}
				t.Cleanup(func() { readAuthSnapshotFileFunc = read })
			}
			registered, errRegister := manager.Register(context.Background(), snapshot)
			_, present := manager.GetByID(snapshot.ID)
			var notified bool
			select {
			case <-changes:
				notified = true
			default:
			}
			if reject {
				if errRegister == nil || errRegister.Error() != "register auth: stale file snapshot" || registered != nil || present || notified || store.saves.Load() != 0 || hook.calls.Load() != 0 || registerSnapshotEpoch(manager, snapshot.ID) != 0 || manager.structuralEpoch.Load() != 0 {
					t.Fatal("register snapshot rejection changed runtime persistence or publication")
				}
				if snapshot.Generation != 0 || snapshot.RegistrationEpoch != 0 || !snapshot.HasFileSnapshot() {
					t.Fatal("register snapshot rejection changed incoming generation")
				}
			} else if errRegister != nil || registered == nil || !present || !notified || store.saves.Load() != 1 || hook.calls.Load() != 1 || registerSnapshotEpoch(manager, snapshot.ID) != 1 || registered.HasFileSnapshot() {
				t.Fatal("register snapshot valid control did not commit exactly once")
			}
		})
	}
}

func TestManagerRegisterFileSnapshotAllowsGenuineRecreation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recreated.json")
	before := []byte(`{"type":"codex","prefix":"before"}`)
	writeRegisterSnapshotFile(t, path, before)
	store, hook := &registerSnapshotStore{}, &registerSnapshotHook{}
	manager := NewManager(store, nil, hook)
	registered, errRegister := manager.Register(context.Background(), registerSnapshotAuth(path, before))
	if errRegister != nil || registered == nil {
		t.Fatal("register snapshot fixture: initial registration failed")
	}
	held := registerSnapshotAuth(path, before)
	if os.Remove(path) != nil {
		t.Fatal("register snapshot fixture: remove failed")
	}
	manager.Remove(context.Background(), held.ID)
	tombstone := registerSnapshotEpoch(manager, held.ID)
	structuralEpoch := manager.structuralEpoch.Load()
	changes, unsubscribe := manager.WatchAuthChanges(held.ID)
	defer unsubscribe()
	if tombstone <= registered.RegistrationEpoch {
		t.Fatal("register snapshot fixture: removal epoch did not advance")
	}
	if result, errStale := manager.Register(context.Background(), held); errStale == nil || result != nil {
		t.Fatal("register snapshot accepted removed file before recreation")
	}
	_, present := manager.GetByID(held.ID)
	if present || registerSnapshotEpoch(manager, held.ID) != tombstone || manager.structuralEpoch.Load() != structuralEpoch || store.saves.Load() != 1 || hook.calls.Load() != 1 {
		t.Fatal("register snapshot rejection advanced removal state")
	}
	select {
	case <-changes:
		t.Fatal("register snapshot rejection published a structural change")
	default:
	}
	after := []byte(`{"type":"codex","prefix":"after"}`)
	writeRegisterSnapshotFile(t, path, after)
	fresh, errFresh := manager.Register(context.Background(), registerSnapshotAuth(path, after))
	if errFresh != nil || fresh == nil || fresh.Prefix != "after" || fresh.RegistrationEpoch <= tombstone || store.saves.Load() != 2 || hook.calls.Load() != 2 {
		t.Fatal("register snapshot rejected genuine file recreation")
	}
	select {
	case <-changes:
	default:
		t.Fatal("register snapshot recreation did not publish a structural change")
	}
}

func TestManagerRegisterFileSnapshotReadDoesNotHoldGlobalLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "slow.json")
	raw := []byte(`{"type":"codex","prefix":"slow"}`)
	writeRegisterSnapshotFile(t, path, raw)
	manager := NewManager(nil, nil, nil)
	started, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	read := readAuthSnapshotFileFunc
	readAuthSnapshotFileFunc = func(candidate string) ([]byte, error) {
		if candidate == path {
			close(started)
			<-release
		}
		return read(candidate)
	}
	var slowOK bool
	var otherFinished chan struct{}
	t.Cleanup(func() {
		unblock()
		select {
		case <-finished:
			readAuthSnapshotFileFunc = read
		case <-time.After(5 * time.Second):
			t.Error("register snapshot slow operation did not join")
		}
		if otherFinished != nil {
			select {
			case <-otherFinished:
			case <-time.After(5 * time.Second):
				t.Error("register snapshot unrelated operation did not join")
			}
		}
	})
	go func() {
		defer close(finished)
		auth, errRegister := manager.Register(WithSkipPersist(context.Background()), registerSnapshotAuth(path, raw))
		slowOK = errRegister == nil && auth != nil
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("register snapshot read did not start")
	}
	otherDone := make(chan bool, 1)
	otherFinished = make(chan struct{})
	go func() {
		defer close(otherFinished)
		_, errRegister := manager.Register(WithSkipPersist(context.Background()), &Auth{ID: "other", Provider: "codex"})
		_, present := manager.GetByID("other")
		otherDone <- errRegister == nil && present
	}()
	select {
	case okay := <-otherDone:
		if !okay {
			t.Fatal("register snapshot unrelated registration failed")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("register snapshot read blocked unrelated registration")
	}
	unblock()
	select {
	case <-finished:
		if !slowOK {
			t.Fatal("register snapshot slow registration failed")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("register snapshot slow registration did not finish")
	}
}

func writeRegisterSnapshotFile(t *testing.T, path string, raw []byte) {
	t.Helper()
	doc := ojson.NewDoc(raw)
	if os.WriteFile(path, doc.Bytes(), 0o600) != nil {
		t.Fatal("register snapshot fixture: write failed")
	}
}

func registerSnapshotAuth(path string, raw []byte) *Auth {
	prefix := ojson.GetBytes(raw, "prefix").String()
	auth := &Auth{ID: filepath.Base(path), FileName: filepath.Base(path), Provider: "codex", Prefix: prefix, Status: StatusActive,
		Attributes: map[string]string{AttributePath: path}, Metadata: map[string]any{"type": "codex", "prefix": prefix}}
	auth.BindFileSnapshot(path, raw)
	return auth
}

func registerSnapshotEpoch(manager *Manager, id string) uint64 {
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	return manager.authEpochs[id]
}

type registerSnapshotStore struct{ saves atomic.Int32 }

func (*registerSnapshotStore) List(context.Context) ([]*Auth, error) { return nil, nil }
func (s *registerSnapshotStore) Save(context.Context, *Auth) (string, error) {
	s.saves.Add(1)
	return "synthetic-file", nil
}
func (*registerSnapshotStore) Delete(context.Context, string) error { return nil }

type registerSnapshotHook struct {
	NoopHook
	calls atomic.Int32
}

func (h *registerSnapshotHook) OnAuthRegistered(context.Context, *Auth) { h.calls.Add(1) }
