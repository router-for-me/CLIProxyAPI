package auth

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

const issue6199RefreshProvider = "issue6199-refresh"

func newIssue6199RefreshLoop(executor ProviderExecutor) (*Manager, *authAutoRefreshLoop) {
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(executor)
	loop := newAuthAutoRefreshLoop(manager, refreshCheckInterval, 2)
	manager.refreshLoop = loop
	return manager, loop
}

func registerIssue6199ExpiredAuth(t *testing.T, manager *Manager, id string, now time.Time) {
	t.Helper()
	auth := &Auth{
		ID:              id,
		Provider:        issue6199RefreshProvider,
		Status:          StatusActive,
		LastRefreshedAt: now.Add(-time.Hour),
		Metadata: map[string]any{
			"access_token":             "expired-access",
			"refresh_token":            "refresh-" + id,
			"expires_at":               now.Add(-time.Hour).Format(time.RFC3339),
			"refresh_interval_seconds": 1,
		},
	}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register expired auth: %v", errRegister)
	}
}

func TestIssue6199AutoRefreshDoesNotRequeueQueuedAuth(t *testing.T) {
	executor := &countingRefreshExecutor{id: issue6199RefreshProvider}
	manager, loop := newIssue6199RefreshLoop(executor)
	now := time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)
	registerIssue6199ExpiredAuth(t, manager, "queued", now)
	loop.rebuild(now)
	loop.handleDue(context.Background(), now)
	if got := len(loop.jobs); got != 1 {
		t.Fatalf("initial queued jobs = %d, want 1", got)
	}

	// Advance the pending window without allowing a worker to consume the job.
	for i := 0; i < 3; i++ {
		loop.applyDirty(now)
		now = now.Add(refreshPendingBackoff)
		loop.handleDue(context.Background(), now)
	}
	if got := len(loop.jobs); got != 1 {
		t.Fatalf("queued auth has %d jobs across pending windows, want exactly 1", got)
	}
}

type issue6199BlockedRefreshExecutor struct {
	countingRefreshExecutor
	blockedStarted   chan struct{}
	healthyRefreshed chan struct{}
	release          chan struct{}
	blockedOnce      sync.Once
	healthyOnce      sync.Once
}

func (e *issue6199BlockedRefreshExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	if auth.ID == "blocked" {
		e.blockedOnce.Do(func() { close(e.blockedStarted) })
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-e.release:
		}
	}
	updated, errRefresh := e.countingRefreshExecutor.Refresh(ctx, auth)
	if auth.ID == "healthy" {
		e.healthyOnce.Do(func() { close(e.healthyRefreshed) })
	}
	return updated, errRefresh
}

func TestIssue6199AutoRefreshRunningAuthDoesNotStarveHealthyAuth(t *testing.T) {
	ctx, cancelCtx := context.WithCancel(context.Background())
	executor := &issue6199BlockedRefreshExecutor{
		countingRefreshExecutor: countingRefreshExecutor{id: issue6199RefreshProvider},
		blockedStarted:          make(chan struct{}),
		healthyRefreshed:        make(chan struct{}),
		release:                 make(chan struct{}),
	}
	manager, loop := newIssue6199RefreshLoop(executor)
	var workers sync.WaitGroup
	defer func() {
		cancelCtx()
		close(executor.release)
		done := make(chan struct{})
		go func() {
			workers.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("refresh workers did not exit after releasing the blocked refresh")
		}
	}()
	startWorker := func() {
		workers.Add(1)
		go func() {
			defer workers.Done()
			loop.worker(ctx)
		}()
	}

	now := time.Date(2026, 4, 12, 0, 0, 0, 0, time.UTC)
	registerIssue6199ExpiredAuth(t, manager, "blocked", now)
	loop.rebuild(now)
	loop.handleDue(ctx, now)
	startWorker()
	select {
	case <-executor.blockedStarted:
	case <-time.After(3 * time.Second):
		t.Fatal("initial refresh did not enter the executor")
	}

	// The refresh remains blocked while the scheduler observes three pending windows.
	for i := 0; i < 3; i++ {
		loop.applyDirty(now)
		now = now.Add(refreshPendingBackoff)
		loop.handleDue(ctx, now)
	}
	if got := len(loop.jobs); got != 0 {
		t.Errorf("running auth queued %d duplicate jobs, want 0", got)
	}

	registerIssue6199ExpiredAuth(t, manager, "healthy", now)
	loop.applyDirty(now)
	loop.handleDue(ctx, now)
	// Start the spare worker only after the FIFO queue is fully arranged.
	startWorker()
	select {
	case <-executor.healthyRefreshed:
	case <-time.After(3 * time.Second):
		t.Error("healthy auth could not refresh while another auth was blocked; spare worker was consumed by a duplicate")
	}
}

