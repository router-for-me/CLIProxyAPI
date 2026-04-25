package executor

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
)

func TestDeferFailureWithoutScopePublishesImmediately(t *testing.T) {
	called := false
	if DeferFailure(context.Background(), func(context.Context) { called = true }) {
		t.Fatal("expected failure not to be deferred without a scope")
	}
	if called {
		t.Fatal("deferred publisher should not be called by DeferFailure")
	}
}

func TestDeferredFailureFlushPublishesLatestFailure(t *testing.T) {
	ctx, deferred := WithDeferredFailure(context.Background())
	var published []string

	if !DeferFailure(ctx, func(context.Context) { published = append(published, "first") }) {
		t.Fatal("expected first failure to be deferred")
	}
	if !DeferFailure(ctx, func(context.Context) { published = append(published, "last") }) {
		t.Fatal("expected last failure to be deferred")
	}

	deferred.Flush(ctx)
	if len(published) != 1 || published[0] != "last" {
		t.Fatalf("published = %v, want [last]", published)
	}
}

func TestDeferredFailureDiscardSuppressesFailure(t *testing.T) {
	ctx, deferred := WithDeferredFailure(context.Background())
	var called atomic.Bool

	if !DeferFailure(ctx, func(context.Context) { called.Store(true) }) {
		t.Fatal("expected failure to be deferred")
	}
	deferred.Discard()
	deferred.Flush(ctx)

	if called.Load() {
		t.Fatal("discarded failure was published")
	}
}

func TestDeferredFailureAfterCloseDoesNotDefer(t *testing.T) {
	ctx, deferred := WithDeferredFailure(context.Background())
	deferred.Flush(ctx)

	if DeferFailure(ctx, func(context.Context) {}) {
		t.Fatal("expected closed scope not to defer failures")
	}
}

func TestDeferredFailureConcurrentFlushPublishesAtMostOnce(t *testing.T) {
	ctx, deferred := WithDeferredFailure(context.Background())
	var published atomic.Int32
	var wg sync.WaitGroup

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = DeferFailure(ctx, func(context.Context) {
				published.Add(1)
			})
		}()
	}
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			deferred.Flush(ctx)
		}()
	}
	wg.Wait()
	deferred.Flush(ctx)

	if got := published.Load(); got > 1 {
		t.Fatalf("published count = %d, want at most 1", got)
	}
}
