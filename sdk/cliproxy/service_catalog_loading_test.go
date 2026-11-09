package cliproxy

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

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
	go func() { defer close(done); service.finishCatalogLoadingWithSignals(ctx, wrapper, ticks, ceiling) }()
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
	registry.SetCatalogLoading(true)
	defer registry.SetCatalogLoading(false)
	ticks := make(chan time.Time, 1)
	ticks <- time.Time{}
	service := &Service{authUpdates: make(chan watcher.AuthUpdate)}
	service.finishCatalogLoadingWithSignals(context.Background(), &WatcherWrapper{}, ticks, nil)
	if registry.CatalogLoading() {
		t.Fatal("loading did not clear after publication")
	}
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
