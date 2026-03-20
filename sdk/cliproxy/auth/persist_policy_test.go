package auth

import (
	"context"
	"sync/atomic"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

type countingStore struct {
	saveCount   atomic.Int32
	deleteCount atomic.Int32
}

func (s *countingStore) List(context.Context) ([]*Auth, error) { return nil, nil }

func (s *countingStore) Save(context.Context, *Auth) (string, error) {
	s.saveCount.Add(1)
	return "", nil
}

func (s *countingStore) Delete(context.Context, string) error {
	s.deleteCount.Add(1)
	return nil
}

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

func TestManager_MarkResult_DoesNotPersistRuntimeState(t *testing.T) {
	store := &countingStore{}
	mgr := NewManager(store, &RoundRobinSelector{}, nil)
	auth := &Auth{
		ID:       "auth-1",
		Provider: "gemini",
		Metadata: map[string]any{"type": "gemini"},
	}

	if _, err := mgr.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register() returned error: %v", err)
	}
	if got := store.saveCount.Load(); got != 1 {
		t.Fatalf("expected 1 Save call after Register, got %d", got)
	}

	mgr.MarkResult(context.Background(), Result{
		AuthID:  auth.ID,
		Model:   "gemini-2.5-pro",
		Success: false,
		Error:   &Error{HTTPStatus: 429, Message: "quota"},
	})

	if got := store.saveCount.Load(); got != 1 {
		t.Fatalf("expected Save call count to remain 1 after MarkResult, got %d", got)
	}
}

func TestManager_MarkResult_ExtremeModeDeletesUnauthorizedAuth(t *testing.T) {
	store := &countingStore{}
	mgr := NewManager(store, &RoundRobinSelector{}, nil)
	mgr.SetConfig(&internalconfig.Config{ExtremeMode: true})
	auth := &Auth{
		ID:       "auth-delete-401",
		Provider: "gemini",
		Metadata: map[string]any{"type": "gemini"},
	}

	if _, err := mgr.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register() returned error: %v", err)
	}

	mgr.MarkResult(context.Background(), Result{
		AuthID:  auth.ID,
		Success: false,
		Error:   &Error{HTTPStatus: 401, Message: "unauthorized"},
	})

	if _, ok := mgr.GetByID(auth.ID); ok {
		t.Fatalf("GetByID(%q) ok = true, want false", auth.ID)
	}
	if got := store.deleteCount.Load(); got != 1 {
		t.Fatalf("expected 1 Delete call, got %d", got)
	}
}

func TestManager_MarkResult_NonExtremeUnauthorizedKeepsAuth(t *testing.T) {
	store := &countingStore{}
	mgr := NewManager(store, &RoundRobinSelector{}, nil)
	auth := &Auth{
		ID:       "auth-keep-401",
		Provider: "gemini",
		Metadata: map[string]any{"type": "gemini"},
	}

	if _, err := mgr.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register() returned error: %v", err)
	}

	mgr.MarkResult(context.Background(), Result{
		AuthID:  auth.ID,
		Success: false,
		Error:   &Error{HTTPStatus: 401, Message: "unauthorized"},
	})

	if _, ok := mgr.GetByID(auth.ID); !ok {
		t.Fatalf("GetByID(%q) ok = false, want true", auth.ID)
	}
	if got := store.deleteCount.Load(); got != 0 {
		t.Fatalf("expected 0 Delete calls, got %d", got)
	}
}

func TestManager_MarkResult_ExtremeModeDeletesUsageLimitAuth(t *testing.T) {
	store := &countingStore{}
	mgr := NewManager(store, &RoundRobinSelector{}, nil)
	mgr.SetConfig(&internalconfig.Config{ExtremeMode: true})
	auth := &Auth{
		ID:       "auth-delete-usage-limit",
		Provider: "gemini",
		Metadata: map[string]any{"type": "gemini"},
	}

	if _, err := mgr.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register() returned error: %v", err)
	}

	mgr.MarkResult(context.Background(), Result{
		AuthID:  auth.ID,
		Success: false,
		Error:   &Error{Code: "usage_limit_reached", Message: `{"error":{"code":"usage_limit_reached"}}`},
	})

	if _, ok := mgr.GetByID(auth.ID); ok {
		t.Fatalf("GetByID(%q) ok = true, want false", auth.ID)
	}
	if got := store.deleteCount.Load(); got != 1 {
		t.Fatalf("expected 1 Delete call, got %d", got)
	}
}
