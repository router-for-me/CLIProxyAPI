package auth

import (
	"context"
	"errors"
	"net/http"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// stopTestExecutor refreshes every credential successfully.
type stopTestExecutor struct{}

func (stopTestExecutor) Identifier() string { return "stop-test" }

func (stopTestExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("not implemented")
}

func (stopTestExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, errors.New("not implemented")
}

func (stopTestExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	refreshed := auth.Clone()
	refreshed.Metadata = map[string]any{
		"access_token":  "fresh-token",
		"refresh_token": "refresh-1",
		"expires_at":    time.Now().Add(time.Hour).Format(time.RFC3339),
	}
	return refreshed, nil
}

func (stopTestExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("not implemented")
}

func (stopTestExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

// alwaysRefresh schedules the credential on every loop interval.
type alwaysRefresh struct{}

func (alwaysRefresh) ShouldRefresh(time.Time, *Auth) bool { return true }

// refreshSaveStore lets a test hold the save that follows a refresh.
type refreshSaveStore struct {
	armed   atomic.Bool
	entered chan struct{}
	hold    func()
	done    atomic.Bool
}

func (*refreshSaveStore) List(context.Context) ([]*Auth, error) { return nil, nil }

func (s *refreshSaveStore) Save(_ context.Context, auth *Auth) (string, error) {
	if !s.armed.Load() {
		return auth.ID, nil
	}
	select {
	case s.entered <- struct{}{}:
	default:
	}
	s.hold()
	s.done.Store(true)
	return auth.ID, nil
}

func (*refreshSaveStore) Delete(context.Context, string) error { return nil }

func startRefreshWithHeldSave(t *testing.T, store *refreshSaveStore) *Manager {
	t.Helper()
	manager := NewManager(store, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(stopTestExecutor{})
	if _, err := manager.Register(context.Background(), &Auth{
		ID:       "stop-waits-for-save",
		Provider: "stop-test",
		Runtime:  alwaysRefresh{},
		Metadata: map[string]any{
			"access_token":  "expired-token",
			"refresh_token": "refresh-1",
			"expires_at":    time.Now().Add(-time.Hour).Format(time.RFC3339),
		},
	}); err != nil {
		t.Fatalf("Register error: %v", err)
	}
	store.armed.Store(true)
	manager.StartAutoRefresh(context.Background(), 10*time.Millisecond)

	// The timer only guards against a hang; the save signals when it starts.
	hangGuard := time.NewTimer(30 * time.Second)
	defer hangGuard.Stop()
	select {
	case <-store.entered:
	case <-hangGuard.C:
		manager.StopAutoRefresh()
		t.Fatal("auto-refresh never saved the refreshed credential")
	}
	return manager
}

// StopAutoRefresh must not return while a refresh it started is still saving:
// callers (service shutdown, tests removing the auth directory) rely on no
// background save running afterwards.
func TestStopAutoRefreshWaitsForInFlightRefreshSave(t *testing.T) {
	var stopCalled atomic.Bool
	store := &refreshSaveStore{entered: make(chan struct{}, 1)}
	store.hold = func() {
		// Hold the save until Stop has been called, then keep working briefly, so a
		// Stop that does not wait returns before the save completes.
		for !stopCalled.Load() {
			runtime.Gosched()
		}
		for i := 0; i < 1000; i++ {
			runtime.Gosched()
		}
	}
	manager := startRefreshWithHeldSave(t, store)

	stopCalled.Store(true)
	manager.StopAutoRefresh()

	if !store.done.Load() {
		t.Fatal("StopAutoRefresh returned while the refresh save was still running")
	}
}

// StopAutoRefreshContext gives up waiting when its context ends, so a refresh
// that never finishes cannot hang shutdown.
func TestStopAutoRefreshContextReturnsWhenContextEnds(t *testing.T) {
	release := make(chan struct{})
	store := &refreshSaveStore{entered: make(chan struct{}, 1)}
	store.hold = func() { <-release }
	manager := startRefreshWithHeldSave(t, store)
	t.Cleanup(func() {
		close(release)
		manager.StopAutoRefresh()
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := manager.StopAutoRefreshContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("StopAutoRefreshContext() error = %v, want context.Canceled", err)
	}
	if store.done.Load() {
		t.Fatal("held save finished early; the test did not exercise the wait")
	}
}
