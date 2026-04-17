package auth

import (
	"context"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

type singleflightRefreshTestExecutor struct {
	provider string
	started  chan string
	release  chan struct{}
	calls    atomic.Int32
}

func (e *singleflightRefreshTestExecutor) Identifier() string { return e.provider }

func (e *singleflightRefreshTestExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *singleflightRefreshTestExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e *singleflightRefreshTestExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	e.calls.Add(1)
	select {
	case e.started <- auth.ID:
	default:
	}
	<-e.release
	return auth, nil
}

func (e *singleflightRefreshTestExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *singleflightRefreshTestExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, nil
}

func TestManager_RefreshAuth_SingleflightByAuthID(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	executor := &singleflightRefreshTestExecutor{
		provider: "test",
		started:  make(chan string, 8),
		release:  make(chan struct{}),
	}
	manager.RegisterExecutor(executor)

	auth := &Auth{ID: "singleflight-auth", Provider: "test"}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			manager.refreshAuth(context.Background(), auth.ID)
		}()
	}

	waitForRefreshStart(t, executor.started, "singleflight-auth")
	time.Sleep(20 * time.Millisecond)
	close(executor.release)
	wg.Wait()

	if got := executor.calls.Load(); got != 1 {
		t.Fatalf("refresh calls = %d, want 1", got)
	}
}

func TestManager_RefreshAuth_DifferentAuthIDsRemainIndependent(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	executor := &singleflightRefreshTestExecutor{
		provider: "test",
		started:  make(chan string, 2),
		release:  make(chan struct{}),
	}
	manager.RegisterExecutor(executor)

	for _, authID := range []string{"auth-a", "auth-b"} {
		if _, err := manager.Register(context.Background(), &Auth{ID: authID, Provider: "test"}); err != nil {
			t.Fatalf("register %s: %v", authID, err)
		}
	}

	var wg sync.WaitGroup
	for _, authID := range []string{"auth-a", "auth-b"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			manager.refreshAuth(context.Background(), id)
		}(authID)
	}

	waitForRefreshStart(t, executor.started, "")
	waitForRefreshStart(t, executor.started, "")
	close(executor.release)
	wg.Wait()

	if got := executor.calls.Load(); got != 2 {
		t.Fatalf("refresh calls = %d, want 2", got)
	}
}

func waitForRefreshStart(t *testing.T, started <-chan string, want string) {
	t.Helper()

	select {
	case got := <-started:
		if want != "" && got != want {
			t.Fatalf("refresh started for %q, want %q", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for refresh start")
	}
}
