package cliproxy

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/watcher"
)

func waitCatalogLoaded(t *testing.T, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !registry.CatalogLoading() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// The flag clears only after the watcher has handed its updates on and the
// service queue is empty, not when Start returns.
func TestFinishCatalogLoadingWaitsForQueuedAuthUpdates(t *testing.T) {
	registry.SetCatalogLoading(true)
	t.Cleanup(func() { registry.SetCatalogLoading(false) })

	var pending atomic.Int32
	pending.Store(2)
	wrapper := &WatcherWrapper{pendingAuthUpdates: func() int { return int(pending.Load()) }}
	service := &Service{authUpdates: make(chan watcher.AuthUpdate, 4)}
	service.authUpdates <- watcher.AuthUpdate{Action: watcher.AuthUpdateActionAdd, ID: "queued"}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go service.finishCatalogLoading(ctx, wrapper)

	time.Sleep(200 * time.Millisecond)
	if !registry.CatalogLoading() {
		t.Fatal("flag cleared while the watcher still had pending updates")
	}
	pending.Store(0)
	time.Sleep(200 * time.Millisecond)
	if !registry.CatalogLoading() {
		t.Fatal("flag cleared while the service queue still held an update")
	}
	<-service.authUpdates
	if !waitCatalogLoaded(t, 2*time.Second) {
		t.Fatal("flag did not clear after both queues drained")
	}
}

// A batch in flight holds authUpdateMu; the flag waits for it.
func TestFinishCatalogLoadingWaitsForInFlightBatch(t *testing.T) {
	registry.SetCatalogLoading(true)
	t.Cleanup(func() { registry.SetCatalogLoading(false) })

	service := &Service{authUpdates: make(chan watcher.AuthUpdate, 1)}
	service.authUpdateMu.Lock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go service.finishCatalogLoading(ctx, &WatcherWrapper{})

	time.Sleep(200 * time.Millisecond)
	if !registry.CatalogLoading() {
		t.Fatal("flag cleared while a batch was being handled")
	}
	service.authUpdateMu.Unlock()
	if !waitCatalogLoaded(t, 2*time.Second) {
		t.Fatal("flag did not clear after the batch finished")
	}
}

// A custom watcher that never dispatches must not leave the flag set forever.
func TestFinishCatalogLoadingClearsAtCeiling(t *testing.T) {
	registry.SetCatalogLoading(true)
	t.Cleanup(func() { registry.SetCatalogLoading(false) })
	previous := catalogLoadingCeiling
	catalogLoadingCeiling = 150 * time.Millisecond
	t.Cleanup(func() { catalogLoadingCeiling = previous })

	service := &Service{authUpdates: make(chan watcher.AuthUpdate, 1)}
	service.authUpdates <- watcher.AuthUpdate{Action: watcher.AuthUpdateActionAdd, ID: "stuck"}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go service.finishCatalogLoading(ctx, &WatcherWrapper{})

	if !waitCatalogLoaded(t, 2*time.Second) {
		t.Fatal("flag did not clear at the ceiling")
	}
}
