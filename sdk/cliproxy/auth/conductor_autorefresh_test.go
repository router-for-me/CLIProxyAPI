package auth

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

type autoRefreshTestExecutor struct {
	provider string
	mu       sync.Mutex
	ids      []string
}

func (e *autoRefreshTestExecutor) Identifier() string { return e.provider }

func (e *autoRefreshTestExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *autoRefreshTestExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, nil
}

func (e *autoRefreshTestExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	e.mu.Lock()
	e.ids = append(e.ids, auth.ID)
	e.mu.Unlock()
	return auth, nil
}

func (e *autoRefreshTestExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *autoRefreshTestExecutor) HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	return nil, nil
}

func (e *autoRefreshTestExecutor) refreshedIDs() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]string, len(e.ids))
	copy(out, e.ids)
	return out
}

func TestManager_StartAutoRefresh_DisabledByDefault(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	executor := &autoRefreshTestExecutor{provider: "test"}
	manager.RegisterExecutor(executor)
	manager.SetConfig(&internalconfig.Config{})

	auth := &Auth{
		ID:       "startup-default",
		Provider: "test",
		Metadata: map[string]any{"refresh_interval_seconds": 3600},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if started := manager.StartAutoRefresh(ctx, time.Hour); started {
		t.Fatal("expected auto refresh to stay disabled by default")
	}
	t.Cleanup(manager.StopAutoRefresh)

	time.Sleep(100 * time.Millisecond)
	if got := len(executor.refreshedIDs()); got != 0 {
		t.Fatalf("startup refresh calls = %d, want 0", got)
	}
}

func TestManager_StartAutoRefresh_EnabledImmediateByDefault(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	executor := &autoRefreshTestExecutor{provider: "test"}
	manager.RegisterExecutor(executor)

	enabled := true
	manager.SetConfig(&internalconfig.Config{
		SDKConfig: internalconfig.SDKConfig{
			OAuthRefresh: internalconfig.OAuthRefreshConfig{
				Enabled: &enabled,
			},
		},
	})

	auth := &Auth{
		ID:       "startup-disabled",
		Provider: "test",
		Metadata: map[string]any{"refresh_interval_seconds": 3600},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if started := manager.StartAutoRefresh(ctx, time.Hour); !started {
		t.Fatal("expected auto refresh to start when enabled")
	}
	t.Cleanup(manager.StopAutoRefresh)

	waitForRefreshCalls(t, executor, 1, time.Second)
}

func TestManager_StartAutoRefresh_EnabledCanSkipStartupCheck(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	executor := &autoRefreshTestExecutor{provider: "test"}
	manager.RegisterExecutor(executor)

	enabled := true
	disabled := false
	manager.SetConfig(&internalconfig.Config{
		SDKConfig: internalconfig.SDKConfig{
			OAuthRefresh: internalconfig.OAuthRefreshConfig{
				Enabled:   &enabled,
				OnStartup: &disabled,
			},
		},
	})

	auth := &Auth{
		ID:       "startup-disabled",
		Provider: "test",
		Metadata: map[string]any{"refresh_interval_seconds": 3600},
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if started := manager.StartAutoRefresh(ctx, time.Hour); !started {
		t.Fatal("expected auto refresh to start when enabled")
	}
	t.Cleanup(manager.StopAutoRefresh)

	time.Sleep(100 * time.Millisecond)
	if got := len(executor.refreshedIDs()); got != 0 {
		t.Fatalf("startup refresh calls = %d, want 0", got)
	}
}

func TestManager_CheckRefreshes_HonorsBatchSize(t *testing.T) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	executor := &autoRefreshTestExecutor{provider: "test"}
	manager.RegisterExecutor(executor)
	manager.SetConfig(&internalconfig.Config{
		SDKConfig: internalconfig.SDKConfig{
			OAuthRefresh: internalconfig.OAuthRefreshConfig{
				BatchSize: 2,
			},
		},
	})

	ctx := context.Background()
	for i := 0; i < 5; i++ {
		auth := &Auth{
			ID:       "batch-auth-" + strconv.Itoa(i),
			Provider: "test",
			Metadata: map[string]any{"refresh_interval_seconds": 3600},
		}
		if _, err := manager.Register(ctx, auth); err != nil {
			t.Fatalf("register auth %d: %v", i, err)
		}
	}

	manager.checkRefreshes(ctx)
	waitForRefreshCalls(t, executor, 2, time.Second)

	manager.checkRefreshes(ctx)
	waitForRefreshCalls(t, executor, 4, time.Second)

	manager.checkRefreshes(ctx)
	waitForRefreshCalls(t, executor, 5, time.Second)

	seen := make(map[string]int)
	for _, id := range executor.refreshedIDs() {
		seen[id]++
	}
	if len(seen) != 5 {
		t.Fatalf("refreshed unique auths = %d, want 5; got=%v", len(seen), seen)
	}
	for id, count := range seen {
		if count != 1 {
			t.Fatalf("auth %s refreshed %d times, want 1", id, count)
		}
	}
}

func waitForRefreshCalls(t *testing.T, executor *autoRefreshTestExecutor, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if got := len(executor.refreshedIDs()); got >= want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %d refresh calls; got %d", want, len(executor.refreshedIDs()))
}
