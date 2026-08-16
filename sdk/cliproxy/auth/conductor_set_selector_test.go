package auth

import (
	"sync"
	"testing"
	"time"
)

// SetSelector never stops the selector it replaces: selectors are owned by
// whoever created them (the SDK caller or the Service's config path), and
// implicitly stopping one breaks compositions that retain it, such as a
// custom wrapper delegating to the previous selector.
func TestManagerSetSelectorDoesNotStopReplacedSelector(t *testing.T) {
	t.Parallel()

	affinity := NewSessionAffinitySelector(&RoundRobinSelector{})
	defer affinity.Stop()
	manager := NewManager(nil, affinity, nil)

	manager.SetSelector(&RoundRobinSelector{})
	select {
	case <-affinity.cache.stopCh:
		t.Fatal("SetSelector stopped the caller-owned affinity selector")
	default:
	}
	if current := manager.Selector(); current == Selector(affinity) {
		t.Fatal("SetSelector did not swap the active selector")
	}
}

// SessionCache.Stop must be safe to invoke concurrently: the Service's config
// path, manager shutdown, and SDK caller cleanup can all stop the same
// selector. A select-then-close idiom would double-close the channel and
// panic.
func TestSessionCacheStopIsConcurrencySafe(t *testing.T) {
	t.Parallel()

	cache := NewSessionCache(time.Minute)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cache.Stop()
		}()
	}
	wg.Wait()
}

// StopSelectorIfInactive retires an owned selector only when it is no longer
// active: a concurrent SetSelector re-installing the same instance must win,
// otherwise the republished selector's cleanup goroutine would be stopped
// underneath it.
func TestManagerStopSelectorIfInactiveSkipsActiveSelector(t *testing.T) {
	t.Parallel()

	affinity := NewSessionAffinitySelector(&RoundRobinSelector{})
	defer affinity.Stop()
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.SetSelector(affinity)

	manager.StopSelectorIfInactive(affinity)
	select {
	case <-affinity.cache.stopCh:
		t.Fatal("active selector was stopped")
	default:
	}
}

func TestManagerStopSelectorIfInactiveStopsReplacedSelector(t *testing.T) {
	t.Parallel()

	affinity := NewSessionAffinitySelector(&RoundRobinSelector{})
	manager := NewManager(nil, affinity, nil)
	manager.SetSelector(&RoundRobinSelector{})

	manager.StopSelectorIfInactive(affinity)
	select {
	case <-affinity.cache.stopCh:
	default:
		t.Fatal("inactive selector was not stopped")
	}
}

// A stopped cache must stay paused on writes: in-flight picks on a retired
// selector call SetAliases, and resuming there would leak one cleanup
// goroutine per reload under traffic. Only an explicit Resume (issued when a
// selector is installed again) restarts cleanup.
func TestSessionCacheStopStaysPausedUntilResume(t *testing.T) {
	t.Parallel()

	cache := NewSessionCache(time.Minute)
	firstStopCh := cache.stopCh
	cache.Stop()
	select {
	case <-firstStopCh:
	default:
		t.Fatal("first Stop did not close stopCh")
	}

	cache.Set("session-1", "auth-1")
	if cache.stopCh != firstStopCh {
		t.Fatal("write after Stop resumed cleanup; only Resume may restart it")
	}

	cache.Resume()
	defer cache.Stop()
	if cache.stopCh == firstStopCh {
		t.Fatal("Resume did not restart cleanup")
	}
	select {
	case <-cache.stopCh:
		t.Fatal("resumed cleanup channel is already closed")
	default:
	}
}

// SetSelector must resume a selector that was stopped while retired, so
// re-installing it restores its expiration sweeps.
func TestManagerSetSelectorResumesResumableSelector(t *testing.T) {
	t.Parallel()

	affinity := NewSessionAffinitySelector(&RoundRobinSelector{})
	manager := NewManager(nil, affinity, nil)
	manager.SetSelector(&RoundRobinSelector{})
	manager.StopSelectorIfInactive(affinity)
	stoppedCh := affinity.cache.stopCh
	select {
	case <-stoppedCh:
	default:
		t.Fatal("retired selector was not stopped")
	}

	manager.SetSelector(affinity)
	if affinity.cache.stopCh == stoppedCh {
		t.Fatal("re-installed selector was not resumed")
	}
	select {
	case <-affinity.cache.stopCh:
		t.Fatal("resumed selector cleanup channel is already closed")
	default:
	}
}
