package cliproxy

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestKiroModelsCacheCoalescesConcurrentFetches verifies that a burst of concurrent
// callers (simulating a fleet of Kiro accounts registering at once) triggers exactly
// one upstream fetch, and every caller receives the shared result.
func TestKiroModelsCacheCoalescesConcurrentFetches(t *testing.T) {
	c := &kiroModelsCache{}

	var calls int32
	release := make(chan struct{})
	fetch := func() ([]*ModelInfo, error) {
		atomic.AddInt32(&calls, 1)
		<-release // hold the flight open so callers pile up behind singleflight
		return []*ModelInfo{{ID: "kiro-model"}}, nil
	}

	const callers = 50
	var wg sync.WaitGroup
	results := make([][]*ModelInfo, callers)
	oks := make([]bool, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			results[idx], oks[idx] = c.get(fetch)
		}(i)
	}

	// Give the goroutines time to block on singleflight before releasing.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected exactly 1 upstream fetch, got %d", got)
	}
	for i := 0; i < callers; i++ {
		if !oks[i] || len(results[i]) != 1 || results[i][0].ID != "kiro-model" {
			t.Fatalf("caller %d got unexpected result ok=%v models=%v", i, oks[i], results[i])
		}
	}
}

// TestKiroModelsCacheServesWithinTTL verifies a cached result is reused without a
// second fetch while the TTL is still valid, and refreshed after it expires.
func TestKiroModelsCacheServesWithinTTL(t *testing.T) {
	c := &kiroModelsCache{}

	var calls int32
	fetch := func() ([]*ModelInfo, error) {
		atomic.AddInt32(&calls, 1)
		return []*ModelInfo{{ID: "kiro-model"}}, nil
	}

	if _, ok := c.get(fetch); !ok {
		t.Fatal("first get should succeed")
	}
	if _, ok := c.get(fetch); !ok {
		t.Fatal("second get should succeed from cache")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("expected 1 fetch within TTL, got %d", got)
	}

	// Force expiry and confirm a refresh happens.
	c.mu.Lock()
	c.expiry = time.Now().Add(-time.Second)
	c.mu.Unlock()

	if _, ok := c.get(fetch); !ok {
		t.Fatal("get after expiry should succeed")
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("expected refresh after TTL expiry, got %d fetches", got)
	}
}

// TestKiroModelsCacheDoesNotCacheFailures verifies that fetch errors are not cached,
// so a later caller retries and can succeed.
func TestKiroModelsCacheDoesNotCacheFailures(t *testing.T) {
	c := &kiroModelsCache{}

	var calls int32
	fetch := func() ([]*ModelInfo, error) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			return nil, errors.New("boom")
		}
		return []*ModelInfo{{ID: "kiro-model"}}, nil
	}

	if _, ok := c.get(fetch); ok {
		t.Fatal("first get should fail and report ok=false")
	}
	models, ok := c.get(fetch)
	if !ok || len(models) != 1 {
		t.Fatalf("second get should retry and succeed, ok=%v models=%v", ok, models)
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("expected 2 fetches (failure then retry), got %d", got)
	}
}

// TestKiroModelsCacheDoesNotCacheEmpty verifies that a successful-but-empty fetch is
// not cached, allowing a later caller to retry.
func TestKiroModelsCacheDoesNotCacheEmpty(t *testing.T) {
	c := &kiroModelsCache{}

	var calls int32
	fetch := func() ([]*ModelInfo, error) {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			return nil, nil // empty catalog
		}
		return []*ModelInfo{{ID: "kiro-model"}}, nil
	}

	if _, ok := c.get(fetch); ok {
		t.Fatal("empty fetch should report ok=false")
	}
	if _, ok := c.get(fetch); !ok {
		t.Fatal("second get should retry and succeed")
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Fatalf("expected 2 fetches (empty then retry), got %d", got)
	}
}
