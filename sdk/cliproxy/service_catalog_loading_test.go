package cliproxy

import (
	"context"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher"
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
