package auth

import (
	"crypto/sha256"
	"sync"

	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

var (
	storeMu         sync.RWMutex
	registeredStore coreauth.Store
)

// expectedWriteTracker provides a global mechanism to prevent self-triggered fsnotify loops.
// When the token store writes to an auth file, it records the expected content hash.
// When the watcher receives fsnotify events, it checks if the event matches an expected write
// and ignores it if so. This prevents the infinite loop described in Issue #833.
var expectedWriteTracker struct {
	mu    sync.RWMutex
	track *tracker
}

type tracker struct {
	expected map[string]string // path -> hash
}

// GetExpectedWriteTracker returns the global expected write tracker.
// This is used by both the token store (to record writes) and the watcher (to filter events).
func GetExpectedWriteTracker() *tracker {
	expectedWriteTracker.mu.RLock()
	if expectedWriteTracker.track != nil {
		expectedWriteTracker.mu.RUnlock()
		return expectedWriteTracker.track
	}
	expectedWriteTracker.mu.RUnlock()

	expectedWriteTracker.mu.Lock()
	defer expectedWriteTracker.mu.Unlock()
	if expectedWriteTracker.track == nil {
		expectedWriteTracker.track = &tracker{
			expected: make(map[string]string),
		}
	}
	return expectedWriteTracker.track
}

// ExpectWrite records that we expect to write to the given path with the given content.
// This should be called before writing to the file.
func (t *tracker) ExpectWrite(path string, content []byte) {
	if t == nil || path == "" || len(content) == 0 {
		return
	}
	hash := computeContentHash(content)
	t.expected[path] = hash
}

// ConsumeIfExpected checks if the given path and content match an expected write.
// If it matches, the expectation is consumed (removed) and returns true.
// Returns false if no matching expectation exists.
func (t *tracker) ConsumeIfExpected(path string, content []byte) bool {
	if t == nil || path == "" {
		return false
	}
	hash := computeContentHash(content)
	if expected, ok := t.expected[path]; ok && expected == hash {
		delete(t.expected, path)
		return true
	}
	return false
}

// computeContentHash computes SHA256 hash for content comparison.
func computeContentHash(content []byte) string {
	if len(content) == 0 {
		return ""
	}
	sum := sha256.Sum256(content)
	return string(sum[:]) // Use first 32 bytes as key
}

// RegisterTokenStore sets the global token store used by the authentication helpers.
func RegisterTokenStore(store coreauth.Store) {
	storeMu.Lock()
	registeredStore = store
	storeMu.Unlock()
}

// GetTokenStore returns the globally registered token store.
func GetTokenStore() coreauth.Store {
	storeMu.RLock()
	s := registeredStore
	storeMu.RUnlock()
	if s != nil {
		return s
	}
	storeMu.Lock()
	defer storeMu.Unlock()
	if registeredStore == nil {
		registeredStore = NewFileTokenStore()
	}
	return registeredStore
}
