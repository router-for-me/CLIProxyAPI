package cliproxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/api"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// Negative readiness assertions call the exact production predicate. They cannot
// pass just because a background goroutine has not been scheduled yet.
func TestFinishCatalogLoadingWaitsForQueuedAuthUpdates(t *testing.T) {
	pending := 2
	wrapper := &WatcherWrapper{pendingAuthUpdates: func() int { return pending }}
	service := &Service{authUpdates: make(chan watcher.AuthUpdate, 4)}
	service.authUpdates <- watcher.AuthUpdate{ID: "synthetic"}
	if service.catalogUpdatesApplied(wrapper) {
		t.Fatal("ready with watcher updates pending")
	}
	pending = 0
	if service.catalogUpdatesApplied(wrapper) {
		t.Fatal("ready with service update pending")
	}
	<-service.authUpdates
	if !service.catalogUpdatesApplied(wrapper) {
		t.Fatal("not ready after publication")
	}
}

func TestFinishCatalogLoadingWaitsForInFlightBatch(t *testing.T) {
	service := &Service{authUpdates: make(chan watcher.AuthUpdate, 1)}
	service.authUpdateMu.Lock()
	ready := service.catalogUpdatesApplied(&WatcherWrapper{})
	service.authUpdateMu.Unlock()
	if ready {
		t.Fatal("ready while publication mutex held")
	}
	if !service.catalogUpdatesApplied(&WatcherWrapper{}) {
		t.Fatal("not ready after publication")
	}
}

func runCatalogSignals(t *testing.T, service *Service, wrapper *WatcherWrapper, cancelInstead bool) {
	t.Helper()
	registry.SetCatalogLoading(true)
	defer registry.SetCatalogLoading(false)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ticks, ceiling := make(chan time.Time), make(chan time.Time)
	done := make(chan struct{})
	go func() {
		defer close(done)
		service.finishCatalogLoadingWithSignals(ctx, catalogStartupReady(wrapper), ticks, ceiling)
	}()
	// Receiving a second tick proves the worker completed the first readiness
	// check. A mutex-blocking implementation cannot reach this handshake.
	for i := 0; i < 2; i++ {
		select {
		case ticks <- time.Time{}:
		case <-time.After(time.Second):
			t.Fatal("readiness check blocked")
		}
	}
	if !registry.CatalogLoading() {
		t.Fatal("loading cleared before completion")
	}
	if cancelInstead {
		cancel()
	} else {
		select {
		case ceiling <- time.Time{}:
		case <-time.After(time.Second):
			t.Fatal("ceiling blocked")
		}
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("worker failed to exit")
	}
	if registry.CatalogLoading() {
		t.Fatal("loading did not clear")
	}
}

func TestFinishCatalogLoadingClearsAtCeiling(t *testing.T) {
	service := &Service{authUpdates: make(chan watcher.AuthUpdate, 1)}
	service.authUpdates <- watcher.AuthUpdate{ID: "custom-watcher-stuck"}
	runCatalogSignals(t, service, &WatcherWrapper{}, false)
}

func TestFinishCatalogLoadingCeilingWhileAuthMutexHeld(t *testing.T) {
	service := &Service{authUpdates: make(chan watcher.AuthUpdate, 1)}
	service.authUpdateMu.Lock()
	defer service.authUpdateMu.Unlock()
	runCatalogSignals(t, service, &WatcherWrapper{}, false)
}

func TestFinishCatalogLoadingCancellationWhileAuthMutexHeld(t *testing.T) {
	service := &Service{authUpdates: make(chan watcher.AuthUpdate, 1)}
	service.authUpdateMu.Lock()
	defer service.authUpdateMu.Unlock()
	runCatalogSignals(t, service, &WatcherWrapper{}, true)
}

func TestFinishCatalogLoadingClearsOnPublication(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service := &Service{authUpdates: make(chan watcher.AuthUpdate)}
		ready, stop := service.startCatalogLoading(t.Context())
		defer stop()
		ready <- &WatcherWrapper{}
		time.Sleep(50 * time.Millisecond)
		synctest.Wait()
		if registry.CatalogLoading() {
			t.Fatal("loading did not clear after publication")
		}
	})
}

// A service batch may coalesce two revisions of the same auth. Both original
// deliveries must be acknowledged, not just the revision that is published.
func TestCatalogLoadingAcknowledgesCoalescedUpdates(t *testing.T) {
	dir := t.TempDir()
	w, err := watcher.NewWatcher("", dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := w.Stop(); err != nil {
			t.Error(err)
		}
	}()
	queue := make(chan watcher.AuthUpdate, 2)
	w.SetAuthUpdateQueue(queue)
	received := make([]watcher.AuthUpdate, 0, 2)
	for i := 0; i < 2; i++ {
		if !w.DispatchRuntimeAuthUpdate(watcher.AuthUpdate{ID: "synthetic", Action: watcher.AuthUpdateActionDelete}) {
			t.Fatal("dispatch failed")
		}
		select {
		case update := <-queue:
			received = append(received, update)
		case <-time.After(time.Second):
			t.Fatal("dispatch blocked")
		}
	}
	if got := w.PendingAuthUpdates(); got != 2 {
		t.Fatalf("unpublished pending=%d, want 2", got)
	}
	service := &Service{}
	service.handleAuthUpdates(context.Background(), received)
	if got := w.PendingAuthUpdates(); got != 0 {
		t.Fatalf("pending after coalesced publication=%d, want 0", got)
	}
}

