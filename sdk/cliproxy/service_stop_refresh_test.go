package cliproxy

import (
	"context"
	"errors"
	"net"
	"runtime"
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
	t.Cleanup(releaseSave)
	manager := coreauth.NewManager(store, nil, nil)
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
	shutdownDone := make(chan error, 1)
	go func() {
		shutdownDone <- service.Shutdown(context.Background())
	}()

	select {
	case <-serverStopped:
	case errShutdown := <-shutdownDone:
		t.Fatalf("Shutdown returned (err %v) while the refresh save was still held", errShutdown)
	case <-hangGuard.C:
		t.Fatal("API listener still open while Shutdown waits for a refresh save")
	}
	select {
	case errShutdown := <-shutdownDone:
		t.Fatalf("Shutdown returned (err %v) while the refresh save was still held", errShutdown)
	default:
	}

	releaseSave()
	select {
	case errShutdown := <-shutdownDone:
		if errShutdown != nil {
			t.Fatalf("Shutdown error = %v, want nil", errShutdown)
		}
	case <-hangGuard.C:
		t.Fatal("Shutdown did not return after the refresh save was released")
	}
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
			t.Cleanup(releaseSave)
			manager := coreauth.NewManager(store, nil, nil)
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
