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
	expected map[string]string // normalized path -> expected content hash
}

// NewExpectedWriteTracker creates a new tracker instance.
func NewExpectedWriteTracker() *ExpectedWriteTracker {
	return &ExpectedWriteTracker{
		expected: make(map[string]string),
	}
}

// ExpectContent registers an expected file content hash.
// Call this BEFORE writing the file.
// This replaces any previous hash for the path, ensuring that:
// 1. Multiple fsnotify events from the same write all match
// 2. Old hashes are automatically cleaned up on new writes
func (t *ExpectedWriteTracker) ExpectContent(normalizedPath string, content []byte) {
	if t == nil {
		return
	}
	hash := computeContentHash(content)

	t.mu.Lock()
	defer t.mu.Unlock()

	// Replace: only keep the latest hash for this path.
	t.expected[normalizedPath] = hash
}

// ConsumeIfExpected checks if the content hash was expected.
// Returns true if this was our own write, false if this was an external change.
// The hash is NOT removed on match, allowing multiple fsnotify events from
// the same write operation (e.g., WRITE then CREATE) to all be recognized
// as self-triggered. The hash is only replaced when a new ExpectContent call
// occurs for the same path.
func (t *ExpectedWriteTracker) ConsumeIfExpected(normalizedPath string, content []byte) bool {
	if t == nil {
		return false
	}
	hash := computeContentHash(content)

	t.mu.Lock()
	defer t.mu.Unlock()

	if expected, ok := t.expected[normalizedPath]; ok && expected == hash {
		// Match found - do NOT delete the hash.
		// Multiple events from the same write should all match.
		return true
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
