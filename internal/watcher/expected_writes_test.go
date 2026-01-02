package watcher

import (
	"sync"
	"testing"
)

const (
	testPathCredentials = "auth/credentials.json"
	testPathConfig      = "config/settings.json"
)

// TestExpectedWriteTracker_ExpectContent verifies that ExpectContent correctly adds a hash
func TestExpectedWriteTracker_ExpectContent(t *testing.T) {
	tracker := NewExpectedWriteTracker()
	content := []byte(`{"key": "value"}`)

	tracker.ExpectContent(testPathCredentials, content)

	// Verify hash was added by consuming it
	if !tracker.ConsumeIfExpected(testPathCredentials, content) {
		t.Error("ExpectContent should have added the hash")
	}
}

// TestExpectedWriteTracker_ConsumeIfExpected_Match verifies true is returned for expected hash
func TestExpectedWriteTracker_ConsumeIfExpected_Match(t *testing.T) {
	tracker := NewExpectedWriteTracker()
	content := []byte(`{"key": "value"}`)

	tracker.ExpectContent(testPathCredentials, content)

	if !tracker.ConsumeIfExpected(testPathCredentials, content) {
		t.Error("Should return true for expected hash")
	}
}

// TestExpectedWriteTracker_ConsumeIfExpected_NoMatch verifies false is returned for unexpected hash
func TestExpectedWriteTracker_ConsumeIfExpected_NoMatch(t *testing.T) {
	tracker := NewExpectedWriteTracker()
	content1 := []byte(`{"key": "value1"}`)
	content2 := []byte(`{"key": "value2"}`)

	tracker.ExpectContent(testPathCredentials, content1)

	if tracker.ConsumeIfExpected(testPathCredentials, content2) {
		t.Error("Should return false for unexpected hash")
	}
}

// TestExpectedWriteTracker_ConsumeIfExpected_RemovesHash verifies hash is removed after consumption
func TestExpectedWriteTracker_ConsumeIfExpected_RemovesHash(t *testing.T) {
	tracker := NewExpectedWriteTracker()
	content := []byte(`{"key": "value"}`)

	tracker.ExpectContent(testPathCredentials, content)

	// First consume should succeed
	if !tracker.ConsumeIfExpected(testPathCredentials, content) {
		t.Error("First consume should succeed")
	}

	// Second consume should fail - hash already consumed
	if tracker.ConsumeIfExpected(testPathCredentials, content) {
		t.Error("Second consume should fail - hash already consumed")
	}
}

// TestExpectedWriteTracker_ConsumeIfExpected_CleansUpEmptyMaps verifies map cleanup when all hashes consumed
func TestExpectedWriteTracker_ConsumeIfExpected_CleansUpEmptyMaps(t *testing.T) {
	tracker := NewExpectedWriteTracker()
	content := []byte(`{"key": "value"}`)

	tracker.ExpectContent(testPathCredentials, content)
	tracker.ConsumeIfExpected(testPathCredentials, content)

	// Verify internal map is cleaned up
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	if _, exists := tracker.expected[testPathCredentials]; exists {
		t.Error("Path should be removed from map when all hashes consumed")
	}
}

// TestExpectedWriteTracker_MultipleHashesSamePath verifies multiple hashes can be tracked for same path
func TestExpectedWriteTracker_MultipleHashesSamePath(t *testing.T) {
	tracker := NewExpectedWriteTracker()
	content1 := []byte(`{"version": 1}`)
	content2 := []byte(`{"version": 2}`)

	tracker.ExpectContent(testPathCredentials, content1)
	tracker.ExpectContent(testPathCredentials, content2)

	// Both hashes should be consumable
	if !tracker.ConsumeIfExpected(testPathCredentials, content1) {
		t.Error("Should consume first hash")
	}
	if !tracker.ConsumeIfExpected(testPathCredentials, content2) {
		t.Error("Should consume second hash")
	}
}

// TestExpectedWriteTracker_Clear verifies Clear removes all expected hashes for a path
func TestExpectedWriteTracker_Clear(t *testing.T) {
	tracker := NewExpectedWriteTracker()
	content := []byte(`{"key": "value"}`)

	tracker.ExpectContent(testPathCredentials, content)
	tracker.Clear(testPathCredentials)

	if tracker.ConsumeIfExpected(testPathCredentials, content) {
		t.Error("Clear should remove all expected hashes for path")
	}
}

// TestExpectedWriteTracker_NilSafety verifies nil tracker does not panic
func TestExpectedWriteTracker_NilSafety(t *testing.T) {
	var tracker *ExpectedWriteTracker

	// Should not panic
	tracker.ExpectContent(testPathCredentials, []byte("content"))
	tracker.Clear(testPathCredentials)

	if tracker.ConsumeIfExpected(testPathCredentials, []byte("content")) {
		t.Error("Nil tracker should return false")
	}
}

// TestExpectedWriteTracker_Concurrent verifies thread safety under concurrent access
// Run with -race flag: go test -race ./internal/watcher/...
func TestExpectedWriteTracker_Concurrent(t *testing.T) {
	tracker := NewExpectedWriteTracker()

	var wg sync.WaitGroup
	iterations := 100

	// Concurrent writes
	for i := 0; i < iterations; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			content := []byte{byte(n)}
			tracker.ExpectContent(testPathCredentials, content)
		}(i)
	}

	// Concurrent reads/consumes
	for i := 0; i < iterations; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			content := []byte{byte(n)}
			tracker.ConsumeIfExpected(testPathCredentials, content)
		}(i)
	}

	// Concurrent clears
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			tracker.Clear(testPathCredentials)
		}()
	}

	wg.Wait()
}

// TestExpectedWriteTracker_PathIsolation verifies different paths are isolated
func TestExpectedWriteTracker_PathIsolation(t *testing.T) {
	tracker := NewExpectedWriteTracker()
	content := []byte(`{"key": "value"}`)

	tracker.ExpectContent(testPathCredentials, content)

	// Same content but different path should return false
	if tracker.ConsumeIfExpected(testPathConfig, content) {
		t.Error("Different path should not match")
	}

	// Correct path should return true
	if !tracker.ConsumeIfExpected(testPathCredentials, content) {
		t.Error("Correct path should match")
	}
}
