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

func TestMarkResult_DoesNotPersistOnSteadySuccess(t *testing.T) {
	store := &countingStore{}
	mgr := NewManager(store, nil, nil)
	auth := &Auth{
		ID:       "auth-1",
		Provider: "antigravity",
		Metadata: map[string]any{"type": "antigravity"},
	}

	if _, err := mgr.Register(WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	mgr.MarkResult(context.Background(), Result{
		AuthID:   "auth-1",
		Provider: "antigravity",
		Model:    "m1",
		Success:  true,
	})
	first := store.saveCount.Load()

	mgr.MarkResult(context.Background(), Result{
		AuthID:   "auth-1",
		Provider: "antigravity",
		Model:    "m1",
		Success:  true,
	})
	second := store.saveCount.Load()

	if second != first {
		t.Fatalf("expected no extra Save on steady success, got first=%d second=%d", first, second)
	}
}

func TestMarkResult_PersistsOnFailureTransition(t *testing.T) {
	store := &countingStore{}
	mgr := NewManager(store, nil, nil)
	auth := &Auth{
		ID:       "auth-1",
		Provider: "antigravity",
		Metadata: map[string]any{"type": "antigravity"},
	}

	if _, err := mgr.Register(WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	mgr.MarkResult(context.Background(), Result{
		AuthID:   "auth-1",
		Provider: "antigravity",
		Model:    "m1",
		Success:  true,
	})
	beforeFail := store.saveCount.Load()

	retryAfter := 2 * time.Minute
	mgr.MarkResult(context.Background(), Result{
		AuthID:     "auth-1",
		Provider:   "antigravity",
		Model:      "m1",
		Success:    false,
		RetryAfter: &retryAfter,
		Error:      &Error{HTTPStatus: 429, Message: "usage_limit_reached"},
	})
	afterFail := store.saveCount.Load()

	if afterFail <= beforeFail {
		t.Fatalf("expected Save on failure transition, got before=%d after=%d", beforeFail, afterFail)
	}
}

func TestMarkResult_PersistsOnRecoveryTransition(t *testing.T) {
	store := &countingStore{}
	mgr := NewManager(store, nil, nil)
	auth := &Auth{
		ID:       "auth-1",
		Provider: "antigravity",
		Metadata: map[string]any{"type": "antigravity"},
	}

	if _, err := mgr.Register(WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	mgr.MarkResult(context.Background(), Result{
		AuthID:   "auth-1",
		Provider: "antigravity",
		Model:    "m1",
		Success:  false,
		Error:    &Error{HTTPStatus: 401, Message: "unauthorized"},
	})
	beforeRecovery := store.saveCount.Load()

	mgr.MarkResult(context.Background(), Result{
		AuthID:   "auth-1",
		Provider: "antigravity",
		Model:    "m1",
		Success:  true,
	})
	afterRecovery := store.saveCount.Load()

	if afterRecovery <= beforeRecovery {
		t.Fatalf("expected Save on recovery transition, got before=%d after=%d", beforeRecovery, afterRecovery)
	}
}
