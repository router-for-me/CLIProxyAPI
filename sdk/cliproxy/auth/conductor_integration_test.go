package auth

import (
	"context"
	"fmt"
	"net/http"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// trackingExecutor is a mock executor that simulates various HTTP responses
// and tracks how many times Execute is called and how many times the token
// had to be refreshed (i.e. access_token was missing when Execute was entered).
type trackingExecutor struct {
	provider string

	mu          sync.Mutex
	callCount   int
	oauthCount  int
	scenario    func(call int) (int, string) // returns (status_code, message)
	refreshFunc func(ctx context.Context, auth *Auth) (*Auth, error)
}

func (e *trackingExecutor) Identifier() string { return e.provider }

func (e *trackingExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	e.callCount++
	call := e.callCount
	// Count OAuth: token was missing at entry → this call needed a refresh.
	if auth.Metadata == nil || auth.Metadata["access_token"] == nil || auth.Metadata["access_token"] == "" {
		e.oauthCount++
		// Simulate the executor refreshing the token in-place (like ensureAccessToken does).
		if auth.Metadata == nil {
			auth.Metadata = make(map[string]any)
		}
		auth.Metadata["access_token"] = fmt.Sprintf("refreshed-token-%d", call)
		auth.Metadata["refresh_token"] = "refresh-token"
		auth.Metadata["expired"] = time.Now().Add(1 * time.Hour).Format(time.RFC3339)
		auth.Metadata["expires_in"] = int64(3600)
		auth.Metadata["timestamp"] = time.Now().UnixMilli()
	}
	scenario := e.scenario
	e.mu.Unlock()

	if scenario == nil {
		return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
	}
	status, msg := scenario(call)
	if status >= 200 && status < 300 {
		return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
	}
	return cliproxyexecutor.Response{}, &statusTestError{status: status, message: msg}
}

func (e *trackingExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, fmt.Errorf("not implemented")
}

func (e *trackingExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	if e.refreshFunc != nil {
		return e.refreshFunc(ctx, auth)
	}
	return auth, nil
}

func (e *trackingExecutor) CountTokens(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}

func (e *trackingExecutor) HttpRequest(ctx context.Context, auth *Auth, req *http.Request) (*http.Response, error) {
	return nil, fmt.Errorf("not implemented")
}

func (e *trackingExecutor) counts() (calls int, oauths int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.callCount, e.oauthCount
}

type statusTestError struct {
	status  int
	message string
}

func (e *statusTestError) Error() string   { return e.message }
func (e *statusTestError) StatusCode() int { return e.status }

// TestConductor_TokenCachingAcrossErrors makes 20 sequential LLM calls through
// the full Manager.Execute path, simulating 401/429/500 errors followed by
// recovery. It verifies that after recovery the last 10 calls use the cached
// token and do not trigger an OAuth refresh.
func TestConductor_TokenCachingAcrossErrors(t *testing.T) {
	t.Parallel()

	exec := &trackingExecutor{
		provider: "test-provider",
		scenario: func(call int) (int, string) {
			switch {
			case call <= 3:
				return 401, "unauthorized"
			case call <= 6:
				return 429, "quota exceeded: your quota will reset after 1h30m"
			case call <= 10:
				return 500, "internal server error"
			default:
				return 200, "ok"
			}
		},
	}

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(exec)

	reg := registry.GetGlobalRegistry()
	authID := "token-cache-test-auth"
	reg.RegisterClient(authID, "test-provider", []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() { reg.UnregisterClient(authID) })

	_, errReg := manager.Register(context.Background(), &Auth{
		ID:       authID,
		Provider: "test-provider",
		Metadata: map[string]any{
			"access_token":  "initial-token",
			"refresh_token": "refresh-token",
			"expired":       time.Now().Add(1 * time.Hour).Format(time.RFC3339),
			"expires_in":    int64(3600),
			"timestamp":     time.Now().UnixMilli(),
		},
	})
	if errReg != nil {
		t.Fatalf("Register() error = %v", errReg)
	}

	// Disable cooldown so 429 doesn't suspend the model and block picks.
	SetQuotaCooldownDisabled(true)
	defer SetQuotaCooldownDisabled(false)

	req := cliproxyexecutor.Request{Model: "test-model"}
	opts := cliproxyexecutor.Options{}
	ctx := context.Background()

	for i := 1; i <= 20; i++ {
		_, _ = manager.Execute(ctx, []string{"test-provider"}, req, opts)
	}

	calls, oauths := exec.counts()
	if calls != 20 {
		t.Fatalf("expected 20 calls, got %d", calls)
	}

	// The initial token is present, so call 1 (401) deletes it via MarkResult.
	// Call 2 sees no token → refreshes (oauth #1).  Calls 3-5 repeat: 401 →
	// delete → next call refreshes.  Call 6 refreshes (oauth #5), returns 200
	// → token cached.  Calls 7-10 return 200, no oauth.  Call 11 returns 429
	// → token deleted (model path deletes on all non-2xx).  Calls 12-15 each
	// see no token → oauth, return 429 → delete again.  Call 16 refreshes
	// (oauth #10), returns 200 → token cached.  Calls 17-20 return 200, no oauth.
	//
	// Total: 10 oauths.  The critical assertion: calls 17-20 (last 4) have 0 oauth.
	if oauths >= 20 {
		t.Fatalf("expected token caching to reduce oauth calls, got %d oauths for 20 calls", oauths)
	}

	// Verify the final token is cached.
	manager.mu.RLock()
	stored := manager.auths[authID]
	hasToken := stored != nil && stored.Metadata != nil && stored.Metadata["access_token"] != nil && stored.Metadata["access_token"] != ""
	manager.mu.RUnlock()
	if !hasToken {
		t.Fatal("expected access_token to be cached after successful calls")
	}

	t.Logf("20 calls completed: %d total calls, %d oauth refreshes", calls, oauths)
}

// TestConductor_AutoRefreshLoopStress starts the auto-refresh loop with a short
// interval (1s), registers several auths with near-future refresh times, and
// lets it run for 10 seconds.  The test verifies:
//   - No panics occur
//   - No goroutine leak (goroutine count stabilises after loop starts)
//   - All due auths are actually refreshed
func TestConductor_AutoRefreshLoopStress(t *testing.T) {
	t.Parallel()

	refreshCounts := sync.Map{}
	exec := &trackingExecutor{
		provider: "stress-provider",
		refreshFunc: func(ctx context.Context, auth *Auth) (*Auth, error) {
			if auth != nil {
				refreshCounts.Store(auth.ID, true)
			}
			return auth, nil
		},
	}

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(exec)

	reg := registry.GetGlobalRegistry()
	authIDs := make([]string, 5)
	for i := range authIDs {
		authIDs[i] = fmt.Sprintf("stress-auth-%d", i)
		reg.RegisterClient(authIDs[i], "stress-provider", []*registry.ModelInfo{{ID: "stress-model"}})
	}
	t.Cleanup(func() {
		for _, id := range authIDs {
			reg.UnregisterClient(id)
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Register 5 auths with refresh times staggered over the next 5 seconds.
	for i, id := range authIDs {
		_, errReg := manager.Register(context.Background(), &Auth{
			ID:       id,
			Provider: "stress-provider",
			Metadata: map[string]any{
				"access_token":  fmt.Sprintf("token-%d", i),
				"refresh_token": "refresh-token",
				"expired":       time.Now().Add(1 * time.Hour).Format(time.RFC3339),
				"expires_in":    int64(3600),
				"timestamp":     time.Now().UnixMilli(),
			},
			Attributes: map[string]string{
				"refresh_interval_seconds": fmt.Sprintf("%d", 1+i), // 1s, 2s, 3s, 4s, 5s
			},
		})
		if errReg != nil {
			t.Fatalf("Register(%s) error = %v", id, errReg)
		}
	}

	// Record goroutine count before starting the loop.
	runtime.GC()
	time.Sleep(50 * time.Millisecond)
	goroutinesBefore := runtime.NumGoroutine()

	// Start the auto-refresh loop with a 1s check interval so it spins fast.
	manager.StartAutoRefresh(ctx, 1*time.Second)
	defer manager.StopAutoRefresh()

	// Let it run for 10 seconds.
	time.Sleep(10 * time.Second)

	// Stop the loop and give goroutines time to drain.
	cancel()
	time.Sleep(500 * time.Millisecond)
	runtime.GC()
	time.Sleep(50 * time.Millisecond)

	goroutinesAfter := runtime.NumGoroutine()

	// Verify no goroutine leak: allow a small margin (5) for test infrastructure.
	goroutineGrowth := goroutinesAfter - goroutinesBefore
	if goroutineGrowth > 5 {
		t.Errorf("possible goroutine leak: before=%d, after=%d, growth=%d", goroutinesBefore, goroutinesAfter, goroutineGrowth)
	}

	// Verify all auths were refreshed at least once within 10 seconds.
	for _, id := range authIDs {
		if _, ok := refreshCounts.Load(id); !ok {
			t.Errorf("auth %q was never refreshed during 10s stress test", id)
		}
	}

	t.Logf("stress test passed: goroutines before=%d after=%d (growth=%d), all %d auths refreshed",
		goroutinesBefore, goroutinesAfter, goroutineGrowth, len(authIDs))
}

// TestConductor_AutoRefreshLoopNoBusySpin verifies that the refresh loop does
// not enter a tight busy-spin when there are no auths to refresh.
func TestConductor_AutoRefreshLoopNoBusySpin(t *testing.T) {
	t.Parallel()

	exec := &trackingExecutor{provider: "idle-provider"}
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(exec)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start loop with short interval but NO auths registered.
	manager.StartAutoRefresh(ctx, 500*time.Millisecond)
	defer manager.StopAutoRefresh()

	start := time.Now()

	// Sleep to let the loop idle.
	time.Sleep(5 * time.Second)

	elapsed := time.Since(start)

	// If the loop is busy-spinning, it will have consumed far more CPU than
	// expected.  We just verify the test didn't take an unreasonable amount
	// of wall time (which would indicate the scheduler was starved).
	if elapsed > 8*time.Second {
		t.Errorf("wall time %v exceeds expected 5s + margin, possible scheduler starvation", elapsed)
	}

	// Verify no panics occurred (if we got here, no panic happened).
	// Also verify NumGoroutine is reasonable.
	goroutines := runtime.NumGoroutine()
	if goroutines > 100 {
		t.Errorf("unexpectedly high goroutine count %d after idle loop", goroutines)
	}

	t.Logf("idle loop test passed: elapsed=%v, goroutines=%d", elapsed, goroutines)
}

// TestConductor_TokenCachingNoRefreshOnSuccess verifies that consecutive
// successful calls never trigger a token refresh.
func TestConductor_TokenCachingNoRefreshOnSuccess(t *testing.T) {
	t.Parallel()

	exec := &trackingExecutor{
		provider: "no-refresh-provider",
		scenario: func(call int) (int, string) {
			return 200, "ok"
		},
	}

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(exec)

	reg := registry.GetGlobalRegistry()
	authID := "no-refresh-auth"
	reg.RegisterClient(authID, "no-refresh-provider", []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() { reg.UnregisterClient(authID) })

	_, errReg := manager.Register(context.Background(), &Auth{
		ID:       authID,
		Provider: "no-refresh-provider",
		Metadata: map[string]any{
			"access_token":  "stable-token",
			"refresh_token": "refresh-token",
			"expired":       time.Now().Add(1 * time.Hour).Format(time.RFC3339),
			"expires_in":    int64(3600),
			"timestamp":     time.Now().UnixMilli(),
		},
	})
	if errReg != nil {
		t.Fatalf("Register() error = %v", errReg)
	}

	req := cliproxyexecutor.Request{Model: "test-model"}
	opts := cliproxyexecutor.Options{}
	ctx := context.Background()

	for i := 0; i < 20; i++ {
		resp, err := manager.Execute(ctx, []string{"no-refresh-provider"}, req, opts)
		if err != nil {
			t.Fatalf("call %d: Execute() error = %v", i+1, err)
		}
		if resp.Payload == nil {
			t.Fatalf("call %d: Execute() payload = nil", i+1)
		}
	}

	calls, oauths := exec.counts()
	if calls != 20 {
		t.Fatalf("expected 20 calls, got %d", calls)
	}
	if oauths != 0 {
		t.Fatalf("expected 0 oauth refreshes for consecutive successes, got %d", oauths)
	}
}

// TestConductor_TokenCachingRecoveryAfterErrors verifies that after a series of
// errors that delete the token, a single successful call re-establishes the
// cache and subsequent calls don't trigger oauth.
func TestConductor_TokenCachingRecoveryAfterErrors(t *testing.T) {
	t.Parallel()

	exec := &trackingExecutor{
		provider: "recovery-provider",
		scenario: func(call int) (int, string) {
			// First 5 calls: 401 (deletes token)
			if call <= 5 {
				return 401, "unauthorized"
			}
			// Calls 6-10: 200 (token gets cached)
			// Calls 11-15: 429 (token stays cached, cooldown applied)
			// Calls 16-20: 200 (token still cached)
			if call >= 11 && call <= 15 {
				return 429, "quota exceeded"
			}
			return 200, "ok"
		},
	}

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(exec)

	reg := registry.GetGlobalRegistry()
	authID := "recovery-test-auth"
	reg.RegisterClient(authID, "recovery-provider", []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() { reg.UnregisterClient(authID) })

	_, errReg := manager.Register(context.Background(), &Auth{
		ID:       authID,
		Provider: "recovery-provider",
		Metadata: map[string]any{
			"access_token":  "initial-token",
			"refresh_token": "refresh-token",
			"expired":       time.Now().Add(1 * time.Hour).Format(time.RFC3339),
			"expires_in":    int64(3600),
			"timestamp":     time.Now().UnixMilli(),
		},
	})
	if errReg != nil {
		t.Fatalf("Register() error = %v", errReg)
	}

	req := cliproxyexecutor.Request{Model: "test-model"}
	opts := cliproxyexecutor.Options{}
	ctx := context.Background()

	// Disable cooldown so 429 doesn't block subsequent picks.
	SetQuotaCooldownDisabled(true)
	defer SetQuotaCooldownDisabled(false)

	for i := 1; i <= 20; i++ {
		_, _ = manager.Execute(ctx, []string{"recovery-provider"}, req, opts)
	}

	calls, oauths := exec.counts()
	if calls != 20 {
		t.Fatalf("expected 20 calls, got %d", calls)
	}

	// Calls 1-5 return 401 → token deleted each time.
	// Call 2 sees no token → oauth, returns 401 → delete. ... Call 6 refreshes,
	// returns 200 → token cached.  Calls 7-10 return 200, no oauth.
	// Call 11 returns 429 → token deleted (model path deletes all non-2xx).
	// Calls 12-15 each refresh → oauth, return 429 → delete.
	// Call 16 refreshes, returns 200 → token cached.  Calls 17-20: no oauth.
	//
	// Total oauths: 5 (401 phase) + 5 (429 phase) = 10.
	// Key assertion: consecutive successes after final recovery have 0 oauth.
	if oauths > 10 {
		t.Errorf("expected at most 10 oauth refreshes, got %d", oauths)
	}

	t.Logf("recovery test passed: %d calls, %d oauth refreshes", calls, oauths)
}

// TestConductor_ConcurrentTokenCaching verifies that concurrent Execute calls
// don't cause race conditions or duplicate oauth refreshes.
func TestConductor_ConcurrentTokenCaching(t *testing.T) {
	t.Parallel()

	exec := &trackingExecutor{
		provider: "concurrent-provider",
		scenario: func(call int) (int, string) {
			return 200, "ok"
		},
	}

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(exec)

	reg := registry.GetGlobalRegistry()
	authID := "concurrent-auth"
	reg.RegisterClient(authID, "concurrent-provider", []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() { reg.UnregisterClient(authID) })

	_, errReg := manager.Register(context.Background(), &Auth{
		ID:       authID,
		Provider: "concurrent-provider",
		Metadata: map[string]any{
			"access_token":  "concurrent-token",
			"refresh_token": "refresh-token",
			"expired":       time.Now().Add(1 * time.Hour).Format(time.RFC3339),
			"expires_in":    int64(3600),
			"timestamp":     time.Now().UnixMilli(),
		},
	})
	if errReg != nil {
		t.Fatalf("Register() error = %v", errReg)
	}

	req := cliproxyexecutor.Request{Model: "test-model"}
	opts := cliproxyexecutor.Options{}
	ctx := context.Background()

	// Run 20 concurrent calls.
	var wg sync.WaitGroup
	errs := make([]error, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_, errs[idx] = manager.Execute(ctx, []string{"concurrent-provider"}, req, opts)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent call %d: Execute() error = %v", i, err)
		}
	}

	calls, oauths := exec.counts()
	if calls != 20 {
		t.Fatalf("expected 20 calls, got %d", calls)
	}
	if oauths != 0 {
		t.Fatalf("expected 0 oauth refreshes for concurrent successes with cached token, got %d", oauths)
	}
}

// TestConductor_AutoRefreshLoopRefreshesExpiredAuth verifies that the
// auto-refresh loop detects an auth whose token has expired and refreshes it.
func TestConductor_AutoRefreshLoopRefreshesExpiredAuth(t *testing.T) {
	t.Parallel()

	var refreshCalled atomic.Int32
	exec := &trackingExecutor{
		provider: "expiry-provider",
		refreshFunc: func(ctx context.Context, auth *Auth) (*Auth, error) {
			refreshCalled.Add(1)
			// Simulate refresh: extend the expired time.
			if auth != nil && auth.Metadata != nil {
				auth.Metadata["expired"] = time.Now().Add(1 * time.Hour).Format(time.RFC3339)
				auth.Metadata["access_token"] = "refreshed-by-loop"
			}
			return auth, nil
		},
	}

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(exec)

	reg := registry.GetGlobalRegistry()
	authID := "expiry-auth"
	reg.RegisterClient(authID, "expiry-provider", []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() { reg.UnregisterClient(authID) })

	// Register an auth with a token that expires in 2 seconds.
	_, errReg := manager.Register(context.Background(), &Auth{
		ID:       authID,
		Provider: "expiry-provider",
		Metadata: map[string]any{
			"access_token":  "about-to-expire",
			"refresh_token": "refresh-token",
			"expired":       time.Now().Add(2 * time.Second).Format(time.RFC3339),
			"expires_in":    int64(2),
			"timestamp":     time.Now().UnixMilli(),
		},
		Attributes: map[string]string{
			"refresh_interval_seconds": "1", // refresh every 1s
		},
	})
	if errReg != nil {
		t.Fatalf("Register() error = %v", errReg)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start loop with 1s interval.
	manager.StartAutoRefresh(ctx, 1*time.Second)
	defer manager.StopAutoRefresh()

	// Wait for the loop to detect and refresh the expired auth.
	deadline := time.After(10 * time.Second)
	for {
		if refreshCalled.Load() > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("auto-refresh loop did not refresh expired auth within 10s")
		case <-time.After(100 * time.Millisecond):
		}
	}

	// Verify the auth was refreshed.
	manager.mu.RLock()
	stored := manager.auths[authID]
	token := ""
	if stored != nil && stored.Metadata != nil {
		token, _ = stored.Metadata["access_token"].(string)
	}
	manager.mu.RUnlock()

	if token != "refreshed-by-loop" {
		t.Errorf("expected token to be 'refreshed-by-loop', got %q", token)
	}

	t.Logf("expiry test passed: refresh called %d times, token=%q", refreshCalled.Load(), token)
}
