package auth

import (
	"sync"
	"testing"
	"time"
)

func TestSessionCache_ConcurrentStop(t *testing.T) {
	cache := NewSessionCache(10 * time.Minute)
	const goroutines = 50

	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func() {
			defer wg.Done()
			cache.Stop()
		}()
	}
	wg.Wait()

	select {
	case <-cache.stopCh:
		// Expected: channel is closed
	default:
		t.Fatal("expected stopCh to be closed")
	}

	// Additional call after goroutines finish should also be safe and non-panicking
	cache.Stop()
}
