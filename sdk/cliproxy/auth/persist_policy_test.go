package auth

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

type countingStore struct {
	saveCount atomic.Int32
}

func (s *countingStore) List(context.Context) ([]*Auth, error) { return nil, nil }

func (s *countingStore) Save(context.Context, *Auth) (string, error) {
	s.saveCount.Add(1)
	return "", nil
}

func (s *countingStore) Delete(context.Context, string) error { return nil }

var errPersistFailed = errors.New("persist failed")

type toggleFailStore struct {
	saveCount atomic.Int32
	fail      atomic.Bool
}

func (s *toggleFailStore) List(context.Context) ([]*Auth, error) { return nil, nil }

func (s *toggleFailStore) Save(context.Context, *Auth) (string, error) {
	s.saveCount.Add(1)
	if s.fail.Load() {
		return "", errPersistFailed
	}
	return "", nil
}

func (s *toggleFailStore) Delete(context.Context, string) error { return nil }

func TestWithSkipPersist_DisablesUpdatePersistence(t *testing.T) {
	store := &countingStore{}
	mgr := NewManager(store, nil, nil)
	auth := &Auth{
		ID:       "auth-1",
		Provider: "antigravity",
		Metadata: map[string]any{"type": "antigravity"},
	}

	if _, err := mgr.Update(context.Background(), auth); err != nil {
		t.Fatalf("Update returned error: %v", err)
	}
	if got := store.saveCount.Load(); got != 1 {
		t.Fatalf("expected 1 Save call, got %d", got)
	}

	ctxSkip := WithSkipPersist(context.Background())
	if _, err := mgr.Update(ctxSkip, auth); err != nil {
		t.Fatalf("Update(skipPersist) returned error: %v", err)
	}
	if got := store.saveCount.Load(); got != 1 {
		t.Fatalf("expected Save call count to remain 1, got %d", got)
	}
}

func TestWithSkipPersist_DisablesRegisterPersistence(t *testing.T) {
	store := &countingStore{}
	mgr := NewManager(store, nil, nil)
	auth := &Auth{
		ID:       "auth-1",
		Provider: "antigravity",
		Metadata: map[string]any{"type": "antigravity"},
	}

	if _, err := mgr.Register(WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatalf("Register(skipPersist) returned error: %v", err)
	}
	if got := store.saveCount.Load(); got != 0 {
		t.Fatalf("expected 0 Save calls, got %d", got)
	}
}

func TestRegister_ReturnsPersistenceErrorWithoutPublishingAuth(t *testing.T) {
	store := &toggleFailStore{}
	store.fail.Store(true)
	mgr := NewManager(store, nil, nil)
	auth := &Auth{
		ID:       "auth-1",
		Provider: "antigravity",
		Metadata: map[string]any{"type": "antigravity"},
	}

	if _, err := mgr.Register(context.Background(), auth); !errors.Is(err, errPersistFailed) {
		t.Fatalf("Register() error = %v, want %v", err, errPersistFailed)
	}
	if _, ok := mgr.GetByID("auth-1"); ok {
		t.Fatal("expected failed register to leave manager state unchanged")
	}
}

func TestUpdate_ReturnsPersistenceErrorWithoutPublishingAuth(t *testing.T) {
	store := &toggleFailStore{}
	mgr := NewManager(store, nil, nil)
	auth := &Auth{
		ID:       "auth-1",
		Provider: "antigravity",
		Metadata: map[string]any{"type": "antigravity"},
	}
	if _, err := mgr.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register() error = %v", err)
	}

	store.fail.Store(true)
	updated := auth.Clone()
	updated.Disabled = true
	if _, err := mgr.Update(context.Background(), updated); !errors.Is(err, errPersistFailed) {
		t.Fatalf("Update() error = %v, want %v", err, errPersistFailed)
	}
	got, ok := mgr.GetByID("auth-1")
	if !ok {
		t.Fatal("expected original auth to remain registered")
	}
	if got.Disabled {
		t.Fatal("expected failed update to leave previous auth state unchanged")
	}
}
