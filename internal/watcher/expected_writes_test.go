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

// TestExpectedWriteTracker_ConsumeIfExpected_DoesNotRemoveHash verifies hash is NOT removed after consumption
// This allows multiple fsnotify events from the same write to all match
func TestExpectedWriteTracker_ConsumeIfExpected_DoesNotRemoveHash(t *testing.T) {
	tracker := NewExpectedWriteTracker()
	content := []byte(`{"key": "value"}`)

	tracker.ExpectContent(testPathCredentials, content)

	// First consume should succeed
	if !tracker.ConsumeIfExpected(testPathCredentials, content) {
		t.Error("First consume should succeed")
	}

	// Second consume should also succeed - hash is retained for multiple events
	if !tracker.ConsumeIfExpected(testPathCredentials, content) {
		t.Error("Second consume should also succeed - hash should be retained")
	}

	// Third consume should also succeed
	if !tracker.ConsumeIfExpected(testPathCredentials, content) {
		t.Error("Third consume should also succeed")
	}
}

// TestExpectedWriteTracker_HashReplacedOnNewWrite verifies hash is replaced when new write occurs
func TestExpectedWriteTracker_HashReplacedOnNewWrite(t *testing.T) {
	tracker := NewExpectedWriteTracker()
	content1 := []byte(`{"key": "value1"}`)
	content2 := []byte(`{"key": "value2"}`)

	tracker.ExpectContent(testPathCredentials, content1)

	// First content should match
	if !tracker.ConsumeIfExpected(testPathCredentials, content1) {
		t.Error("First content should match")
	}

	// New write replaces the old hash
	tracker.ExpectContent(testPathCredentials, content2)

	// Old content should no longer match
	if tracker.ConsumeIfExpected(testPathCredentials, content1) {
		t.Error("Old content should not match after new ExpectContent")
	}

	// New content should match
	if !tracker.ConsumeIfExpected(testPathCredentials, content2) {
		t.Error("New content should match")
	}
}

// TestExpectedWriteTracker_NewWriteReplacesOldHash verifies new ExpectContent replaces old hash
func TestExpectedWriteTracker_NewWriteReplacesOldHash(t *testing.T) {
	tracker := NewExpectedWriteTracker()
	content1 := []byte(`{"version": 1}`)
	content2 := []byte(`{"version": 2}`)

	tracker.ExpectContent(testPathCredentials, content1)
	tracker.ExpectContent(testPathCredentials, content2)

	// Only the latest hash (content2) should match
	if tracker.ConsumeIfExpected(testPathCredentials, content1) {
		t.Error("First hash should have been replaced by second")
	}
	if !tracker.ConsumeIfExpected(testPathCredentials, content2) {
		t.Error("Second (latest) hash should match")
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
