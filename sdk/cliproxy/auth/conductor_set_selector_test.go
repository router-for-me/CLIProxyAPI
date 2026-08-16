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
