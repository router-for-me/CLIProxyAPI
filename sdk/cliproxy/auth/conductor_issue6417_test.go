package auth

import (
	"context"
	"testing"
	"time"
)

type issue6417BlockingRefreshExecutor struct {
	schedulerProviderTestExecutor
	started chan struct{}
	release chan struct{}
}

func (e *issue6417BlockingRefreshExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	close(e.started)
	<-e.release
	auth.Metadata["access_token"] = "access-token-a-refreshed"
	auth.Metadata["refresh_token"] = "refresh-token-a-refreshed"
	auth.Metadata["expired"] = time.Now().Add(time.Hour).Format(time.RFC3339)
	return auth, nil
}

func TestManager_RefreshAuth_DropsStaleConcurrentCredential(t *testing.T) {
	ctx := context.Background()
	store := newMemoryAuthTestStore()
	manager := NewManager(store, &RoundRobinSelector{}, nil)
	executor := &issue6417BlockingRefreshExecutor{
		schedulerProviderTestExecutor: schedulerProviderTestExecutor{provider: "antigravity"},
		started:                       make(chan struct{}),
		release:                       make(chan struct{}),
	}
	manager.RegisterExecutor(executor)

	auth := &Auth{
		ID:       "issue-6417-auth",
		Provider: "antigravity",
		Status:   StatusActive,
		Metadata: map[string]any{
			"access_token":  "access-token-a",
			"refresh_token": "refresh-token-a",
			"expired":       time.Now().Add(-time.Hour).Format(time.RFC3339),
		},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	refreshDone := make(chan struct{})
	go func() {
		manager.refreshAuth(ctx, auth.ID)
		close(refreshDone)
	}()
	<-executor.started

	current, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatalf("auth %q not found", auth.ID)
	}
	current.Metadata["access_token"] = "access-token-b"
	current.Metadata["refresh_token"] = "refresh-token-b"
	if _, errUpdate := manager.Update(ctx, current); errUpdate != nil {
		t.Fatalf("update concurrent credential: %v", errUpdate)
	}

	close(executor.release)
	<-refreshDone

	updated, ok := manager.GetByID(auth.ID)
	if !ok {
		t.Fatalf("auth %q not found after refresh", auth.ID)
	}
	if got := updated.Metadata["access_token"]; got != "access-token-b" {
		t.Fatalf("access_token = %q, want access-token-b", got)
	}
	if got := updated.Metadata["refresh_token"]; got != "refresh-token-b" {
		t.Fatalf("refresh_token = %q, want refresh-token-b", got)
	}

	store.mu.Lock()
	persisted := store.auths[auth.ID]
	store.mu.Unlock()
	if persisted == nil {
		t.Fatalf("persisted auth %q not found", auth.ID)
	}
	if got := persisted.Metadata["access_token"]; got != "access-token-b" {
		t.Fatalf("persisted access_token = %q, want access-token-b", got)
	}
	if got := persisted.Metadata["refresh_token"]; got != "refresh-token-b" {
		t.Fatalf("persisted refresh_token = %q, want refresh-token-b", got)
	}
}
