package auth

import (
	"context"
	"errors"
	"net/http"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
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
	heldID  atomic.Value  // string: the auth whose save is held
	passed  chan<- string // if set, receives the ID of each save that is not held
}

func (*refreshSaveStore) List(context.Context) ([]*Auth, error) { return nil, nil }

func (s *refreshSaveStore) Save(_ context.Context, auth *Auth) (string, error) {
	if !s.armed.Load() {
		if s.passed != nil {
			select {
			case s.passed <- auth.ID:
			default:
			}
		}
		return auth.ID, nil
	}
	s.heldID.Store(auth.ID)
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

// heldSave holds the refresh save until released, and reports stop progress.
type heldSave struct {
	store   *refreshSaveStore
	release func()
	waiting chan []*authAutoRefreshLoop // receives the loops a stop starts waiting for
}

func newHeldSave(t *testing.T) *heldSave {
	t.Helper()
	releaseCh := make(chan struct{})
	var once sync.Once
	h := &heldSave{
		store:   &refreshSaveStore{entered: make(chan struct{}, 1), hold: func() { <-releaseCh }},
		release: func() { once.Do(func() { close(releaseCh) }) },
		waiting: make(chan []*authAutoRefreshLoop, 4),
	}
	original := beforeAutoRefreshStopWait
	beforeAutoRefreshStopWait = func(loops []*authAutoRefreshLoop) { h.waiting <- loops }
	t.Cleanup(func() {
		h.release()
		beforeAutoRefreshStopWait = original
	})
	return h
}

// waitingStop stops auto-refresh and waits for its workers without a bound.
func waitingStop(manager *Manager) func() {
	return func() { _ = manager.StopAutoRefreshContext(context.Background()) }
}

// stopAsync runs stop on its own goroutine and sends whether the held save had
// finished when stop returned.
func (h *heldSave) stopAsync(stop func()) <-chan bool {
	returned := make(chan bool, 1)
	go func() {
		stop()
		returned <- h.store.done.Load()
	}()
	return returned
}

// awaitWaiting fails if stop returns while the save is held, and returns the
// loops stop is waiting for once it waits. The timer only guards against a hang.
func (h *heldSave) awaitWaiting(t *testing.T, returned <-chan bool) []*authAutoRefreshLoop {
	t.Helper()
	hangGuard := time.NewTimer(30 * time.Second)
	defer hangGuard.Stop()
	select {
	case loops := <-h.waiting:
		return loops
	case <-returned:
		t.Fatal("stop returned while the refresh save was still held")
	case <-hangGuard.C:
		t.Fatal("stop never started waiting for the refresh workers")
	}
	return nil
}

// replaceRefreshLoop starts a new loop over the running one, whose worker is
// holding the save, and returns the replaced loop. The new loop's saves pass.
func replaceRefreshLoop(manager *Manager, store *refreshSaveStore) *authAutoRefreshLoop {
	manager.mu.RLock()
	replaced := manager.refreshLoop
	manager.mu.RUnlock()
	store.armed.Store(false)
	manager.StartAutoRefresh(context.Background(), 10*time.Millisecond)
	return replaced
}

func containsLoop(loops []*authAutoRefreshLoop, want *authAutoRefreshLoop) bool {
	for _, loop := range loops {
		if loop == want {
			return true
		}
	}
	return false
}

func awaitReturned(t *testing.T, returned <-chan bool) bool {
	t.Helper()
	hangGuard := time.NewTimer(30 * time.Second)
	defer hangGuard.Stop()
	select {
	case saveDone := <-returned:
		return saveDone
	case <-hangGuard.C:
		t.Fatal("stop did not return after the save was released")
		return false
	}
}

// StopAutoRefreshContext must not return while a refresh it started is still
// saving: callers (service shutdown, tests removing the auth directory) rely on no
// background save running afterwards.
func TestStopAutoRefreshContextWaitsForInFlightRefreshSave(t *testing.T) {
	h := newHeldSave(t)
	manager := startRefreshWithHeldSave(t, h.store)

	returned := h.stopAsync(waitingStop(manager))
	h.awaitWaiting(t, returned)
	h.release()
	if !awaitReturned(t, returned) {
		t.Fatal("StopAutoRefreshContext returned before the refresh save finished")
	}
}

// StopAutoRefreshContext gives up waiting when its context ends, so a refresh
// that never finishes cannot hang shutdown.
func TestStopAutoRefreshContextReturnsWhenContextEnds(t *testing.T) {
	h := newHeldSave(t)
	manager := startRefreshWithHeldSave(t, h.store)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := manager.StopAutoRefreshContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("StopAutoRefreshContext() error = %v, want context.Canceled", err)
	}
	if h.store.done.Load() {
		t.Fatal("held save finished early; the test did not exercise the wait")
	}
}

