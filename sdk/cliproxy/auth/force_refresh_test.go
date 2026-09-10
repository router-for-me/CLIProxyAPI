package auth

import (
	"context"
	"testing"
)

func TestManager_ForceRefreshAuth_ClearsErrorAndRefreshes(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	executor := &countingRefreshExecutor{id: "antigravity"}
	manager.RegisterExecutor(executor)

	auth := &Auth{
		ID:          "ag-err",
		Provider:    "antigravity",
		Status:      StatusError,
		Unavailable: true,
		LastError:   &Error{Code: "unauthorized", Message: "token revoked"},
		Metadata: map[string]any{
			"access_token":  "old-tok",
			"refresh_token": "ref-tok",
		},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	// Verify before: hasUnauthorizedAuthFailure is true
	if !hasUnauthorizedAuthFailure(auth) {
		t.Fatal("expected hasUnauthorizedAuthFailure to be true initially")
	}

	refreshed, err := manager.ForceRefreshAuth(ctx, auth.ID)
	if err != nil {
		t.Fatalf("ForceRefreshAuth failed: %v", err)
	}
	if refreshed.Status != StatusActive {
		t.Fatalf("expected status Active, got %s", refreshed.Status)
	}
	if refreshed.Unavailable {
		t.Fatal("expected Unavailable to be false after force refresh")
	}
	if refreshed.LastError != nil {
		t.Fatalf("expected LastError to be nil, got %+v", refreshed.LastError)
	}
	if executor.refreshCalls.Load() != 1 {
		t.Fatalf("expected 1 refresh call, got %d", executor.refreshCalls.Load())
	}
}

func TestManager_ForceRefreshAll(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	executor := &countingRefreshExecutor{id: "antigravity"}
	manager.RegisterExecutor(executor)

	auth1 := &Auth{
		ID:       "ag-1",
		Provider: "antigravity",
		Metadata: map[string]any{"refresh_token": "ref-1"},
	}
	auth2 := &Auth{
		ID:       "ag-2",
		Provider: "antigravity",
		Metadata: map[string]any{"refresh_token": "ref-2"},
	}
	auth3NoRef := &Auth{
		ID:       "ag-no-ref",
		Provider: "antigravity",
		Metadata: map[string]any{"access_token": "no-refresh"},
	}
	_, _ = manager.Register(ctx, auth1)
	_, _ = manager.Register(ctx, auth2)
	_, _ = manager.Register(ctx, auth3NoRef)

	results := manager.ForceRefreshAll(ctx)
	if len(results) != 2 {
		t.Fatalf("expected 2 results (only credentials with refresh_token), got %d", len(results))
	}
	for _, res := range results {
		if !res.Success {
			t.Fatalf("expected success for %s, got error: %s", res.ID, res.Error)
		}
	}
	if executor.refreshCalls.Load() != 2 {
		t.Fatalf("expected 2 refresh calls, got %d", executor.refreshCalls.Load())
	}
}

func TestManager_ForceRefreshAuth_PreservesErrorOnFailure(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	// No executor registered, so refresh will fail

	auth := &Auth{
		ID:          "ag-fail",
		Provider:    "antigravity",
		Status:      StatusError,
		Unavailable: true,
		LastError:   &Error{Code: "unauthorized", Message: "token revoked"},
		Metadata: map[string]any{
			"access_token":  "old-tok",
			"refresh_token": "ref-tok",
		},
	}
	if _, errRegister := manager.Register(ctx, auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	_, err := manager.ForceRefreshAuth(ctx, auth.ID)
	if err == nil {
		t.Fatal("expected ForceRefreshAuth to fail when executor is missing")
	}

	current, exists := manager.GetByID(auth.ID)
	if !exists {
		t.Fatal("auth should exist")
	}
	if current.Status != StatusError {
		t.Fatalf("status should remain StatusError on failure, got %s", current.Status)
	}
	if !current.Unavailable {
		t.Fatal("Unavailable should remain true on failure")
	}
	if current.LastError == nil {
		t.Fatal("LastError should not be wiped on failure")
	}
}
