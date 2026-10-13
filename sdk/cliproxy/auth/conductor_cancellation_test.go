package auth

import (
	"context"
	"errors"
	"math"
	"sync/atomic"
	"testing"
	"testing/synctest"
)

type cancellationStore struct {
	saves   atomic.Int32
	lists   atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func (s *cancellationStore) Save(_ context.Context, auth *Auth) (string, error) {
	s.saves.Add(1)
	if auth.Label == "blocked" {
		close(s.entered)
		<-s.release
	}
	return "", nil
}
func (s *cancellationStore) List(context.Context) ([]*Auth, error) {
	s.lists.Add(1)
	return nil, nil
}
func (*cancellationStore) Delete(context.Context, string) error { return nil }

type cancellationHook struct{ calls atomic.Int32 }

func (h *cancellationHook) OnAuthRegistered(context.Context, *Auth) { h.calls.Add(1) }
func (h *cancellationHook) OnAuthUpdated(context.Context, *Auth)    { h.calls.Add(1) }
func (*cancellationHook) OnResult(context.Context, Result)          {}

func assertCanceledMutation(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("expected context cancellation, got %v", err)
		}
	default:
		t.Error("canceled operation still waiting for lock")
	}
}

func TestManagerCanceledLoadUnblocksUnrelatedMutation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := &cancellationStore{entered: make(chan struct{}), release: make(chan struct{})}
		manager := NewManager(store, nil, nil)
		saveDone := make(chan error, 1)
		go func() {
			_, err := manager.Register(context.Background(), &Auth{ID: "A", Provider: "audit", Label: "blocked", Metadata: map[string]any{"type": "audit"}})
			saveDone <- err
		}()
		<-store.entered
		ctx, cancel := context.WithCancel(context.Background())
		loadDone := make(chan error, 1)
		go func() { loadDone <- manager.Load(ctx) }()
		synctest.Wait()
		bDone := make(chan error, 1)
		go func() {
			_, err := manager.Register(context.Background(), &Auth{ID: "B", Provider: "audit"})
			bDone <- err
		}()
		synctest.Wait()
		select {
		case <-bDone:
			t.Error("B overtook queued reload")
		default:
		}
		cancel()
		synctest.Wait()
		assertCanceledMutation(t, loadDone)
		select {
		case err := <-bDone:
			if err != nil {
				t.Error(err)
			}
		default:
			t.Error("canceled reload still blocks unrelated B")
		}
		if store.lists.Load() != 0 {
			t.Error("canceled reload called List")
		}
		close(store.release)
		synctest.Wait()
		if err := <-saveDone; err != nil {
			t.Error(err)
		}
		if !manager.authLoadGate.TryAcquire(math.MaxInt64) {
			t.Error("reload barrier permit leaked")
		} else {
			manager.authLoadGate.Release(math.MaxInt64)
		}
	})
}

func TestManagerCanceledMutationWait(t *testing.T) {
	for _, operation := range []string{"Register", "Update", "Prepare", "Refresh"} {
		for _, waitOn := range []string{"barrier", "gate"} {
			t.Run(operation+"/"+waitOn, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					store := &cancellationStore{}
					hook := &cancellationHook{}
					manager := NewManager(store, nil, hook)
					base, err := manager.Register(WithSkipPersist(context.Background()), &Auth{ID: "A", Provider: "audit", Metadata: map[string]any{"type": "audit"}})
					if err != nil {
						t.Fatal(err)
					}
					hook.calls.Store(0)
					var release func()
					if waitOn == "barrier" {
						if errAcquire := manager.authLoadGate.Acquire(context.Background(), math.MaxInt64); errAcquire != nil {
							t.Fatal(errAcquire)
						}
						release = func() { manager.authLoadGate.Release(math.MaxInt64) }
					} else {
						release = manager.lockAuthMutation(base.ID)
					}
					ctx, cancel := context.WithCancel(context.Background())
					done := make(chan error, 1)
					go func() {
						var errMutation error
						switch operation {
						case "Register":
							_, errMutation = manager.Register(ctx, base.Clone())
						case "Update":
							_, errMutation = manager.Update(ctx, base.Clone())
						case "Prepare":
							_, errMutation = manager.UpdatePreparedAuth(ctx, base, base.Clone())
						case "Refresh":
							_, errMutation = manager.UpdateRefreshedAuth(ctx, base, base.Clone())
						}
						done <- errMutation
					}()
					synctest.Wait()
					cancel()
					synctest.Wait()
					assertCanceledMutation(t, done)
					release()
					synctest.Wait()
					if store.saves.Load() != 0 || hook.calls.Load() != 0 {
						t.Error("canceled mutation invoked Save or hook")
					}
					if got, _ := manager.GetByID(base.ID); got.Generation != base.Generation || got.RegistrationEpoch != base.RegistrationEpoch {
						t.Error("canceled mutation published auth")
					}
					if !manager.authLoadGate.TryAcquire(math.MaxInt64) {
						t.Error("mutation leaked reload barrier permit")
					} else {
						manager.authLoadGate.Release(math.MaxInt64)
					}
					value, _ := manager.authMutationLocks.Load(base.ID)
					if len(value.(chan struct{})) != 0 {
						t.Error("mutation leaked credential gate")
					}
				})
			})
		}
	}
}

func TestManagerMutationNilContext(t *testing.T) {
	manager := NewManager(&cancellationStore{}, nil, nil)
	auth, err := manager.Register(nil, &Auth{ID: "A", Provider: "audit", Metadata: map[string]any{"type": "audit"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, errUpdate := manager.Update(nil, auth); errUpdate != nil {
		t.Fatal(errUpdate)
	}
	if errLoad := manager.Load(nil); errLoad != nil {
		t.Fatal(errLoad)
	}
}
