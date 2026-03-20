package auth

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
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

type blockingStore struct {
	started chan struct{}
	release chan struct{}
	count   atomic.Int32
}

func (s *blockingStore) List(context.Context) ([]*Auth, error) { return nil, nil }

func (s *blockingStore) Save(context.Context, *Auth) (string, error) {
	s.count.Add(1)
	select {
	case s.started <- struct{}{}:
	default:
	}
	<-s.release
	return "", nil
}

func (s *blockingStore) Delete(context.Context, string) error { return nil }

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

func TestMarkResult_PersistsAsynchronously(t *testing.T) {
	store := &blockingStore{
		started: make(chan struct{}, 1),
		release: make(chan struct{}),
	}
	mgr := NewManager(store, &RoundRobinSelector{}, nil)
	model := "test-model"
	if _, errRegister := mgr.Register(WithSkipPersist(context.Background()), &Auth{
		ID:       "auth-1",
		Provider: "gemini",
		Metadata: map[string]any{"type": "gemini"},
		ModelStates: map[string]*ModelState{
			model: {},
		},
	}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	done := make(chan struct{})
	go func() {
		mgr.MarkResult(context.Background(), Result{
			AuthID:   "auth-1",
			Provider: "gemini",
			Model:    model,
			Success:  false,
			Error:    &Error{HTTPStatus: 429, Message: "quota"},
		})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(200 * time.Millisecond):
		t.Fatalf("MarkResult blocked on persistence")
	}

	select {
	case <-store.started:
	case <-time.After(1 * time.Second):
		t.Fatalf("expected async Save to start")
	}

	close(store.release)
}

func TestMarkResult_SkipPersistStillSkipsAsyncPersistence(t *testing.T) {
	store := &countingStore{}
	mgr := NewManager(store, &RoundRobinSelector{}, nil)
	model := "test-model"
	if _, errRegister := mgr.Register(WithSkipPersist(context.Background()), &Auth{
		ID:       "auth-1",
		Provider: "gemini",
		Metadata: map[string]any{"type": "gemini"},
		ModelStates: map[string]*ModelState{
			model: {},
		},
	}); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	mgr.MarkResult(WithSkipPersist(context.Background()), Result{
		AuthID:   "auth-1",
		Provider: "gemini",
		Model:    model,
		Success:  false,
		Error:    &Error{HTTPStatus: 429, Message: "quota"},
	})

	time.Sleep(50 * time.Millisecond)
	if got := store.saveCount.Load(); got != 0 {
		t.Fatalf("expected 0 Save calls, got %d", got)
	}
}
