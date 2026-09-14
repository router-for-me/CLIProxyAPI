package auth

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
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

type concurrencyTrackingRefreshExecutor struct {
	id            string
	current       atomic.Int32
	maxConcurrent atomic.Int32
	totalEntered  atomic.Int32
	enteredCh     chan struct{}
	releaseCh     chan struct{}
}

func (e *concurrencyTrackingRefreshExecutor) Identifier() string { return e.id }
func (e *concurrencyTrackingRefreshExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (e *concurrencyTrackingRefreshExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}
func (e *concurrencyTrackingRefreshExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (e *concurrencyTrackingRefreshExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}
func (e *concurrencyTrackingRefreshExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	e.totalEntered.Add(1)
	cur := e.current.Add(1)
	for {
		max := e.maxConcurrent.Load()
		if cur <= max || e.maxConcurrent.CompareAndSwap(max, cur) {
			break
		}
	}
	if e.enteredCh != nil {
		e.enteredCh <- struct{}{}
	}
	if e.releaseCh != nil {
		<-e.releaseCh
	}
	e.current.Add(-1)
	if auth.Metadata == nil {
		auth.Metadata = make(map[string]any)
	}
	auth.Metadata["access_token"] = "refreshed-token"
	return auth, nil
}

func TestManager_ForceRefreshAll_WorkersBounded(t *testing.T) {
	ctx := context.Background()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.runtimeConfig.Store(&internalconfig.Config{AuthAutoRefreshWorkers: 2})

	executor := &concurrencyTrackingRefreshExecutor{
		id:        "antigravity",
		enteredCh: make(chan struct{}, 6),
		releaseCh: make(chan struct{}),
	}
	manager.RegisterExecutor(executor)

	for i := 0; i < 6; i++ {
		auth := &Auth{
			ID:       fmt.Sprintf("ag-%d", i),
			Provider: "antigravity",
			Metadata: map[string]any{"refresh_token": fmt.Sprintf("ref-%d", i)},
		}
		if _, err := manager.Register(ctx, auth); err != nil {
			t.Fatalf("register auth: %v", err)
		}
	}

	done := make(chan struct{})
	var results []ForceRefreshResult
	go func() {
		results = manager.ForceRefreshAll(ctx)
		close(done)
	}()

	// Wait for 2 workers to enter Refresh
	<-executor.enteredCh
	<-executor.enteredCh

	// If unbounded, remaining goroutines also enter Refresh
	// Release the blocked workers
	close(executor.releaseCh)
	<-done

	if len(results) != 6 {
		t.Fatalf("expected 6 results, got %d", len(results))
	}
	for _, res := range results {
		if !res.Success {
			t.Fatalf("expected success for %s, got error: %s", res.ID, res.Error)
		}
	}

	if max := executor.maxConcurrent.Load(); max > 2 {
		t.Fatalf("expected max concurrent calls <= 2, got %d", max)
	}
}

func TestManager_ForceRefreshAll_CanceledContextSkipsExecutors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.runtimeConfig.Store(&internalconfig.Config{AuthAutoRefreshWorkers: 2})
	executor := &countingRefreshExecutor{id: "antigravity"}
	manager.RegisterExecutor(executor)

	for i := 0; i < 6; i++ {
		auth := &Auth{
			ID:       fmt.Sprintf("ag-%d", i),
			Provider: "antigravity",
			Metadata: map[string]any{"refresh_token": fmt.Sprintf("ref-%d", i)},
		}
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("register auth: %v", err)
		}
	}

	results := manager.ForceRefreshAll(ctx)
	if len(results) != 6 {
		t.Fatalf("expected 6 results, got %d", len(results))
	}
	for _, res := range results {
		if res.Success {
			t.Fatalf("expected failure due to cancellation for %s", res.ID)
		}
		if res.Error != context.Canceled.Error() {
			t.Fatalf("expected error %q, got %q", context.Canceled.Error(), res.Error)
		}
	}
	if executor.refreshCalls.Load() != 0 {
		t.Fatalf("expected 0 refresh calls when context canceled, got %d", executor.refreshCalls.Load())
	}
}

func TestManager_ForceRefreshAll_DynamicCancellationSkipsRemaining(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.runtimeConfig.Store(&internalconfig.Config{AuthAutoRefreshWorkers: 2})

	executor := &concurrencyTrackingRefreshExecutor{
		id:        "antigravity",
		enteredCh: make(chan struct{}, 6),
		releaseCh: make(chan struct{}),
	}
	manager.RegisterExecutor(executor)

	for i := 0; i < 6; i++ {
		auth := &Auth{
			ID:       fmt.Sprintf("ag-%d", i),
			Provider: "antigravity",
			Metadata: map[string]any{"refresh_token": fmt.Sprintf("ref-%d", i)},
		}
		if _, err := manager.Register(context.Background(), auth); err != nil {
			t.Fatalf("register auth: %v", err)
		}
	}

	done := make(chan struct{})
	var results []ForceRefreshResult
	go func() {
		results = manager.ForceRefreshAll(ctx)
		close(done)
	}()

	// Wait for 2 workers to enter Refresh
	<-executor.enteredCh
	<-executor.enteredCh

	// Cancel context while first batch is in flight
	cancel()

	// Release the blocked workers
	close(executor.releaseCh)
	<-done

	if len(results) != 6 {
		t.Fatalf("expected 6 results, got %d", len(results))
	}

	// Exactly 2 workers entered Refresh; remaining 4 were canceled in queue
	if entered := executor.totalEntered.Load(); entered != 2 {
		t.Fatalf("expected exactly 2 entered calls, got %d", entered)
	}

	successCount := 0
	cancelCount := 0
	for _, res := range results {
		if res.Success {
			successCount++
		} else if res.Error == context.Canceled.Error() {
			cancelCount++
		}
	}
	if successCount != 2 {
		t.Fatalf("expected 2 successful refreshes, got %d", successCount)
	}
	if cancelCount != 4 {
		t.Fatalf("expected 4 canceled refreshes, got %d", cancelCount)
	}
}

func TestManager_RefreshWorkersResolution(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)

	// Default when config is empty
	if w := manager.refreshWorkers(); w != 16 {
		t.Fatalf("expected default 16 workers, got %d", w)
	}

	// Zero should keep default 16
	manager.runtimeConfig.Store(&internalconfig.Config{AuthAutoRefreshWorkers: 0})
	if w := manager.refreshWorkers(); w != 16 {
		t.Fatalf("expected 16 workers for 0, got %d", w)
	}

	// Negative should keep default 16
	manager.runtimeConfig.Store(&internalconfig.Config{AuthAutoRefreshWorkers: -5})
	if w := manager.refreshWorkers(); w != 16 {
		t.Fatalf("expected 16 workers for -5, got %d", w)
	}

	// Positive override
	manager.runtimeConfig.Store(&internalconfig.Config{AuthAutoRefreshWorkers: 4})
	if w := manager.refreshWorkers(); w != 4 {
		t.Fatalf("expected 4 workers, got %d", w)
	}
}
