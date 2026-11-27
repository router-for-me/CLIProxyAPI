package cliproxy

import (
	"context"
	"errors"
	"net"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/api"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// heldRefreshSaveStore holds the first save made after it is armed until released.
type heldRefreshSaveStore struct {
	armed   atomic.Bool
	entered chan struct{}
	release chan struct{}
}

func (*heldRefreshSaveStore) List(context.Context) ([]*coreauth.Auth, error) { return nil, nil }

func (s *heldRefreshSaveStore) Save(_ context.Context, auth *coreauth.Auth) (string, error) {
	if s.armed.CompareAndSwap(true, false) {
		close(s.entered)
		<-s.release
	}
	return auth.ID, nil
}

func (*heldRefreshSaveStore) Delete(context.Context, string) error { return nil }

// alwaysRefreshRuntime schedules the credential on every loop interval.
type alwaysRefreshRuntime struct{}

func (alwaysRefreshRuntime) ShouldRefresh(time.Time, *coreauth.Auth) bool { return true }

// Shutdown waits for an in-flight refresh to finish saving, but must close the API
// listener first: a slow save must not keep the server accepting requests.
func TestServiceShutdown_ClosesListenerBeforeWaitingForRefreshSave(t *testing.T) {
	ln, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("failed to listen: %v", errListen)
	}
	addr := ln.Addr().String()
	port := ln.Addr().(*net.TCPAddr).Port
	if errClose := ln.Close(); errClose != nil {
		t.Fatalf("failed to close listener: %v", errClose)
	}
	cfg := &config.Config{Host: "127.0.0.1", Port: port}
	server := api.NewServer(cfg, nil, nil, "")
	// Start returns once Stop has closed the listener.
	serverStopped := make(chan struct{})
	go func() {
		defer close(serverStopped)
		_ = server.Start()
	}()

	// The timer only guards against a hang.
	hangGuard := time.NewTimer(30 * time.Second)
	defer hangGuard.Stop()
	for {
		conn, errDial := net.Dial("tcp", addr)
		if errDial == nil {
			if errClose := conn.Close(); errClose != nil {
				t.Logf("connection close error: %v", errClose)
			}
			break
		}
		select {
		case <-hangGuard.C:
			t.Fatalf("server never started listening: %v", errDial)
		default:
			runtime.Gosched()
		}
	}

	store := &heldRefreshSaveStore{entered: make(chan struct{}), release: make(chan struct{})}
	released := false
	releaseSave := func() {
		if !released {
			released = true
			close(store.release)
		}
	}
	manager := coreauth.NewManager(store, nil, nil)
	t.Cleanup(func() {
		releaseSave()
		// Let the refresh finish before the next test touches shared state.
		_ = manager.WaitAutoRefresh(context.Background())
	})
	manager.RegisterExecutor(&syncTestExecutor{})
	if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "shutdown-held-refresh",
		Provider: "codex",
		Runtime:  alwaysRefreshRuntime{},
	}); errRegister != nil {
		t.Fatalf("Register error: %v", errRegister)
	}
	store.armed.Store(true)
	manager.StartAutoRefresh(context.Background(), 10*time.Millisecond)
	select {
	case <-store.entered:
	case <-hangGuard.C:
		t.Fatal("auto-refresh never saved the refreshed credential")
	}

	service := &Service{server: server, cfg: cfg, coreManager: manager}
	shutdownCtx, cancelShutdown := context.WithCancel(context.Background())
	defer cancelShutdown()
	shutdownDone := make(chan error, 1)
	go func() {
		shutdownDone <- service.Shutdown(shutdownCtx)
	}()

	select {
	case <-serverStopped:
	case errShutdown := <-shutdownDone:
		t.Fatalf("Shutdown returned (err %v) before closing the API listener", errShutdown)
	case <-hangGuard.C:
		t.Fatal("API listener still open while Shutdown waits for a refresh save")
	}

	// The listener is closed and the save is still held. Ending ctx now makes a
	// Shutdown that is still waiting for the save return context.Canceled; one
	// that skipped the wait has returned nil already. No scheduling assumption.
	cancelShutdown()
	select {
	case errShutdown := <-shutdownDone:
		if !errors.Is(errShutdown, context.Canceled) {
			t.Fatalf("Shutdown error = %v, want context.Canceled from the refresh wait", errShutdown)
		}
	case <-hangGuard.C:
		t.Fatal("Shutdown did not return after its ctx ended")
	}
	releaseSave()
}

// countingStopSelector counts Stop calls; StoppableSelector.Stop need not be
// safe to call twice.
type countingStopSelector struct {
	coreauth.RoundRobinSelector
	stops atomic.Int32
}

func (s *countingStopSelector) Stop() { s.stops.Add(1) }

// Shutdown stops auto-refresh early and waits for its saves later; a custom
// selector must still be stopped only once.
func TestServiceShutdown_StopsSelectorOnce(t *testing.T) {
	selector := &countingStopSelector{}
	manager := coreauth.NewManager(nil, selector, nil)
	manager.StartAutoRefresh(context.Background(), time.Hour)
	service := &Service{cfg: &config.Config{}, coreManager: manager}
	if errShutdown := service.Shutdown(context.Background()); errShutdown != nil {
		t.Fatalf("Shutdown error = %v", errShutdown)
	}
	if got := selector.stops.Load(); got != 1 {
		t.Fatalf("selector Stop calls = %d, want 1", got)
	}
}

