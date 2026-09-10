package auth

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"testing/synctest"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

type blockedBatchRefreshExecutor struct {
	countingRefreshExecutor
	release <-chan struct{}
	calls   atomic.Int32
	failID  string
}

func (e *blockedBatchRefreshExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	e.calls.Add(1)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-e.release:
	}
	if auth.ID == e.failID {
		return nil, errors.New("fixture refresh failure")
	}
	return e.countingRefreshExecutor.Refresh(ctx, auth)
}

func newBatchRefreshManager(t *testing.T, workers, credentials int, release <-chan struct{}) (*Manager, *blockedBatchRefreshExecutor) {
	t.Helper()
	manager := NewManager(nil, nil, nil)
	manager.SetConfig(&internalconfig.Config{AuthAutoRefreshWorkers: workers})
	executor := &blockedBatchRefreshExecutor{
		countingRefreshExecutor: countingRefreshExecutor{id: "batch-refresh"},
		release:                 release,
	}
	manager.RegisterExecutor(executor)
	for i := range credentials {
		auth := &Auth{
			ID:       fmt.Sprintf("batch-%d", i),
			Provider: executor.Identifier(),
			Metadata: map[string]any{"refresh_token": "fixture-refresh-token"},
		}
		if _, errRegister := manager.Register(t.Context(), auth); errRegister != nil {
			t.Fatalf("register credential: %v", errRegister)
		}
	}
	return manager, executor
}

func TestForceRefreshAllBoundsConcurrency(t *testing.T) {
	for _, tc := range []struct {
		name        string
		workers     int
		credentials int
		wantActive  int32
	}{
		{name: "default", credentials: 21, wantActive: 16},
		{name: "negative uses default", workers: -1, credentials: 21, wantActive: 16},
		{name: "serial", workers: 1, credentials: 7, wantActive: 1},
		{name: "configured", workers: 3, credentials: 9, wantActive: 3},
		{name: "fewer credentials than workers", workers: 99, credentials: 3, wantActive: 3},
		{name: "empty", workers: 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				release := make(chan struct{})
				manager, executor := newBatchRefreshManager(t, tc.workers, tc.credentials, release)
				executor.failID = "batch-0"
				done := make(chan []ForceRefreshResult, 1)
				go func() { done <- manager.ForceRefreshAll(t.Context()) }()
				synctest.Wait()
				if got := executor.calls.Load(); got != tc.wantActive {
					t.Errorf("concurrent refresh calls = %d, want %d", got, tc.wantActive)
				}
				close(release)
				results := <-done
				if len(results) != tc.credentials {
					t.Fatalf("results = %d, want %d", len(results), tc.credentials)
				}
				if got := executor.calls.Load(); got != int32(tc.credentials) {
					t.Errorf("total refresh calls = %d, want %d", got, tc.credentials)
				}
				seen := make(map[string]bool)
				for _, result := range results {
					if seen[result.ID] || result.ID == "" {
						t.Errorf("duplicate or empty result ID: %q", result.ID)
					}
					seen[result.ID] = true
					if result.ID == executor.failID {
						if result.Success || result.Error != "fixture refresh failure" {
							t.Errorf("failed refresh result = %+v", result)
						}
					} else if !result.Success || result.Error != "" {
						t.Errorf("successful refresh result = %+v", result)
					}
				}
			})
		})
	}
}

func TestForceRefreshAllCancellationSkipsQueuedCredentials(t *testing.T) {
	for _, canceledBeforeStart := range []bool{false, true} {
		t.Run(fmt.Sprintf("canceled before start=%t", canceledBeforeStart), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				const workers, credentials = 2, 9
				manager, executor := newBatchRefreshManager(t, workers, credentials, make(chan struct{}))
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if canceledBeforeStart {
					cancel()
				}
				done := make(chan []ForceRefreshResult, 1)
				go func() { done <- manager.ForceRefreshAll(ctx) }()
				synctest.Wait()
				cancel()
				results := <-done
				wantCalls := int32(workers)
				if canceledBeforeStart {
					wantCalls = 0
				}
				if got := executor.calls.Load(); got != wantCalls {
					t.Errorf("refresh calls = %d, want %d", got, wantCalls)
				}
				if len(results) != credentials {
					t.Fatalf("results = %d, want %d", len(results), credentials)
				}
				for _, result := range results {
					if result.ID == "" || result.Success || result.Error != context.Canceled.Error() {
						t.Errorf("canceled refresh result = %+v", result)
					}
				}
			})
		})
	}
}

func TestForceRefreshAllNilContext(t *testing.T) {
	release := make(chan struct{})
	close(release)
	manager, executor := newBatchRefreshManager(t, 1, 2, release)
	results := manager.ForceRefreshAll(nil)
	if len(results) != 2 || executor.calls.Load() != 2 {
		t.Fatalf("results = %+v, calls = %d", results, executor.calls.Load())
	}
	for _, result := range results {
		if !result.Success {
			t.Errorf("nil context refresh failed: %+v", result)
		}
	}
}
