package cachestats

import "sync"

// The process-wide store the usage hook feeds and the management API reads.
var (
	defaultMu    sync.RWMutex
	defaultStore = NewStore(Config{})
)

// Default returns the process-wide store. It is never nil.
func Default() *Store {
	defaultMu.RLock()
	defer defaultMu.RUnlock()
	return defaultStore
}

// SetDefault replaces the process-wide store. A nil store installs an empty,
// disabled one so callers never have to nil-check.
func SetDefault(store *Store) {
	if store == nil {
		store = NewStore(Config{})
	}
	defaultMu.Lock()
	defaultStore = store
	defaultMu.Unlock()
}

// Record ingests one observation into the process-wide store.
func Record(observation Observation) { Default().Record(observation) }