// A waiting stop after one that gave up on its context must still wait for the
// same loop's workers, not return because the loop was already detached.
func TestStopAutoRefreshContextAfterTimedOutStopStillWaits(t *testing.T) {
	h := newHeldSave(t)
	manager := startRefreshWithHeldSave(t, h.store)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := manager.StopAutoRefreshContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("first StopAutoRefreshContext() error = %v, want context.Canceled", err)
	}
	<-h.waiting // the first stop waited before giving up

	returned := h.stopAsync(waitingStop(manager))
	h.awaitWaiting(t, returned)
	h.release()
	if !awaitReturned(t, returned) {
		t.Fatal("second StopAutoRefreshContext returned before the refresh save finished")
	}
}

// StartAutoRefresh replaces a loop without waiting for it, so a later waiting stop
// must still wait for the replaced loop's worker that is saving.
func TestStopAutoRefreshContextWaitsForReplacedLoop(t *testing.T) {
	h := newHeldSave(t)
	manager := startRefreshWithHeldSave(t, h.store)
	replaced := replaceRefreshLoop(manager, h.store)

	returned := h.stopAsync(waitingStop(manager))
	if loops := h.awaitWaiting(t, returned); !containsLoop(loops, replaced) {
		t.Fatal("StopAutoRefreshContext does not wait for the loop StartAutoRefresh replaced")
	}
	h.release()
	if !awaitReturned(t, returned) {
		t.Fatal("StopAutoRefreshContext returned before the replaced loop's save finished")
	}
}

// A restart after a stop that gave up on its context must not drop the stopped
// loop: the next waiting stop still waits for its worker.
func TestStopAutoRefreshContextAfterTimedOutStopAndRestartStillWaits(t *testing.T) {
	h := newHeldSave(t)
	manager := startRefreshWithHeldSave(t, h.store)
	manager.mu.RLock()
	stopped := manager.refreshLoop
	manager.mu.RUnlock()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := manager.StopAutoRefreshContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("first StopAutoRefreshContext() error = %v, want context.Canceled", err)
	}
	<-h.waiting // the first stop waited before giving up
	replaceRefreshLoop(manager, h.store)

	returned := h.stopAsync(waitingStop(manager))
	if loops := h.awaitWaiting(t, returned); !containsLoop(loops, stopped) {
		t.Fatal("StopAutoRefreshContext after a restart does not wait for the loop a timed-out stop left running")
	}
	h.release()
	if !awaitReturned(t, returned) {
		t.Fatal("StopAutoRefreshContext returned before the stopped loop's save finished")
	}
}

