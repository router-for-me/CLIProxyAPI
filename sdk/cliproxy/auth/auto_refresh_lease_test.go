package auth

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type leaseTestSelector struct {
	stopped atomic.Bool
}

type leaseRefreshEvaluator struct{}

func (leaseRefreshEvaluator) ShouldRefresh(time.Time, *Auth) bool { return true }

func (s *leaseTestSelector) Pick(context.Context, string, string, cliproxyexecutor.Options, []*Auth) (*Auth, error) {
	return nil, nil
}

func (s *leaseTestSelector) Stop() {
	s.stopped.Store(true)
}

type blockingRefreshExecutor struct {
	started chan struct{}
	release chan struct{}
}

func (e *blockingRefreshExecutor) Identifier() string { return "codex" }

func (e *blockingRefreshExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *blockingRefreshExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e *blockingRefreshExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	select {
	case <-e.started:
	default:
		close(e.started)
	}
	<-e.release
	return auth, nil
}

func (e *blockingRefreshExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *blockingRefreshExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestAutoRefreshLeaseWaitsWithoutStoppingSelector(t *testing.T) {
	selector := &leaseTestSelector{}
	manager := NewManager(nil, selector, nil)
	executor := &blockingRefreshExecutor{started: make(chan struct{}), release: make(chan struct{})}
	manager.RegisterExecutor(executor)
	_, errRegister := manager.Register(t.Context(), &Auth{
		ID:       "codex-refresh-lease",
		Provider: "codex",
		Attributes: map[string]string{
			AttributeAuthKind: AuthKindOAuth,
		},
		Metadata: map[string]any{
			"access_token":  "expired-access",
			"refresh_token": "refresh-token",
			"expires_at":    time.Now().Add(-time.Minute).Format(time.RFC3339),
		},
		Runtime: leaseRefreshEvaluator{},
	})
	if errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	lease := manager.EnsureAutoRefresh(t.Context(), time.Millisecond)
	if lease == nil || !lease.owned {
		t.Fatal("expected an owned auto-refresh lease")
	}
	select {
	case <-executor.started:
	case <-time.After(2 * time.Second):
		t.Fatal("refresh worker did not start")
	}

	closed := make(chan error, 1)
	go func() { closed <- lease.Close(context.Background()) }()
	select {
	case errClose := <-closed:
		t.Fatalf("lease closed before refresh worker exited: %v", errClose)
	case <-time.After(50 * time.Millisecond):
	}
	close(executor.release)
	select {
	case errClose := <-closed:
		if errClose != nil {
			t.Fatalf("close lease: %v", errClose)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("lease did not wait for refresh worker shutdown")
	}
	if selector.stopped.Load() {
		t.Fatal("closing refresh lease stopped host selector")
	}
	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if manager.refreshLoop != nil || manager.refreshCancel != nil || manager.refreshDone != nil {
		t.Fatal("owned refresh loop remains installed after lease close")
	}
}

func TestAutoRefreshLeasePreservesExistingHostLoop(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	manager.StartAutoRefresh(t.Context(), time.Hour)
	manager.mu.RLock()
	hostLoop := manager.refreshLoop
	manager.mu.RUnlock()
	if hostLoop == nil {
		t.Fatal("host refresh loop was not installed")
	}

	lease := manager.EnsureAutoRefresh(t.Context(), time.Minute)
	if lease == nil || lease.owned {
		t.Fatal("expected a non-owning lease for the existing host loop")
	}
	if errClose := lease.Close(t.Context()); errClose != nil {
		t.Fatalf("close non-owning lease: %v", errClose)
	}
	manager.mu.RLock()
	current := manager.refreshLoop
	manager.mu.RUnlock()
	if current != hostLoop {
		t.Fatal("closing non-owning lease replaced or removed host loop")
	}
	manager.StopAutoRefresh()
}

func TestAutoRefreshLeaseReplacesExitedHostLoop(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	hostCtx, cancelHost := context.WithCancel(t.Context())
	manager.StartAutoRefresh(hostCtx, time.Hour)
	manager.mu.RLock()
	hostLoop := manager.refreshLoop
	hostDone := manager.refreshDone
	manager.mu.RUnlock()
	cancelHost()
	select {
	case <-hostDone:
	case <-time.After(2 * time.Second):
		t.Fatal("host refresh loop did not exit")
	}

	lease := manager.EnsureAutoRefresh(t.Context(), time.Hour)
	if lease == nil || !lease.owned {
		t.Fatal("expected an owned lease after host loop exit")
	}
	if lease.loop == hostLoop {
		t.Fatal("exited host loop was reused")
	}
	if errClose := lease.Close(t.Context()); errClose != nil {
		t.Fatalf("close replacement lease: %v", errClose)
	}
}
