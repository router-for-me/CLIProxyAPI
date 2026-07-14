package auth

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

type stopCountingSelector struct {
	RoundRobinSelector
	stops atomic.Int32
}

func (s *stopCountingSelector) Stop() {
	s.stops.Add(1)
}

// mapSelector is intentionally non-comparable (named map type) to ensure
// SetSelector does not panic on interface equality.
type mapSelector map[string]struct{}

func (mapSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	return (&RoundRobinSelector{}).Pick(ctx, provider, model, opts, auths)
}

// stoppableMapSelector is a non-comparable (named map type) selector that also
// implements Stop, to ensure use-tracking never indexes a map with an
// unhashable key ("hash of unhashable type").
type stoppableMapSelector map[string]struct{}

func (stoppableMapSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	return (&RoundRobinSelector{}).Pick(ctx, provider, model, opts, auths)
}

func (stoppableMapSelector) Stop() {}

func waitForStopCount(t *testing.T, s *stopCountingSelector, want int32) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s.stops.Load() == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("Stop calls = %d, want %d", s.stops.Load(), want)
}

func TestManagerSetSelectorStopsPreviousStoppableSelector(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	first := &stopCountingSelector{}
	second := &stopCountingSelector{}

	manager.SetSelector(first)
	manager.SetSelector(second)
	waitForStopCount(t, first, 1)

	if got := second.stops.Load(); got != 0 {
		t.Fatalf("second selector Stop calls = %d, want 0", got)
	}

	// Replacing with a non-stoppable selector should still stop the previous one.
	manager.SetSelector(&RoundRobinSelector{})
	waitForStopCount(t, second, 1)
}

func TestManagerSetSelectorDoesNotPanicOnNonComparableSelector(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	first := &stopCountingSelector{}
	manager.SetSelector(first)

	// Replacing with a non-comparable concrete type must not panic and must stop previous.
	manager.SetSelector(mapSelector{})
	waitForStopCount(t, first, 1)

	// Replacing non-comparable with another non-comparable must also not panic.
	manager.SetSelector(mapSelector{"x": {}})
	manager.SetSelector(&RoundRobinSelector{})
}

// funcSelector is a function-typed Selector used to prove selectorSameInstance
// never conflates distinct closures built from the same function literal (Go
// gives no reliable identity to function values).
type funcSelector func(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error)

func (f funcSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	return f(ctx, provider, model, opts, auths)
}

func TestSelectorSameInstancePointerIdentity(t *testing.T) {
	s := &RoundRobinSelector{}
	if !selectorSameInstance(s, s) {
		t.Fatal("same pointer should be same instance")
	}
	if selectorSameInstance(s, &RoundRobinSelector{}) {
		t.Fatal("different pointers should not be same instance")
	}
	if selectorSameInstance(mapSelector{}, mapSelector{}) {
		t.Fatal("distinct non-comparable values should not report same instance")
	}
}

// sliceSelector is a named slice-typed selector used to verify identity
// accounts for capacity, not just pointer and length.
type sliceSelector []int

func (sliceSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	return (&RoundRobinSelector{}).Pick(ctx, provider, model, opts, auths)
}

func TestSelectorSameInstanceSliceCapacity(t *testing.T) {
	base := make(sliceSelector, 4)
	a := base[:1:1]
	b := base[:1:2]
	// Same backing array and length but different capacity — a Pick can observe
	// cap, so these must be treated as different instances.
	if selectorSameInstance(a, b) {
		t.Fatal("slice selectors with different capacities must not be same instance")
	}
	if !selectorSameInstance(a, a) {
		t.Fatal("identical slice header must be same instance")
	}
}

func TestSelectorSameInstanceFuncSelectorsNeverSame(t *testing.T) {
	build := func() funcSelector {
		return funcSelector(func(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
			return nil, nil
		})
	}
	// Two closures from the same literal can share a code pointer; they must not
	// be treated as the same instance, otherwise SetSelector would drop the new
	// closure and keep stale routing state.
	if selectorSameInstance(build(), build()) {
		t.Fatal("distinct func selectors must not report same instance")
	}
}

func TestManagerSetSelectorWaitsForInflightSelectorUse(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	first := &stopCountingSelector{}
	manager.SetSelector(first)

	counter := manager.acquireSelectorUse()
	if counter == nil {
		t.Fatal("expected in-flight counter for stoppable selector")
	}

	manager.SetSelector(&RoundRobinSelector{})
	// Give the async retire path time to observe a non-zero use count.
	time.Sleep(30 * time.Millisecond)
	if got := first.stops.Load(); got != 0 {
		t.Fatalf("Stop called while selector still in use: stops=%d", got)
	}

	manager.releaseSelectorUse(counter)
	waitForStopCount(t, first, 1)
}