// When ctx ends while a refresh is still saving, Shutdown must say so, even if an
// earlier teardown step already failed: later calls return at once, so the first
// call's result is the caller's only signal that cleanup is incomplete (for
// example before removing the auth directory).
func TestServiceShutdown_ReportsRefreshSaveStillRunning(t *testing.T) {
	errWatcherStop := errors.New("watcher stop failed")
	for name, earlier := range map[string]error{"alone": nil, "after an earlier error": errWatcherStop} {
		t.Run(name, func(t *testing.T) {
			store := &heldRefreshSaveStore{entered: make(chan struct{}), release: make(chan struct{})}
			released := false
			releaseSave := func() {
				if !released {
					released = true
					close(store.release)
				}
			}
			manager := coreauth.NewManager(store, nil, nil)
			t.Cleanup(func() {
				releaseSave()
				_ = manager.WaitAutoRefresh(context.Background())
			})
			manager.RegisterExecutor(&syncTestExecutor{})
			if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{
				ID:       "shutdown-unfinished-refresh",
				Provider: "codex",
				Runtime:  alwaysRefreshRuntime{},
			}); errRegister != nil {
				t.Fatalf("Register error: %v", errRegister)
			}
			store.armed.Store(true)
			manager.StartAutoRefresh(context.Background(), 10*time.Millisecond)
			// The timer only guards against a hang.
			hangGuard := time.NewTimer(30 * time.Second)
			defer hangGuard.Stop()
			select {
			case <-store.entered:
			case <-hangGuard.C:
				t.Fatal("auto-refresh never saved the refreshed credential")
			}

			service := &Service{cfg: &config.Config{}, coreManager: manager}
			if earlier != nil {
				service.watcher = &WatcherWrapper{stop: func() error { return earlier }}
			}
			expired, cancel := context.WithCancel(context.Background())
			cancel()
			// The save is held, so the wait can only end with ctx.
			errShutdown := service.Shutdown(expired)
			if !errors.Is(errShutdown, context.Canceled) {
				t.Fatalf("Shutdown with a save still held = %v, want an error wrapping context.Canceled", errShutdown)
			}
			if earlier != nil && !errors.Is(errShutdown, earlier) {
				t.Fatalf("Shutdown = %v, want it to keep the earlier teardown error too", errShutdown)
			}
			if errLater := service.Shutdown(context.Background()); errLater != nil {
				t.Fatalf("later Shutdown = %v, want nil at once", errLater)
			}
		})
	}
}

// signalStopSelector closes stopped on its first Stop, which StopAutoRefresh
// calls: it tells a test that a Shutdown has entered teardown.
type signalStopSelector struct {
	coreauth.RoundRobinSelector
	once    sync.Once
	stopped chan struct{}
}

func (s *signalStopSelector) Stop() { s.once.Do(func() { close(s.stopped) }) }

// shutdownFromHook calls Shutdown from OnAuthUpdated, which runs on the refresh
// worker, once the first Shutdown has stopped auto-refresh.
type shutdownFromHook struct {
	coreauth.NoopHook
	armed   atomic.Bool
	service atomic.Pointer[Service]
	waitFor <-chan struct{}
	entered chan struct{}
	done    chan error
}

func (h *shutdownFromHook) OnAuthUpdated(context.Context, *coreauth.Auth) {
	if !h.armed.CompareAndSwap(true, false) {
		return
	}
	close(h.entered)
	<-h.waitFor
	// An already expired deadline: the hook does not want to wait at all.
	expired, cancel := context.WithCancel(context.Background())
	cancel()
	h.done <- h.service.Load().Shutdown(expired)
}

// A refresh hook may call Shutdown while another Shutdown is tearing down. The
// first call waits for in-flight refresh saves, and the hook runs on a refresh
// worker after its save, so that wait must not include the hook: otherwise the
// hook blocks on the first call and the first call on the hook, and with an
// unbounded first ctx neither returns.
func TestServiceShutdown_RefreshHookCallsShutdownDuringShutdown(t *testing.T) {
	selector := &signalStopSelector{stopped: make(chan struct{})}
	hook := &shutdownFromHook{
		waitFor: selector.stopped,
		entered: make(chan struct{}),
		done:    make(chan error, 1),
	}
	manager := coreauth.NewManager(&heldRefreshSaveStore{}, selector, hook)
	manager.RegisterExecutor(&syncTestExecutor{})
	if _, errRegister := manager.Register(context.Background(), &coreauth.Auth{
		ID:       "shutdown-from-refresh-hook",
		Provider: "codex",
		Runtime:  alwaysRefreshRuntime{},
	}); errRegister != nil {
		t.Fatalf("Register error: %v", errRegister)
	}
	service := &Service{cfg: &config.Config{}, coreManager: manager}
	hook.service.Store(service)
	hook.armed.Store(true)
	manager.StartAutoRefresh(context.Background(), 10*time.Millisecond)

	// The timer only guards against a hang.
	hangGuard := time.NewTimer(30 * time.Second)
	defer hangGuard.Stop()
	select {
	case <-hook.entered:
	case <-hangGuard.C:
		t.Fatal("auto-refresh never ran the update hook")
	}

	// The hook is now on a refresh worker, waiting for this call to stop
	// auto-refresh before it calls Shutdown itself.
	firstDone := make(chan error, 1)
	go func() {
		firstDone <- service.Shutdown(context.Background())
	}()

	for range 2 {
		select {
		case errFirst := <-firstDone:
			if errFirst != nil {
				t.Fatalf("first Shutdown error = %v", errFirst)
			}
			firstDone = nil
		case errHook := <-hook.done:
			if errHook != nil {
				t.Fatalf("Shutdown from the refresh hook error = %v", errHook)
			}
			hook.done = nil
		case <-hangGuard.C:
			t.Fatalf("deadlock: first Shutdown returned=%v, hook Shutdown returned=%v", firstDone == nil, hook.done == nil)
		}
	}
}
