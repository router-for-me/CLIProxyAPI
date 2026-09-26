package keepalive

import "sync"

var (
	defaultMu        sync.RWMutex
	defaultScheduler *Scheduler
)

// Default returns the process-wide scheduler, or nil when the runtime has not
// installed one. Every Scheduler method tolerates a nil receiver, so callers on
// the request path can call Observe unconditionally.
func Default() *Scheduler {
	defaultMu.RLock()
	defer defaultMu.RUnlock()
	return defaultScheduler
}

// SetDefault installs the process-wide scheduler and stops the one it replaces.
func SetDefault(scheduler *Scheduler) {
	defaultMu.Lock()
	previous := defaultScheduler
	defaultScheduler = scheduler
	defaultMu.Unlock()
	if previous != nil && previous != scheduler {
		previous.Stop()
	}
}