func TestManagerSetSelectorSameInstanceDoesNotStopOrDropTracking(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	first := &stopCountingSelector{}
	manager.SetSelector(first)

	counter := manager.acquireSelectorUse()
	if counter == nil {
		t.Fatal("expected in-flight counter")
	}

	// Same instance must not stop or reset tracking.
	manager.SetSelector(first)
	time.Sleep(20 * time.Millisecond)
	if got := first.stops.Load(); got != 0 {
		t.Fatalf("Stop called on same-instance SetSelector: stops=%d", got)
	}

	// Replacement must wait for the in-flight use still held.
	manager.SetSelector(&RoundRobinSelector{})
	time.Sleep(30 * time.Millisecond)
	if got := first.stops.Load(); got != 0 {
		t.Fatalf("Stop called while still in use: stops=%d", got)
	}
	manager.releaseSelectorUse(counter)
	waitForStopCount(t, first, 1)
}

func TestManagerSetSelectorReleaseThenReacquireStillWaits(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	first := &stopCountingSelector{}
	manager.SetSelector(first)

	counter := manager.acquireSelectorUse()
	manager.releaseSelectorUse(counter)
	// Counter may be zero but must remain reachable for a re-acquire.
	counter = manager.acquireSelectorUse()
	if counter == nil {
		t.Fatal("in-flight counter unreachable after release/re-acquire")
	}

	manager.SetSelector(&RoundRobinSelector{})
	time.Sleep(30 * time.Millisecond)
	if got := first.stops.Load(); got != 0 {
		t.Fatalf("Stop called after release/re-acquire while still held: stops=%d", got)
	}
	manager.releaseSelectorUse(counter)
	waitForStopCount(t, first, 1)
}

func TestManagerSetSelectorDoesNotStopReinstalledSelector(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	first := &stopCountingSelector{}
	manager.SetSelector(first)

	counter := manager.acquireSelectorUse()

	// A -> B queues stop of A after its picks drain.
	manager.SetSelector(&RoundRobinSelector{})
	// A reinstalled before drain completes.
	manager.SetSelector(first)
	manager.releaseSelectorUse(counter)

	// Async retire must skip Stop because A is current again.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if first.stops.Load() != 0 {
			t.Fatalf("reinstalled selector was Stopped: stops=%d", first.stops.Load())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestManagerSetSelectorStopsAfterMultiGenerationDrain(t *testing.T) {
	// A -> B -> A -> C: A spans two installs. A must be Stopped only after every
	// pick that borrowed it (across both installs) returns, and exactly once.
	manager := NewManager(nil, nil, nil)
	a := &stopCountingSelector{}
	manager.SetSelector(a)
	firstUse := manager.acquireSelectorUse() // borrowed during install #1

	manager.SetSelector(&RoundRobinSelector{}) // A -> B, retire(A) waits
	manager.SetSelector(a)                     // B -> A, reinstalled
	secondUse := manager.acquireSelectorUse()  // borrowed during install #2

	manager.SetSelector(&stopCountingSelector{}) // A -> C, retire(A) again

	// Release the first install's borrow; the second is still outstanding, so A
	// must not be Stopped yet.
	manager.releaseSelectorUse(firstUse)
	time.Sleep(30 * time.Millisecond)
	if got := a.stops.Load(); got != 0 {
		t.Fatalf("A Stopped while a pick from install #2 was still in flight: stops=%d", got)
	}

	manager.releaseSelectorUse(secondUse)
	waitForStopCount(t, a, 1)
	// Ensure no double-stop from the two retire goroutines.
	time.Sleep(30 * time.Millisecond)
	if got := a.stops.Load(); got != 1 {
		t.Fatalf("A Stop count = %d, want exactly 1", got)
	}
}

// countingFuncSelector is a func-typed StoppableSelector: Go gives func values
// no reliable identity, so tracking must not rely on comparing selector values.
var countingFuncSelectorStops atomic.Int32

type countingFuncSelector func(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error)

func (countingFuncSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	return nil, nil
}

func (countingFuncSelector) Stop() { countingFuncSelectorStops.Add(1) }

func TestManagerRetiresFuncTypedStoppableSelector(t *testing.T) {
	countingFuncSelectorStops.Store(0)
	manager := NewManager(nil, nil, nil)
	fn := countingFuncSelector(func(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
		return nil, nil
	})
	manager.SetSelector(fn)

	// In-flight picks on a func-typed selector must be tracked (previously the
	// value-lookup returned nil for funcs, so they were never tracked or stopped).
	manager.mu.RLock()
	entry := manager.acquireSelectorUse()
	manager.mu.RUnlock()
	if entry == nil {
		t.Fatal("func-typed stoppable selector must be tracked")
	}

	manager.SetSelector(&RoundRobinSelector{})
	time.Sleep(30 * time.Millisecond)
	if got := countingFuncSelectorStops.Load(); got != 0 {
		t.Fatalf("Stop called while func selector still in use: stops=%d", got)
	}
	manager.releaseSelectorUse(entry)

	// Must be Stopped exactly once after drain, with its tracking entry removed.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && countingFuncSelectorStops.Load() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if got := countingFuncSelectorStops.Load(); got != 1 {
		t.Fatalf("func selector Stop count = %d, want 1", got)
	}
	manager.mu.RLock()
	leaked := len(manager.selectorUses)
	manager.mu.RUnlock()
	if leaked != 0 {
		t.Fatalf("selectorUses leaked %d entries after retirement", leaked)
	}
}

