package helps

import (
	"fmt"
	"testing"
	"time"
)

func TestClaudeDiagnosticsTracksCompletedMessagePerCredentialSession(t *testing.T) {
	resetClaudeDiagnosticsForTest()
	defer resetClaudeDiagnosticsForTest()

	key, sequence, previous := BeginClaudeDiagnostics("credential-a", "session-a")
	if key == "" || sequence != 1 || previous != "" {
		t.Fatalf("first begin = %q/%d/%q, want key/1/empty", key, sequence, previous)
	}
	CommitClaudeDiagnostics(key, sequence, "msg_first")
	_, secondSequence, previous := BeginClaudeDiagnostics("credential-a", "session-a")
	if secondSequence != 2 || previous != "msg_first" {
		t.Fatalf("second begin = %d/%q, want 2/msg_first", secondSequence, previous)
	}

	_, _, otherSession := BeginClaudeDiagnostics("credential-a", "session-b")
	_, _, otherCredential := BeginClaudeDiagnostics("credential-b", "session-a")
	if otherSession != "" || otherCredential != "" {
		t.Fatalf("diagnostics leaked across identity: session=%q credential=%q", otherSession, otherCredential)
	}
}

func TestClaudeDiagnosticsRejectsExpiredGenerationCommit(t *testing.T) {
	resetClaudeDiagnosticsForTest()
	defer resetClaudeDiagnosticsForTest()

	key, expiredSequence, _ := BeginClaudeDiagnostics("credential", "session")
	claudeDiagnosticsState.Lock()
	entry := claudeDiagnosticsState.entries[key]
	entry.expiresAt = time.Now().Add(-time.Second)
	claudeDiagnosticsState.entries[key] = entry
	claudeDiagnosticsState.Unlock()

	newKey, currentSequence, previous := BeginClaudeDiagnostics("credential", "session")
	if newKey != key || currentSequence <= expiredSequence || previous != "" {
		t.Fatalf("new generation = %q/%d/%q, want same key/new sequence/empty", newKey, currentSequence, previous)
	}
	CommitClaudeDiagnostics(newKey, currentSequence, "msg_current")
	CommitClaudeDiagnostics(key, expiredSequence, "msg_expired")
	_, _, previous = BeginClaudeDiagnostics("credential", "session")
	if previous != "msg_current" {
		t.Fatalf("previous message = %q, want current generation", previous)
	}
}

func TestClaudeDiagnosticsCacheEvictsOldestEntriesWithinCapacity(t *testing.T) {
	resetClaudeDiagnosticsForTest()
	defer resetClaudeDiagnosticsForTest()

	firstKey, firstSequence, _ := BeginClaudeDiagnostics("credential", "session-0")
	var newestKey string
	for index := 1; index <= claudeDiagnosticsMaxEntries; index++ {
		newestKey, _, _ = BeginClaudeDiagnostics("credential", fmt.Sprintf("session-%d", index))
	}

	claudeDiagnosticsState.Lock()
	entryCount := len(claudeDiagnosticsState.entries)
	_, firstFound := claudeDiagnosticsState.entries[firstKey]
	_, newestFound := claudeDiagnosticsState.entries[newestKey]
	claudeDiagnosticsState.Unlock()
	if entryCount > claudeDiagnosticsMaxEntries {
		t.Fatalf("cache entries = %d, want at most %d", entryCount, claudeDiagnosticsMaxEntries)
	}
	if firstFound {
		t.Fatal("oldest diagnostics entry was not evicted")
	}
	if !newestFound {
		t.Fatal("newest diagnostics entry was evicted")
	}

	newKey, newSequence, _ := BeginClaudeDiagnostics("credential", "session-0")
	if newKey != firstKey || newSequence <= firstSequence {
		t.Fatalf("recreated generation = %q/%d, want same key after sequence %d", newKey, newSequence, firstSequence)
	}
	CommitClaudeDiagnostics(newKey, newSequence, "msg_recreated")
	CommitClaudeDiagnostics(firstKey, firstSequence, "msg_evicted")
	_, _, previous := BeginClaudeDiagnostics("credential", "session-0")
	if previous != "msg_recreated" {
		t.Fatalf("previous message = %q, want recreated generation", previous)
	}
}

func TestClaudeDiagnosticsRejectsLateOlderCommit(t *testing.T) {
	resetClaudeDiagnosticsForTest()
	defer resetClaudeDiagnosticsForTest()

	key, first, _ := BeginClaudeDiagnostics("credential", "session")
	_, second, _ := BeginClaudeDiagnostics("credential", "session")
	CommitClaudeDiagnostics(key, second, "msg_newer")
	CommitClaudeDiagnostics(key, first, "msg_older")
	_, _, previous := BeginClaudeDiagnostics("credential", "session")
	if previous != "msg_newer" {
		t.Fatalf("previous message = %q, want newer completed generation", previous)
	}
}