// A loop replaced while its only worker is held in a save must release its queued
// jobs at once, so the new loop refreshes those credentials instead of waiting for
// the held save.
func TestReplacedLoopReleasesQueuedJobsWhileWorkerSaves(t *testing.T) {
	h := newHeldSave(t)
	passed := make(chan string, 8)
	h.store.passed = passed
	manager := NewManager(h.store, &RoundRobinSelector{}, nil)
	manager.runtimeConfig.Store(&internalconfig.Config{AuthAutoRefreshWorkers: 1})
	manager.RegisterExecutor(stopTestExecutor{})
	ids := []string{"replaced-loop-a", "replaced-loop-b"}
	for _, id := range ids {
		if _, err := manager.Register(context.Background(), &Auth{
			ID:       id,
			Provider: "stop-test",
			Runtime:  alwaysRefresh{},
			Metadata: map[string]any{
				"access_token":  "expired-token",
				"refresh_token": "refresh-1",
				"expires_at":    time.Now().Add(-time.Hour).Format(time.RFC3339),
			},
		}); err != nil {
			t.Fatalf("Register(%s) error: %v", id, err)
		}
	}
	// Drop the saves Register made; only refresh saves count below.
	for len(passed) > 0 {
		<-passed
	}
	h.store.armed.Store(true)
	manager.StartAutoRefresh(context.Background(), 10*time.Millisecond)
	t.Cleanup(func() {
		h.release()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = manager.StopAutoRefreshContext(ctx)
	})

	// The timer only guards against a hang.
	hangGuard := time.NewTimer(30 * time.Second)
	defer hangGuard.Stop()
	select {
	case <-h.store.entered:
	case <-hangGuard.C:
		t.Fatal("auto-refresh never saved a refreshed credential")
	}
	queued := ids[0]
	if h.store.heldID.Load() == queued {
		queued = ids[1]
	}
	manager.mu.RLock()
	oldLoop := manager.refreshLoop
	manager.mu.RUnlock()
	// The other credential's job must be waiting on the old loop's busy worker.
	for {
		manager.mu.RLock()
		job := manager.refreshJobs[queued]
		isQueued := job != nil && job.loop == oldLoop && !job.running
		manager.mu.RUnlock()
		if isQueued {
			break
		}
		select {
		case <-hangGuard.C:
			t.Fatalf("%s was never queued behind the held save", queued)
		default:
			runtime.Gosched()
		}
	}

	replaceRefreshLoop(manager, h.store)
	for {
		select {
		case id := <-passed:
			if id == queued {
				return
			}
		case <-hangGuard.C:
			t.Fatalf("the new loop did not refresh %s while the replaced loop's worker held its save", queued)
		}
	}
}

// stopFromRefreshHook stops auto-refresh from the first OnAuthUpdated callback,
// which runs on the refresh worker that is finishing the refresh.
type stopFromRefreshHook struct {
	NoopHook
	manager  *Manager
	once     sync.Once
	returned chan struct{}
}

func (h *stopFromRefreshHook) OnAuthUpdated(context.Context, *Auth) {
	h.once.Do(func() {
		h.manager.StopAutoRefresh()
		close(h.returned)
	})
}

// A hook may stop auto-refresh from a refresh callback. StopAutoRefresh must not
// wait for the workers there: the worker running the hook cannot return until
// the stop does.
func TestStopAutoRefreshFromRefreshHookDoesNotDeadlock(t *testing.T) {
	hook := &stopFromRefreshHook{returned: make(chan struct{})}
	manager := NewManager(nil, &RoundRobinSelector{}, hook)
	hook.manager = manager
	manager.RegisterExecutor(stopTestExecutor{})
	if _, err := manager.Register(context.Background(), &Auth{
		ID:       "stop-from-hook",
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
	manager.StartAutoRefresh(context.Background(), 10*time.Millisecond)
	t.Cleanup(func() {
		// Bounded so a deadlocked worker fails the test instead of hanging it.
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = manager.StopAutoRefreshContext(ctx)
	})

	// The timer only guards against a hang; the hook signals when its stop returns.
	hangGuard := time.NewTimer(30 * time.Second)
	defer hangGuard.Stop()
	select {
	case <-hook.returned:
	case <-hangGuard.C:
		t.Fatal("StopAutoRefresh called from OnAuthUpdated on the refresh worker never returned")
	}
}

// Concurrent StartAutoRefresh calls must each retire the loop they replace, so a
// waiting stop afterwards leaves no loop running.
func TestConcurrentStartAutoRefreshLeavesNoLoopRunning(t *testing.T) {
	var mu sync.Mutex
	var loops []*authAutoRefreshLoop
	original := refreshLoopStarted
	refreshLoopStarted = func(loop *authAutoRefreshLoop) {
		mu.Lock()
		loops = append(loops, loop)
		mu.Unlock()
	}
	t.Cleanup(func() { refreshLoopStarted = original })

	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	const starts = 40
	begin := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < starts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-begin
			manager.StartAutoRefresh(context.Background(), time.Hour)
		}()
	}
	close(begin)
	wg.Wait()

	// Bounded so an orphaned loop fails the test instead of hanging it.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := manager.StopAutoRefreshContext(ctx); err != nil {
		t.Fatalf("StopAutoRefreshContext() error = %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(loops) != starts {
		t.Fatalf("started %d loops, want %d", len(loops), starts)
	}
	running := 0
	for _, loop := range loops {
		select {
		case <-loop.done:
		default:
			running++
		}
	}
	if running != 0 {
		t.Fatalf("%d of %d loops still running after StopAutoRefreshContext", running, starts)
	}
}