func TestManagerTracksSelectorInstalledByNewManager(t *testing.T) {
	// A stoppable selector passed to NewManager (e.g. startup session affinity)
	// must be retired on the first replacement, not treated as already-retired.
	initial := &stopCountingSelector{}
	manager := NewManager(nil, initial, nil)

	manager.SetSelector(&RoundRobinSelector{})
	waitForStopCount(t, initial, 1)
}

func TestManagerSetSelectorHandlesNonComparableStoppableSelector(t *testing.T) {
	manager := NewManager(nil, nil, nil)

	// Installing and looking up a non-comparable stoppable selector must not
	// panic with "hash of unhashable type".
	first := stoppableMapSelector{}
	manager.SetSelector(first)

	manager.mu.RLock()
	entry := manager.acquireSelectorUse()
	manager.mu.RUnlock()
	if entry == nil {
		t.Fatal("expected tracking entry for non-comparable stoppable selector")
	}
	manager.releaseSelectorUse(entry)

	// The retire path (which used to index a map keyed by the selector) must
	// also not panic.
	manager.SetSelector(&RoundRobinSelector{})
	manager.SetSelector(stoppableMapSelector{})
	manager.SetSelector(&RoundRobinSelector{})
}

func TestManagerRetireClearsVacatedSlot(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	first := &stopCountingSelector{}
	manager.SetSelector(first)
	manager.SetSelector(&RoundRobinSelector{})
	waitForStopCount(t, first, 1)

	manager.mu.RLock()
	defer manager.mu.RUnlock()
	if len(manager.selectorUses) != 0 {
		t.Fatalf("selectorUses len = %d, want 0", len(manager.selectorUses))
	}
	// The vacated backing-array slot must be niled so the retired selector (and
	// its SessionCache) is not retained for the manager's lifetime.
	backing := manager.selectorUses[:cap(manager.selectorUses)]
	for i, entry := range backing {
		if entry != nil {
			t.Fatalf("backing slot %d retains a stale entry after retirement", i)
		}
	}
}

// invalidatingSelector implements both StoppableSelector and InvalidateAuth; its
// InvalidateAuth blocks until released to exercise the drain accounting.
type invalidatingSelector struct {
	RoundRobinSelector
	stops   atomic.Int32
	started chan struct{}
	release chan struct{}
}

func (s *invalidatingSelector) Stop() { s.stops.Add(1) }

func (s *invalidatingSelector) InvalidateAuth(string) {
	close(s.started)
	<-s.release
}

func TestManagerInvalidateSessionAffinityBlocksRetireStop(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	sel := &invalidatingSelector{started: make(chan struct{}), release: make(chan struct{})}
	manager.SetSelector(sel)

	go manager.invalidateSessionAffinity("auth-1")
	<-sel.started // InvalidateAuth is running and holding the borrow

	manager.SetSelector(&RoundRobinSelector{}) // queues retire(sel)
	time.Sleep(40 * time.Millisecond)
	if got := sel.stops.Load(); got != 0 {
		t.Fatalf("selector Stopped while InvalidateAuth was in flight: stops=%d", got)
	}

	close(sel.release) // InvalidateAuth returns, releasing the borrow
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && sel.stops.Load() == 0 {
		time.Sleep(5 * time.Millisecond)
	}
	if got := sel.stops.Load(); got != 1 {
		t.Fatalf("selector Stop count = %d, want 1 after InvalidateAuth returns", got)
	}
}

func TestManagerStopAutoRefreshThenSetSelectorNoDoubleStop(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	first := &stopCountingSelector{}
	manager.SetSelector(first)

	manager.StopAutoRefresh() // stops first exactly once and claims its entry
	waitForStopCount(t, first, 1)

	// A later SetSelector must not queue a second Stop on the already-stopped
	// selector (Stop is not required to be idempotent).
	manager.SetSelector(&RoundRobinSelector{})
	time.Sleep(50 * time.Millisecond)
	if got := first.stops.Load(); got != 1 {
		t.Fatalf("first Stop count = %d, want exactly 1 (no double-stop)", got)
	}
}

func TestSessionCacheStopIdempotentConcurrent(t *testing.T) {
	c := NewSessionCache(time.Minute)
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c.Stop()
		}()
	}
	wg.Wait() // must not panic on double close(stopCh)
}