func TestIssue6199AutoRefreshFullQueueDoesNotBlockScheduler(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		executor := &countingRefreshExecutor{id: issue6199RefreshProvider}
		manager, loop := newIssue6199RefreshLoop(executor)
		ctx, cancelCtx := context.WithCancel(context.Background())
		defer cancelCtx()
		now := time.Now()
		var overflowID string
		for i := 0; i <= cap(loop.jobs); i++ {
			id := fmt.Sprintf("full-queue-%02d", i)
			registerIssue6199ExpiredAuth(t, manager, id, now)
			// Explicit ordering makes the last credential the overflow candidate.
			loop.upsert(id, now.Add(time.Duration(i)*time.Nanosecond))
			overflowID = id
		}
		now = now.Add(time.Duration(cap(loop.jobs)) * time.Nanosecond)
		done := make(chan struct{})
		go func() {
			loop.handleDue(ctx, now)
			close(done)
		}()
		synctest.Wait()

		select {
		case <-done:
		default:
			t.Errorf("handleDue blocked with %d/%d jobs queued, want scheduler to return without a consumer", len(loop.jobs), cap(loop.jobs))
			cancelCtx()
			synctest.Wait()
		}
		if got := len(loop.jobs); got != cap(loop.jobs) {
			t.Fatalf("queued jobs = %d, want capacity %d", got, cap(loop.jobs))
		}
		loop.mu.Lock()
		_, scheduled := loop.index[overflowID]
		loop.mu.Unlock()
		if !scheduled {
			t.Fatalf("overflow auth %q lost its retry schedule", overflowID)
		}
	})
}

func TestIssue6199AutoRefreshFullQueueRetriesAfterCapacityReturns(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		executor := &countingRefreshExecutor{id: issue6199RefreshProvider}
		manager, loop := newIssue6199RefreshLoop(executor)
		ctx, cancelCtx := context.WithCancel(context.Background())
		defer cancelCtx()
		now := time.Now()
		for i := 0; i <= cap(loop.jobs); i++ {
			id := fmt.Sprintf("capacity-%02d", i)
			registerIssue6199ExpiredAuth(t, manager, id, now)
			loop.upsert(id, now.Add(time.Duration(i)*time.Nanosecond))
		}
		now = now.Add(time.Duration(cap(loop.jobs)) * time.Nanosecond)
		go loop.handleDue(ctx, now)
		synctest.Wait()
		// Also release an unfixed blocking dispatcher so the regression can finish.
		cancelCtx()
		synctest.Wait()
		for len(loop.jobs) > 0 {
			<-loop.jobs
		}
		now = now.Add(refreshPendingBackoff + loop.interval)
		loop.handleDue(context.Background(), now)
		if got := len(loop.jobs); got != 1 {
			t.Fatalf("overflow retry jobs after capacity returned = %d, want 1", got)
		}
	})
}

type issue6199UncancelableRefreshExecutor struct {
	issue6199BlockedRefreshExecutor
}

func (e *issue6199UncancelableRefreshExecutor) Refresh(ctx context.Context, auth *Auth) (*Auth, error) {
	e.blockedOnce.Do(func() { close(e.blockedStarted) })
	<-e.release
	return e.countingRefreshExecutor.Refresh(ctx, auth)
}

