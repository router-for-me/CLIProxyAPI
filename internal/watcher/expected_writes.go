// expected_writes.go tracks file content hashes that we expect to see
// from our own writes. This allows the watcher to skip processing
// self-triggered fsnotify events without relying on timing.
package watcher

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
)

// ExpectedWriteTracker tracks expected file content hashes.
// When filestore writes a file, it registers the content hash here.
// When watcher receives an event, it checks if the hash matches an expected write.
type ExpectedWriteTracker struct {
	mu       sync.Mutex
	expected map[string]map[string]struct{} // normalized path -> set of expected hashes
}

// NewExpectedWriteTracker creates a new tracker instance.
func NewExpectedWriteTracker() *ExpectedWriteTracker {
	return &ExpectedWriteTracker{
		expected: make(map[string]map[string]struct{}),
	}
}

// ExpectContent registers an expected file content hash.
// Call this BEFORE writing the file.
func (t *ExpectedWriteTracker) ExpectContent(normalizedPath string, content []byte) {
	if t == nil {
		return
	}
	hash := computeContentHash(content)

	t.mu.Lock()
	defer t.mu.Unlock()

	if t.expected[normalizedPath] == nil {
		t.expected[normalizedPath] = make(map[string]struct{})
	}
	t.expected[normalizedPath][hash] = struct{}{}
}

// ConsumeIfExpected checks if the content hash was expected.
// Returns true if this was our own write (and consumes the expectation).
// Returns false if this was an external change.
func (t *ExpectedWriteTracker) ConsumeIfExpected(normalizedPath string, content []byte) bool {
	if t == nil {
		return false
	}
	hash := computeContentHash(content)

	t.mu.Lock()
	defer t.mu.Unlock()

	if hashes, ok := t.expected[normalizedPath]; ok {
		if _, exists := hashes[hash]; exists {
			// Found expected hash - consume it
			delete(hashes, hash)
			if len(hashes) == 0 {
				delete(t.expected, normalizedPath)
			}
			return true
		}
	}
	return false
}

// Clear removes all expected hashes for a path.
func (t *ExpectedWriteTracker) Clear(normalizedPath string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	delete(t.expected, normalizedPath)
	t.mu.Unlock()
}

// computeContentHash calculates SHA256 hash of content.
func computeContentHash(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}
