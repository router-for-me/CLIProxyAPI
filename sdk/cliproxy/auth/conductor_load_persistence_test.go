package auth

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"testing/synctest"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// reloadAuthStore snapshots records independently of blocked storage I/O.
type reloadAuthStore struct {
	mu          sync.Mutex
	records     map[string]*Auth
	saveEntered chan struct{}
	saveRelease chan struct{}
	listEntered chan struct{}
	listRelease chan struct{}
}

func (*reloadAuthStore) Delete(context.Context, string) error { return nil }
func (s *reloadAuthStore) Save(_ context.Context, auth *Auth) (string, error) {
	if s.saveEntered != nil {
		close(s.saveEntered)
		<-s.saveRelease
	}
	auth.FileName = auth.ID + ".json"
	s.mu.Lock()
	s.records[auth.ID] = auth.Clone()
	s.mu.Unlock()
	return auth.FileName, nil
}
func (s *reloadAuthStore) List(context.Context) ([]*Auth, error) {
	s.mu.Lock()
	items := make([]*Auth, 0, len(s.records))
	for _, auth := range s.records {
		items = append(items, auth.Clone())
	}
	s.mu.Unlock()
	close(s.listEntered)
	if s.listRelease != nil {
		<-s.listRelease
	}
	return items, nil
}

func assertUnrelatedAuthReadable(t *testing.T, manager *Manager) {
	t.Helper()
	done := make(chan error, 1)
	go func() {
		if _, ok := manager.GetByID("B"); !ok {
			done <- errors.New("B disappeared")
			return
		}
		selected, err := manager.SelectAuth(context.Background(), "audit-b", "", cliproxyexecutor.Options{})
		if err == nil && (selected == nil || selected.ID != "B") {
			err = errors.New("did not select B")
		}
		done <- err
	}()
	synctest.Wait()
	select {
	case err := <-done:
		if err != nil {
			t.Error(err)
		}
	default:
		t.Error("storage I/O blocked reading/selecting unrelated B")
	}
}

func TestManagerBlockedSaveSerializesLoad(t *testing.T) {
	for _, operation := range []string{"Register", "ReRegister", "Update", "Prepare", "Refresh", "MarkResult"} {
		t.Run(operation, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx := context.Background()
				store := &reloadAuthStore{records: make(map[string]*Auth), saveEntered: make(chan struct{}), saveRelease: make(chan struct{}), listEntered: make(chan struct{})}
				manager := NewManager(store, nil, nil)
				manager.RegisterExecutor(&aliasRoutingExecutor{id: "audit-b"})
				b, err := manager.Register(WithSkipPersist(ctx), &Auth{ID: "B", Provider: "audit-b", Status: StatusActive})
				if err != nil {
					t.Fatal(err)
				}
				store.records[b.ID] = b.Clone()
				base := &Auth{ID: "A", Provider: "meta", Status: StatusActive, Metadata: map[string]any{"access_token": "old"}}
				if operation != "Register" {
					base, err = manager.Register(WithSkipPersist(ctx), base)
					if err != nil {
						t.Fatal(err)
					}
					store.records[base.ID] = base.Clone()
				}
				updated := base.Clone()
				updated.Metadata["access_token"] = "minted"
				saveDone := make(chan error, 1)
				go func() {
					var errSave error
					switch operation {
					case "Register", "ReRegister":
						_, errSave = manager.Register(ctx, updated)
					case "Update":
						_, errSave = manager.Update(ctx, updated)
					case "Prepare":
						_, errSave = manager.UpdatePreparedAuth(ctx, base, updated)
					case "Refresh":
						_, errSave = manager.UpdateRefreshedAuth(ctx, base, updated)
					case "MarkResult":
						manager.MarkResult(ctx, Result{AuthID: base.ID, Provider: base.Provider, Success: true})
					}
					saveDone <- errSave
				}()
				<-store.saveEntered
				loadDone := make(chan error, 1)
				go func() { loadDone <- manager.Load(ctx) }()
				synctest.Wait()
				select {
				case <-store.listEntered:
					t.Error("Load read stale records before Save/publication completed")
				default:
				}
				assertUnrelatedAuthReadable(t, manager)
				close(store.saveRelease)
				if errSave := <-saveDone; errSave != nil {
					t.Errorf("save/publication failed: %v", errSave)
				}
				if errLoad := <-loadDone; errLoad != nil {
					t.Fatal(errLoad)
				}
				got, ok := manager.GetByID(base.ID)
				if !ok {
					t.Fatal("Load lost the saved credential")
				}
				store.mu.Lock()
				saved := store.records[base.ID].Clone()
				store.mu.Unlock()
				if got.RegistrationEpoch <= saved.RegistrationEpoch {
					t.Error("Load did not run after save/publication")
				}
				saved.RegistrationEpoch, saved.Generation = got.RegistrationEpoch, got.Generation
				if !reflect.DeepEqual(saved, got) {
					t.Errorf("store and manager diverged: saved=%+v got=%+v", saved, got)
				}
				manager.scheduler.mu.Lock()
				scheduled := manager.scheduler.providers["meta"].auths[base.ID].auth.Clone()
				manager.scheduler.mu.Unlock()
				if !reflect.DeepEqual(got, scheduled) {
					t.Error("scheduler differs from reloaded credential")
				}
			})
		})
	}
}

func TestManagerBlockedLoadSerializesNewRegistration(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		store := &reloadAuthStore{records: make(map[string]*Auth), saveEntered: make(chan struct{}), saveRelease: make(chan struct{}), listEntered: make(chan struct{}), listRelease: make(chan struct{})}
		manager := NewManager(store, nil, nil)
		manager.RegisterExecutor(&aliasRoutingExecutor{id: "audit-b"})
		b, err := manager.Register(WithSkipPersist(ctx), &Auth{ID: "B", Provider: "audit-b", Status: StatusActive})
		if err != nil {
			t.Fatal(err)
		}
		store.records[b.ID] = b.Clone()
		loadDone := make(chan error, 1)
		go func() { loadDone <- manager.Load(ctx) }()
		<-store.listEntered
		saveDone := make(chan error, 1)
		go func() {
			_, errRegister := manager.Register(ctx, &Auth{ID: "new", Provider: "audit", Metadata: map[string]any{"type": "audit"}})
			saveDone <- errRegister
		}()
		synctest.Wait()
		select {
		case <-store.saveEntered:
			t.Error("new registration overtook Load's whole-map replacement")
		default:
		}
		assertUnrelatedAuthReadable(t, manager)
		close(store.listRelease)
		if errLoad := <-loadDone; errLoad != nil {
			t.Fatal(errLoad)
		}
		<-store.saveEntered
		close(store.saveRelease)
		if errSave := <-saveDone; errSave != nil {
			t.Fatal(errSave)
		}
		if got, ok := manager.GetByID("new"); !ok || got.FileName != "new.json" {
			t.Fatal("Load discarded a new registration")
		}
	})
}