func TestIssue6199AutoRefreshRestartDoesNotDuplicateRunningJob(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		executor := &issue6199UncancelableRefreshExecutor{
			issue6199BlockedRefreshExecutor: issue6199BlockedRefreshExecutor{
				countingRefreshExecutor: countingRefreshExecutor{id: issue6199RefreshProvider},
				blockedStarted:          make(chan struct{}),
				release:                 make(chan struct{}),
			},
		}
		manager, oldLoop := newIssue6199RefreshLoop(executor)
		ctx, cancelCtx := context.WithCancel(context.Background())
		defer cancelCtx()
		var releaseOnce sync.Once
		defer releaseOnce.Do(func() { close(executor.release) })
		now := time.Now()
		registerIssue6199ExpiredAuth(t, manager, "restart-running", now)
		oldLoop.rebuild(now)
		oldLoop.handleDue(ctx, now)
		go oldLoop.worker(ctx)
		<-executor.blockedStarted
		cancelCtx()

		newLoop := newAuthAutoRefreshLoop(manager, oldLoop.interval, 2)
		manager.mu.Lock()
		manager.refreshLoop = newLoop
		manager.mu.Unlock()
		now = now.Add(2 * refreshPendingBackoff)
		newLoop.rebuild(now)
		newLoop.handleDue(context.Background(), now)
		if got := len(newLoop.jobs); got != 0 {
			t.Errorf("restarted loop queued %d duplicate jobs while the old refresh still ran, want 0", got)
		}

		releaseOnce.Do(func() { close(executor.release) })
		synctest.Wait()
		newLoop.applyDirty(now)
		newLoop.handleDue(context.Background(), now)
		if got := len(newLoop.jobs); got != 1 {
			t.Fatalf("restarted loop jobs after old refresh completed = %d, want 1", got)
		}
	})
}

func TestIssue6199AutoRefreshCanceledLoopReleasesQueuedJob(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		executor := &countingRefreshExecutor{id: issue6199RefreshProvider}
		manager, oldLoop := newIssue6199RefreshLoop(executor)
		now := time.Now()
		registerIssue6199ExpiredAuth(t, manager, "restart-queued", now)
		oldLoop.rebuild(now)
		oldLoop.handleDue(context.Background(), now)
		ctx, cancelCtx := context.WithCancel(context.Background())
		cancelCtx()
		oldLoop.run(ctx)
		synctest.Wait()
		if got := executor.refreshCalls.Load(); got != 0 {
			t.Errorf("canceled loop executed %d queued refreshes, want 0", got)
		}

		newLoop := newAuthAutoRefreshLoop(manager, oldLoop.interval, 2)
		manager.mu.Lock()
		manager.refreshLoop = newLoop
		manager.mu.Unlock()
		now = now.Add(oldLoop.interval)
		newLoop.rebuild(now)
		newLoop.handleDue(context.Background(), now)
		if got := len(newLoop.jobs); got != 1 {
			t.Fatalf("jobs after queued refresh cancellation and restart = %d, want 1", got)
		}
	})
}

func TestIssue6199AutoRefreshQueuedJobDoesNotRefreshNewRegistration(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		executor := &countingRefreshExecutor{id: issue6199RefreshProvider}
		manager, loop := newIssue6199RefreshLoop(executor)
		now := time.Now()
		registerIssue6199ExpiredAuth(t, manager, "replacement", now)
		loop.rebuild(now)
		loop.handleDue(context.Background(), now)
		manager.Remove(context.Background(), "replacement")
		registerIssue6199ExpiredAuth(t, manager, "replacement", now)
		ctx, cancelCtx := context.WithCancel(context.Background())
		defer cancelCtx()
		go loop.worker(ctx)
		synctest.Wait()
		if got := executor.refreshCalls.Load(); got != 0 {
			t.Fatalf("stale queued job refreshed replacement registration %d times, want 0", got)
		}
		loop.applyDirty(now)
		loop.handleDue(ctx, now)
		synctest.Wait()
		if got := executor.refreshCalls.Load(); got != 1 {
			t.Fatalf("replacement registration refresh calls = %d, want 1", got)
		}
	})
}
