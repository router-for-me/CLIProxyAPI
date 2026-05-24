package registry

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func makeFetcher(models []*ModelInfo, err error, callCount *int64) PerAuthModelFetcher {
	return func(_ context.Context) ([]*ModelInfo, error) {
		if callCount != nil {
			atomic.AddInt64(callCount, 1)
		}
		return models, err
	}
}

func TestPerAuthModelCache_LazyFetch(t *testing.T) {
	var calls int64
	m := &ModelInfo{ID: "grok-code-fast-1", Type: "grok"}
	fetcher := makeFetcher([]*ModelInfo{m}, nil, &calls)

	c := NewPerAuthModelCache(time.Hour, func() []*ModelInfo { return nil })

	// First call — fetcher must be invoked.
	got, err := c.GetOrFetch(context.Background(), "auth-1", false, fetcher)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].ID != "grok-code-fast-1" {
		t.Fatalf("unexpected models: %v", got)
	}
	if atomic.LoadInt64(&calls) != 1 {
		t.Fatalf("expected 1 fetcher call, got %d", calls)
	}

	// Second call within TTL — fetcher must NOT be invoked.
	got2, err := c.GetOrFetch(context.Background(), "auth-1", false, fetcher)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got2) != 1 || got2[0].ID != "grok-code-fast-1" {
		t.Fatalf("unexpected models on second call: %v", got2)
	}
	if atomic.LoadInt64(&calls) != 1 {
		t.Fatalf("expected still 1 fetcher call, got %d", calls)
	}
}

func TestPerAuthModelCache_RespectsLocalModelOnly(t *testing.T) {
	var calls int64
	fetcher := makeFetcher([]*ModelInfo{{ID: "grok-4-fast"}}, nil, &calls)
	fallback := []*ModelInfo{{ID: "fallback-model"}}
	c := NewPerAuthModelCache(time.Hour, func() []*ModelInfo { return fallback })

	got, err := c.GetOrFetch(context.Background(), "auth-1", true, fetcher)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].ID != "fallback-model" {
		t.Fatalf("expected fallback model, got: %v", got)
	}
	if atomic.LoadInt64(&calls) != 0 {
		t.Fatalf("fetcher must not be called with localModelOnly=true, got %d calls", calls)
	}
}

func TestPerAuthModelCache_TTLExpiry(t *testing.T) {
	var calls int64
	m := &ModelInfo{ID: "grok-code-fast-1", Type: "grok"}
	fetcher := makeFetcher([]*ModelInfo{m}, nil, &calls)

	// Use a controllable clock.
	var fakeNow int64 = 1000 // seconds since epoch (arbitrary base)
	clock := func() time.Time {
		return time.Unix(atomic.LoadInt64(&fakeNow), 0)
	}

	c := NewPerAuthModelCache(10*time.Millisecond, func() []*ModelInfo { return nil }, WithClock(clock))

	// First call — populates cache at fakeNow=1000, expires at 1000+0.01s≈1000 in unix seconds.
	// Use a proper duration: TTL=1s so we can advance the clock clearly.
	c2 := NewPerAuthModelCache(time.Second, func() []*ModelInfo { return nil }, WithClock(clock))

	_, _ = c2.GetOrFetch(context.Background(), "auth-1", false, fetcher)
	if atomic.LoadInt64(&calls) != 1 {
		t.Fatalf("expected 1 call after first fetch, got %d", calls)
	}

	// Within TTL — no additional call.
	_, _ = c2.GetOrFetch(context.Background(), "auth-1", false, fetcher)
	if atomic.LoadInt64(&calls) != 1 {
		t.Fatalf("expected still 1 call within TTL, got %d", calls)
	}

	// Advance clock past TTL.
	atomic.StoreInt64(&fakeNow, 1002) // 2 seconds ahead, past 1s TTL

	_, _ = c2.GetOrFetch(context.Background(), "auth-1", false, fetcher)
	if atomic.LoadInt64(&calls) != 2 {
		t.Fatalf("expected 2 calls after TTL expiry, got %d", calls)
	}

	_ = c // suppress unused warning
}