func TestCatalogLoadingWaitsForInitialNativePublication(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		resetAntigravityCapabilityCache()
		defer resetAntigravityCapabilityCache()
		service := &Service{cfg: &config.Config{}, coreManager: coreauth.NewManager(nil, nil, nil)}
		auth, err := service.coreManager.Register(t.Context(), antigravityTestAuth("catalog-loading-native", "https://catalog.invalid"))
		if err != nil {
			t.Fatal(err)
		}
		reg := GlobalModelRegistry()
		defer reg.UnregisterClient(auth.ID)
		started, release := make(chan struct{}, 1), make(chan struct{})
		transport := http.DefaultTransport
		http.DefaultTransport = antigravityReviewTransport(func(req *http.Request) (*http.Response, error) {
			select {
			case started <- struct{}{}:
			default:
			}
			select {
			case <-release:
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"models":{"gemini-3.1-flash-lite":{}}}`)), Request: req}, nil
			case <-req.Context().Done():
				return nil, req.Context().Err()
			}
		})
		defer func() { http.DefaultTransport = transport }()
		ctx, cancel := context.WithCancel(t.Context())
		defer func() { cancel(); service.WaitAntigravityProbes(); synctest.Wait() }()
		service.completeModelRegistrationForAuth(ctx, auth)
		synctest.Wait()
		select {
		case <-started:
		default:
			t.Fatal("initial native probe did not start")
		}
		if reg.ClientSupportsModel(auth.ID, "gemini-3.1-flash-lite") {
			t.Fatal("model published before probe response")
		}
		if service.catalogUpdatesApplied(&WatcherWrapper{}) {
			t.Fatal("catalog ready while initial native publication is pending")
		}
		close(release)
		service.WaitAntigravityProbes()
		if !reg.ClientSupportsModel(auth.ID, "gemini-3.1-flash-lite") {
			t.Fatal("probe finished without publishing the native model")
		}
		if !service.catalogUpdatesApplied(&WatcherWrapper{}) {
			t.Fatal("catalog not ready after native publication")
		}
	})
}

func TestFinishCatalogLoadingCeilingWhileProbeMutexHeld(t *testing.T) {
	service := &Service{}
	service.antigravityProbeMu.Lock()
	defer service.antigravityProbeMu.Unlock()
	runCatalogSignals(t, service, &WatcherWrapper{}, false)
}

func TestCatalogLoadingWaitsForQueuedAndRunningNativeProbes(t *testing.T) {
	service := &Service{antigravityRefreshPending: map[string]*antigravityModelRefreshRequest{
		"queued":  {},
		"running": nil,
	}}
	wrapper := &WatcherWrapper{}
	if service.catalogUpdatesApplied(wrapper) {
		t.Fatal("ready with queued native discovery")
	}
	delete(service.antigravityRefreshPending, "queued")
	if service.catalogUpdatesApplied(wrapper) {
		t.Fatal("ready with native publication in flight")
	}
	delete(service.antigravityRefreshPending, "running")
	if !service.catalogUpdatesApplied(wrapper) {
		t.Fatal("not ready after native discovery completed")
	}
}

// Exercise the post-listener startup sequence called by Run, including the
// loading transition, server start, hook, watcher factory and watcher start.
func TestServiceRunCatalogDeadlineCoversSynchronousStartup(t *testing.T) {
	for _, stage := range []string{"after-start", "watcher-factory", "watcher-start"} {
		t.Run(stage, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				service := catalogStartupTestService(t)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				entered, release := make(chan struct{}), make(chan struct{})
				block := func() { close(entered); <-release }
				service.hooks.OnAfterStart = func(*Service) {
					if stage == "after-start" {
						block()
					}
				}
				service.watcherFactory = func(string, string, func(*config.Config)) (*WatcherWrapper, error) {
					if stage == "watcher-factory" {
						block()
					}
					return &WatcherWrapper{start: func(context.Context) error {
						if stage == "watcher-start" {
							block()
						}
						return nil
					}}, nil
				}
				done := make(chan error, 1)
				go func() {
					stop, err := service.startServerAndWatcher(ctx, false)
					stop()
					done <- err
				}()
				<-entered
				synctest.Wait()
				if !registry.CatalogLoading() {
					t.Error("empty queues cleared loading before startup completed")
				}
				time.Sleep(31 * time.Second) // synctest virtual time, not wall-clock ordering
				synctest.Wait()
				if registry.CatalogLoading() {
					t.Error("startup stall prevented the 30-second ceiling from clearing loading")
				}
				cancel()
				close(release)
				<-done
				synctest.Wait()
				if registry.CatalogLoading() {
					t.Error("loading remained set after Run exited")
				}
			})
		})
	}
}

func catalogStartupTestService(t *testing.T) *Service {
	t.Helper()
	t.Cleanup(func() { registry.SetCatalogLoading(false) })
	// An uninitialized server returns immediately without opening sockets or
	// allocating unrelated management-handler background workers.
	return &Service{cfg: &config.Config{}, server: &api.Server{}}
}

func catalogStartupReady(w *WatcherWrapper) <-chan *WatcherWrapper {
	ready := make(chan *WatcherWrapper, 1)
	ready <- w
	return ready
}

func TestServiceCatalogStartupGateAndPublication(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service := catalogStartupTestService(t)
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		var pending atomic.Int64
		pending.Store(1)
		watcherStarted, release := make(chan struct{}), make(chan struct{})
		service.watcherFactory = func(string, string, func(*config.Config)) (*WatcherWrapper, error) {
			return &WatcherWrapper{
				start: func(context.Context) error {
					close(watcherStarted)
					<-release
					return nil
				},
				pendingAuthUpdates: func() int { return int(pending.Load()) },
			}, nil
		}
		stopped := make(chan func(), 1)
		go func() {
			stop, err := service.startServerAndWatcher(ctx, false)
			if err != nil {
				t.Error(err)
			}
			stopped <- stop
		}()
		<-watcherStarted
		// Even empty queues must not bypass the still-incomplete startup stage.
		pending.Store(0)
		time.Sleep(200 * time.Millisecond)
		synctest.Wait()
		if !registry.CatalogLoading() {
			t.Error("loading cleared before watcher initialization completed")
		}
		pending.Store(1)
		close(release)
		stop := <-stopped
		defer stop()
		time.Sleep(200 * time.Millisecond)
		synctest.Wait()
		if !registry.CatalogLoading() {
			t.Error("loading cleared before initial publication was acknowledged")
		}
		pending.Store(0)
		time.Sleep(50 * time.Millisecond)
		synctest.Wait()
		if registry.CatalogLoading() {
			t.Error("loading remained set after startup and publication completed")
		}
	})
}

func TestServiceCatalogStartupNilWatcher(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service := &Service{}
		ready, stop := service.startCatalogLoading(t.Context())
		defer stop()
		time.Sleep(50 * time.Millisecond) // Virtual time inside synctest.
		synctest.Wait()
		if !registry.CatalogLoading() {
			t.Fatal("loading cleared before startup readiness was signalled")
		}
		// WatcherWrapper methods explicitly support nil to disable watching.
		// Receiving that completed startup must not look like an unopened gate.
		ready <- nil
		time.Sleep(50 * time.Millisecond)
		synctest.Wait()
		if registry.CatalogLoading() {
			t.Fatal("nil watcher prevented completed startup from becoming ready")
		}
	})
}

func TestServiceCatalogStartupCancellationAndErrors(t *testing.T) {
	for _, stage := range []string{"cancel", "factory-error", "start-error"} {
		t.Run(stage, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				service := catalogStartupTestService(t)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				entered, release := make(chan struct{}), make(chan struct{})
				wantErr := errors.New("startup failed")
				service.watcherFactory = func(string, string, func(*config.Config)) (*WatcherWrapper, error) {
					if stage == "factory-error" {
						return nil, wantErr
					}
					return &WatcherWrapper{start: func(context.Context) error {
						if stage == "start-error" {
							return wantErr
						}
						close(entered)
						<-release
						return nil
					}}, nil
				}
				done := make(chan error, 1)
				go func() {
					stop, err := service.startServerAndWatcher(ctx, false)
					stop() // Must be safe after the error path already stopped the worker.
					done <- err
				}()
				if stage == "cancel" {
					<-entered
					cancel()
					synctest.Wait()
					if registry.CatalogLoading() {
						t.Error("cancellation waited for blocked startup")
					}
					close(release)
				}
				err := <-done
				if stage != "cancel" && !errors.Is(err, wantErr) {
					t.Errorf("error = %v, want startup failure", err)
				}
				cancel()
				synctest.Wait()
				if registry.CatalogLoading() {
					t.Error("loading remained set after startup exited")
				}
				// No owned timer/worker may survive to clear a subsequent loading epoch.
				registry.SetCatalogLoading(true)
				time.Sleep(31 * time.Second)
				synctest.Wait()
				if !registry.CatalogLoading() {
					t.Error("startup worker survived cleanup")
				}
			})
		})
	}
}

func TestServiceCatalogStartupHomeDoesNotChangeLoading(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		service := catalogStartupTestService(t)
		service.watcherFactory = func(string, string, func(*config.Config)) (*WatcherWrapper, error) {
			t.Error("Home mode created a watcher")
			return &WatcherWrapper{}, nil
		}
		for _, loading := range []bool{false, true} {
			registry.SetCatalogLoading(loading)
			stop, err := service.startServerAndWatcher(t.Context(), true)
			if err != nil {
				t.Fatal(err)
			}
			stop()
			time.Sleep(31 * time.Second)
			synctest.Wait()
			if registry.CatalogLoading() != loading {
				t.Errorf("Home mode changed loading from %t", loading)
			}
		}
	})
}