func TestPerAuthModelCache_FetcherFailureUsesFallbackWithoutCaching(t *testing.T) {
	var calls int64
	fetchErr := errors.New("upstream unavailable")
	fetcher := makeFetcher(nil, fetchErr, &calls)
	fallback := []*ModelInfo{{ID: "fallback-model"}}
	c := NewPerAuthModelCache(time.Hour, func() []*ModelInfo { return fallback })

	// First call — fetcher errors, fallback returned.
	got, err := c.GetOrFetch(context.Background(), "auth-1", false, fetcher)
	if err == nil {
		t.Fatal("expected error to be propagated")
	}
	if len(got) != 1 || got[0].ID != "fallback-model" {
		t.Fatalf("expected fallback on error, got: %v", got)
	}
	if atomic.LoadInt64(&calls) != 1 {
		t.Fatalf("expected 1 fetcher call, got %d", calls)
	}

	// Second call — failure must NOT be cached; fetcher invoked again.
	_, _ = c.GetOrFetch(context.Background(), "auth-1", false, fetcher)
	if atomic.LoadInt64(&calls) != 2 {
		t.Fatalf("expected 2 fetcher calls (failure not cached), got %d", calls)
	}
}

func TestPerAuthModelCache_Invalidate(t *testing.T) {
	var calls int64
	m := &ModelInfo{ID: "grok-code-fast-1", Type: "grok"}
	fetcher := makeFetcher([]*ModelInfo{m}, nil, &calls)
	c := NewPerAuthModelCache(time.Hour, func() []*ModelInfo { return nil })

	// Populate cache.
	_, _ = c.GetOrFetch(context.Background(), "auth-1", false, fetcher)
	if atomic.LoadInt64(&calls) != 1 {
		t.Fatalf("expected 1 call, got %d", calls)
	}

	// Invalidate.
	c.Invalidate("auth-1")

	// Next call must invoke fetcher again.
	_, _ = c.GetOrFetch(context.Background(), "auth-1", false, fetcher)
	if atomic.LoadInt64(&calls) != 2 {
		t.Fatalf("expected 2 calls after Invalidate, got %d", calls)
	}
}

func TestUnregisterAuth_InvalidatesBothRegistries(t *testing.T) {
	const authID = "test-unregister-auth-both"

	// Register a model in the global model registry.
	reg := GetGlobalRegistry()
	reg.RegisterClient(authID, "grok", []*ModelInfo{{ID: "grok-code-fast-1", Type: "grok"}})

	// Populate the per-auth cache.
	cache := NewPerAuthModelCache(time.Hour, func() []*ModelInfo { return nil })
	var fetched int64
	fetcher := makeFetcher([]*ModelInfo{{ID: "grok-4-fast"}}, nil, &fetched)
	_, _ = cache.GetOrFetch(context.Background(), authID, false, fetcher)
	if atomic.LoadInt64(&fetched) != 1 {
		t.Fatal("fetcher should have been called once to populate cache")
	}

	// Verify the global registry has the client before unregistration.
	if reg.GetModelCount("grok-code-fast-1") == 0 {
		t.Fatal("expected model to be registered before UnregisterAuth")
	}

	// Call UnregisterAuth — must clear both.
	reg.UnregisterClient(authID)
	cache.Invalidate(authID)

	// Global registry: model count should be 0.
	if reg.GetModelCount("grok-code-fast-1") != 0 {
		t.Fatal("expected model count 0 after UnregisterAuth")
	}

	// Per-auth cache: next fetch should call fetcher again (entry was evicted).
	_, _ = cache.GetOrFetch(context.Background(), authID, false, fetcher)
	if atomic.LoadInt64(&fetched) != 2 {
		t.Fatalf("expected 2 fetcher calls after cache invalidation, got %d", fetched)
	}
}
