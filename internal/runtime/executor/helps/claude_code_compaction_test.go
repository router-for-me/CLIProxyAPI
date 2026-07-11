package helps

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestClaudeCodeCompactionLaneIsolatedAndDefensive(t *testing.T) {
	resetClaudeCodeCompactionLanesForTest()
	key, ok := NewClaudeCodeCompactionLaneKey("session", "model-a", "auth-a")
	if !ok {
		t.Fatal("expected valid lane key")
	}

	lane := LockClaudeCodeCompactionLane(key, time.Hour)
	revision, err := lane.Commit(ClaudeCodeCompactionState{
		SourcePrefixHashes: []string{"one"},
		ReplacementItems:   [][]byte{[]byte(`{"type":"compaction"}`)},
	})
	if err != nil {
		t.Fatalf("commit lane: %v", err)
	}
	state := lane.State()
	state.SourcePrefixHashes[0] = "mutated"
	state.ReplacementItems[0][0] = 'x'
	lane.Unlock()

	lane = LockClaudeCodeCompactionLane(key, time.Hour)
	got := lane.State()
	lane.Unlock()
	if got.Revision != revision || got.SourcePrefixHashes[0] != "one" || string(got.ReplacementItems[0]) != `{"type":"compaction"}` {
		t.Fatalf("stored state was not defensively copied: %#v", got)
	}

	otherKey, _ := NewClaudeCodeCompactionLaneKey("session", "model-b", "auth-a")
	other := LockClaudeCodeCompactionLane(otherKey, time.Hour)
	defer other.Unlock()
	if got := other.State(); len(got.ReplacementItems) != 0 {
		t.Fatalf("model-specific lane leaked state: %#v", got)
	}
}

func TestClaudeCodeCompactionLaneBoundsDurableHashHistory(t *testing.T) {
	resetClaudeCodeCompactionLanesForTest()
	key, ok := NewClaudeCodeCompactionLaneKey("bounded-hashes", "model-a", "auth-a")
	if !ok {
		t.Fatal("expected valid lane key")
	}
	values := make([]string, maxClaudeCodeCompactionStateHashes+32)
	for i := range values {
		values[i] = fmt.Sprintf("hash-%04d", i)
	}
	lane := LockClaudeCodeCompactionLane(key, time.Hour)
	if _, err := lane.Commit(ClaudeCodeCompactionState{
		AbsorbedReplayItemHashes:    append([]string(nil), values...),
		RejectedEncryptedItemHashes: append([]string(nil), values...),
	}); err != nil {
		t.Fatalf("commit bounded hash state: %v", err)
	}
	got := lane.State()
	lane.Unlock()

	for name, hashes := range map[string][]string{
		"absorbed": got.AbsorbedReplayItemHashes,
		"rejected": got.RejectedEncryptedItemHashes,
	} {
		if len(hashes) != maxClaudeCodeCompactionStateHashes {
			t.Fatalf("%s hashes = %d, want %d", name, len(hashes), maxClaudeCodeCompactionStateHashes)
		}
		if hashes[0] != "hash-0032" || hashes[len(hashes)-1] != fmt.Sprintf("hash-%04d", len(values)-1) {
			t.Fatalf("%s hashes did not retain newest window: first=%q last=%q", name, hashes[0], hashes[len(hashes)-1])
		}
	}
}

func TestClaudeCodeCompactionObservationRejectsStaleRevision(t *testing.T) {
	resetClaudeCodeCompactionLanesForTest()
	key, _ := NewClaudeCodeCompactionLaneKey("session", "model", "auth")
	staleLane := LockClaudeCodeCompactionLane(key, time.Hour)
	if _, err := staleLane.Commit(ClaudeCodeCompactionState{ReplacementItems: [][]byte{[]byte(`{"type":"compaction"}`)}}); err != nil {
		t.Fatalf("commit lane: %v", err)
	}
	staleRevision := staleLane.BeginObservation()
	staleLane.Unlock()

	currentLane := LockClaudeCodeCompactionLane(key, time.Hour)
	currentRevision := currentLane.BeginObservation()
	currentLane.Unlock()

	if err := staleLane.ObserveTerminal(staleRevision, 100, 80, 7); err != nil {
		t.Fatalf("observe stale terminal: %v", err)
	}
	if err := currentLane.ObserveTerminal(currentRevision, 120, 90, 11); err != nil {
		t.Fatalf("observe current terminal: %v", err)
	}

	lane := LockClaudeCodeCompactionLane(key, time.Hour)
	got := lane.State()
	lane.Unlock()
	if got.ClientInputTokens != 120 || got.UpstreamInputTokens != 90 || got.PendingContextTokens != 11 {
		t.Fatalf("terminal observation = (%d, %d, %d), want (120, 90, 11)", got.ClientInputTokens, got.UpstreamInputTokens, got.PendingContextTokens)
	}
}

func TestClaudeCodeCompactionStateSurvivesRestartExactly(t *testing.T) {
	resetClaudeCodeCompactionLanesForTest()
	stateDir := t.TempDir()
	key, _ := NewClaudeCodeCompactionLaneKey("restart-session", "gpt-5.6-sol", "codex-auth")
	wantItems := [][]byte{
		[]byte(` {"type":"compaction","encrypted_content":"AA=="} `),
		{0x00, 0xff, '\n', '\r', '\t', 0x7f},
		nil,
		{},
	}

	lane := LockClaudeCodeCompactionLane(key, time.Hour, stateDir)
	_, err := lane.Commit(ClaudeCodeCompactionState{
		SourcePrefixHashes:          []string{"a", "b"},
		ReplacementItems:            wantItems,
		EnvelopeHash:                "envelope",
		CompactionTokens:            1_200,
		AbsorbedReplayItemHashes:    []string{"replay-a"},
		RejectedEncryptedItemHashes: []string{"reject-a"},
		LegacyOnly:                  true,
	})
	if err != nil {
		t.Fatalf("commit persistent lane: %v", err)
	}
	revision := lane.BeginObservation()
	lane.Unlock()
	if err := lane.ObserveTerminal(revision, 241_000, 34_000, 800); err != nil {
		t.Fatalf("persist terminal observation: %v", err)
	}

	resetClaudeCodeCompactionLanesForTest()
	lane = LockClaudeCodeCompactionLane(key, time.Hour, stateDir)
	defer lane.Unlock()
	if err := lane.PersistenceError(); err != nil {
		t.Fatalf("reload persistent lane: %v", err)
	}
	got := lane.State()
	if got.Revision != revision || got.EnvelopeHash != "envelope" || !got.LegacyOnly || got.PendingContextTokens != 800 {
		t.Fatalf("reloaded scalar state mismatch: %#v", got)
	}
	if len(got.AbsorbedReplayItemHashes) != 1 || got.AbsorbedReplayItemHashes[0] != "replay-a" || len(got.RejectedEncryptedItemHashes) != 1 || got.RejectedEncryptedItemHashes[0] != "reject-a" {
		t.Fatalf("reloaded replay/rejection markers mismatch: %#v", got)
	}
	if len(got.ReplacementItems) != len(wantItems) {
		t.Fatalf("replacement item count = %d, want %d", len(got.ReplacementItems), len(wantItems))
	}
	for i := range wantItems {
		if !bytes.Equal(got.ReplacementItems[i], wantItems[i]) {
			t.Fatalf("replacement item %d changed across restart: got %x want %x", i, got.ReplacementItems[i], wantItems[i])
		}
		if (got.ReplacementItems[i] == nil) != (wantItems[i] == nil) {
			t.Fatalf("replacement item %d lost nil/empty distinction", i)
		}
	}

	entries, err := os.ReadDir(stateDir)
	if err != nil {
		t.Fatalf("read state directory: %v", err)
	}
	if len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), claudeCodeCompactionStateFilePrefix) {
		t.Fatalf("state directory entries = %#v, want one versioned state file", entries)
	}
	if runtime.GOOS != "windows" {
		dirInfo, err := os.Stat(stateDir)
		if err != nil {
			t.Fatal(err)
		}
		if gotMode := dirInfo.Mode().Perm(); gotMode != claudeCodeCompactionStateDirectoryMode {
			t.Fatalf("state directory mode = %o, want %o", gotMode, claudeCodeCompactionStateDirectoryMode)
		}
		fileInfo, err := entries[0].Info()
		if err != nil {
			t.Fatal(err)
		}
		if gotMode := fileInfo.Mode().Perm(); gotMode != claudeCodeCompactionStateFileMode {
			t.Fatalf("state file mode = %o, want %o", gotMode, claudeCodeCompactionStateFileMode)
		}
	}
}

func TestClaudeCodeCompactionReplaceStateIfRevisionIsAtomicAndConditional(t *testing.T) {
	resetClaudeCodeCompactionLanesForTest()
	stateDir := t.TempDir()
	key, _ := NewClaudeCodeCompactionLaneKey("rejected-session", "gpt-5.6-sol", "codex-auth")
	lane := LockClaudeCodeCompactionLane(key, time.Hour, stateDir)
	if _, err := lane.Commit(ClaudeCodeCompactionState{
		ReplacementItems: [][]byte{[]byte(`{"type":"compaction","encrypted_content":"rejected"}`)},
		EnvelopeHash:     "envelope",
	}); err != nil {
		t.Fatalf("commit rejected lane: %v", err)
	}
	revision := lane.BeginObservation()
	lane.Unlock()
	defer lane.AbandonObservation()

	replaced, err := lane.ReplaceStateIfRevision(revision, ClaudeCodeCompactionState{
		EnvelopeHash:                "envelope",
		RejectedEncryptedItemHashes: []string{"reasoning-a"},
	})
	if err != nil || !replaced {
		t.Fatalf("replace rejected state replaced=%v err=%v", replaced, err)
	}
	staleReplace, err := lane.ReplaceStateIfRevision(revision, ClaudeCodeCompactionState{EnvelopeHash: "stale"})
	if err != nil || staleReplace {
		t.Fatalf("stale replace replaced=%v err=%v", staleReplace, err)
	}

	resetClaudeCodeCompactionLanesForTest()
	reloaded := LockClaudeCodeCompactionLane(key, time.Hour, stateDir)
	defer reloaded.Unlock()
	got := reloaded.State()
	if len(got.ReplacementItems) != 0 || got.EnvelopeHash != "envelope" || len(got.RejectedEncryptedItemHashes) != 1 || got.RejectedEncryptedItemHashes[0] != "reasoning-a" {
		t.Fatalf("atomic rejection state did not survive reload: %#v", got)
	}
}

func TestClaudeCodeCompactionRejectsUnchangedReplacementAcrossNewerObservation(t *testing.T) {
	resetClaudeCodeCompactionLanesForTest()
	stateDir := t.TempDir()
	key, _ := NewClaudeCodeCompactionLaneKey("parallel-rejected-session", "gpt-5.6-sol", "codex-auth")
	expected := ClaudeCodeCompactionState{
		SourcePrefixHashes: []string{"source-a"},
		ReplacementItems:   [][]byte{[]byte(`{"type":"compaction","encrypted_content":"bad"}`)},
		EnvelopeHash:       "envelope",
	}
	older := LockClaudeCodeCompactionLane(key, time.Hour, stateDir)
	if _, err := older.Commit(expected); err != nil {
		t.Fatalf("commit replacement: %v", err)
	}
	older.BeginObservation()
	older.Unlock()
	defer older.AbandonObservation()

	newer := LockClaudeCodeCompactionLane(key, time.Hour, stateDir)
	newer.BeginObservation() // Advances only the revision, not replacement identity.
	newer.Unlock()
	defer newer.AbandonObservation()

	replaced, err := older.ReplaceStateIfCurrentMatches(expected, ClaudeCodeCompactionState{
		EnvelopeHash:                "envelope",
		RejectedEncryptedItemHashes: []string{"reasoning-a"},
	})
	if err != nil || !replaced {
		t.Fatalf("retire unchanged rejected replacement replaced=%v err=%v", replaced, err)
	}
	check := LockClaudeCodeCompactionLane(key, time.Hour, stateDir)
	defer check.Unlock()
	got := check.State()
	if len(got.ReplacementItems) != 0 || len(got.AbsorbedReplayItemHashes) != 0 || len(got.RejectedEncryptedItemHashes) != 1 {
		t.Fatalf("rejected replacement was not retired atomically: %#v", got)
	}
}

func TestClaudeCodeCompactionStateRejectsCorruptChecksum(t *testing.T) {
	resetClaudeCodeCompactionLanesForTest()
	stateDir := t.TempDir()
	key, _ := NewClaudeCodeCompactionLaneKey("corrupt-session", "gpt-5.6-terra", "codex-auth")
	lane := LockClaudeCodeCompactionLane(key, time.Hour, stateDir)
	if _, err := lane.Commit(ClaudeCodeCompactionState{
		ReplacementItems: [][]byte{[]byte(`{"type":"compaction","encrypted_content":"original"}`)},
	}); err != nil {
		t.Fatalf("commit persistent lane: %v", err)
	}
	lane.Unlock()

	path := claudeCodeCompactionStatePath(stateDir, key)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read state file: %v", err)
	}
	var record map[string]any
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatalf("decode state file: %v", err)
	}
	state := record["state"].(map[string]any)
	state["envelope_hash"] = "tampered-without-updating-checksum"
	corrupt, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("encode corrupt state: %v", err)
	}
	if err := os.WriteFile(path, corrupt, claudeCodeCompactionStateFileMode); err != nil {
		t.Fatalf("write corrupt state: %v", err)
	}

	resetClaudeCodeCompactionLanesForTest()
	lane = LockClaudeCodeCompactionLane(key, time.Hour, stateDir)
	defer lane.Unlock()
	if err := lane.PersistenceError(); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("persistence error = %v, want checksum mismatch", err)
	}
	if got := lane.State(); len(got.ReplacementItems) != 0 || got.EnvelopeHash != "" {
		t.Fatalf("corrupt state was exposed: %#v", got)
	}
}

func TestClaudeCodeCompactionStateExpiresAcrossRestart(t *testing.T) {
	resetClaudeCodeCompactionLanesForTest()
	stateDir := t.TempDir()
	key, _ := NewClaudeCodeCompactionLaneKey("expired-session", "gpt-5.6-luna", "codex-auth")
	ttl := 20 * time.Millisecond
	lane := LockClaudeCodeCompactionLane(key, ttl, stateDir)
	if _, err := lane.Commit(ClaudeCodeCompactionState{ReplacementItems: [][]byte{[]byte("opaque")}}); err != nil {
		t.Fatalf("commit persistent lane: %v", err)
	}
	lane.Unlock()
	time.Sleep(3 * ttl)

	resetClaudeCodeCompactionLanesForTest()
	lane = LockClaudeCodeCompactionLane(key, ttl, stateDir)
	defer lane.Unlock()
	if err := lane.PersistenceError(); err != nil {
		t.Fatalf("load expired lane: %v", err)
	}
	if got := lane.State(); len(got.ReplacementItems) != 0 {
		t.Fatalf("expired state was loaded: %#v", got)
	}
	if _, err := os.Stat(claudeCodeCompactionStatePath(stateDir, key)); !os.IsNotExist(err) {
		t.Fatalf("expired state file still exists: %v", err)
	}
}

func TestClaudeCodeCompactionSweepRemovesOnlyAuthenticatedExpiredStates(t *testing.T) {
	resetClaudeCodeCompactionLanesForTest()
	stateDir := t.TempDir()
	now := time.Now()
	expiredKey, _ := NewClaudeCodeCompactionLaneKey("sweep-expired", "model", "auth")
	freshKey, _ := NewClaudeCodeCompactionLaneKey("sweep-fresh", "model", "auth")
	corruptKey, _ := NewClaudeCodeCompactionLaneKey("sweep-corrupt", "model", "auth")
	if err := persistClaudeCodeCompactionState(stateDir, expiredKey, ClaudeCodeCompactionState{EnvelopeHash: "expired"}, now.Add(-2*time.Hour), time.Hour); err != nil {
		t.Fatalf("persist expired state: %v", err)
	}
	if err := persistClaudeCodeCompactionState(stateDir, freshKey, ClaudeCodeCompactionState{EnvelopeHash: "fresh"}, now, time.Hour); err != nil {
		t.Fatalf("persist fresh state: %v", err)
	}
	corruptPath := claudeCodeCompactionStatePath(stateDir, corruptKey)
	if err := os.WriteFile(corruptPath, []byte(`{"version":1,"expires_at_unix_nano":1,"sha256":"invalid"}`), claudeCodeCompactionStateFileMode); err != nil {
		t.Fatalf("write corrupt state: %v", err)
	}
	unrelatedPath := filepath.Join(stateDir, "keep-me.json")
	if err := os.WriteFile(unrelatedPath, []byte("unrelated"), claudeCodeCompactionStateFileMode); err != nil {
		t.Fatalf("write unrelated file: %v", err)
	}

	triggerKey, _ := NewClaudeCodeCompactionLaneKey("sweep-trigger", "model", "auth")
	lane := LockClaudeCodeCompactionLane(triggerKey, time.Hour, stateDir)
	lane.Unlock()

	if _, err := os.Stat(claudeCodeCompactionStatePath(stateDir, expiredKey)); !os.IsNotExist(err) {
		t.Fatalf("authenticated expired state still exists: %v", err)
	}
	for name, path := range map[string]string{
		"fresh state":    claudeCodeCompactionStatePath(stateDir, freshKey),
		"corrupt state":  corruptPath,
		"unrelated file": unrelatedPath,
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s was removed: %v", name, err)
		}
	}

	// A second request in the same interval must not rescan the directory.
	deferredKey, _ := NewClaudeCodeCompactionLaneKey("sweep-deferred", "model", "auth")
	if err := persistClaudeCodeCompactionState(stateDir, deferredKey, ClaudeCodeCompactionState{EnvelopeHash: "deferred"}, now.Add(-2*time.Hour), time.Hour); err != nil {
		t.Fatalf("persist deferred expired state: %v", err)
	}
	secondTriggerKey, _ := NewClaudeCodeCompactionLaneKey("sweep-second-trigger", "model", "auth")
	lane = LockClaudeCodeCompactionLane(secondTriggerKey, time.Hour, stateDir)
	lane.Unlock()
	if _, err := os.Stat(claudeCodeCompactionStatePath(stateDir, deferredKey)); err != nil {
		t.Fatalf("state was swept again inside throttle interval: %v", err)
	}

	claudeCodeCompactionSweeps.Lock()
	claudeCodeCompactionSweeps.lastByDirectory[stateDir] = now.Add(-2 * claudeCodeCompactionSweepInterval)
	claudeCodeCompactionSweeps.Unlock()
	thirdTriggerKey, _ := NewClaudeCodeCompactionLaneKey("sweep-third-trigger", "model", "auth")
	lane = LockClaudeCodeCompactionLane(thirdTriggerKey, time.Hour, stateDir)
	lane.Unlock()
	if _, err := os.Stat(claudeCodeCompactionStatePath(stateDir, deferredKey)); !os.IsNotExist(err) {
		t.Fatalf("expired state still exists after throttle interval: %v", err)
	}
}

func TestClaudeCodeCompactionPruneKeepsActiveAndWaitingLane(t *testing.T) {
	resetClaudeCodeCompactionLanesForTest()
	key, _ := NewClaudeCodeCompactionLaneKey("pinned-session", "model", "auth")
	active := LockClaudeCodeCompactionLane(key, time.Hour)
	if _, err := active.Commit(ClaudeCodeCompactionState{EnvelopeHash: "still-the-same-entry"}); err != nil {
		t.Fatalf("commit active lane: %v", err)
	}

	waiterReady := make(chan *ClaudeCodeCompactionLane, 1)
	go func() {
		waiterReady <- LockClaudeCodeCompactionLane(key, time.Hour)
	}()
	waitForClaudeCodeCompactionPins(t, active.entry, 2)

	for i := 0; i < maxClaudeCodeCompactionLanes+maxClaudeCodeCompactionLanes/2; i++ {
		otherKey, _ := NewClaudeCodeCompactionLaneKey(fmt.Sprintf("other-%d", i), "model", "auth")
		other := LockClaudeCodeCompactionLane(otherKey, time.Hour)
		other.Unlock()
	}

	claudeCodeCompactionLanes.Lock()
	mapped := claudeCodeCompactionLanes.entries[key]
	claudeCodeCompactionLanes.Unlock()
	if mapped != active.entry {
		active.Unlock()
		t.Fatal("pruning removed an active/waiting lane and allowed the key to split")
	}
	active.Unlock()

	select {
	case waiter := <-waiterReady:
		defer waiter.Unlock()
		if waiter.entry != mapped {
			t.Fatal("waiter acquired a different entry for the same lane key")
		}
		if got := waiter.State().EnvelopeHash; got != "still-the-same-entry" {
			t.Fatalf("waiter state envelope = %q, want preserved active state", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("waiter did not acquire active lane after unlock")
	}
}

func TestClaudeCodeCompactionPruneKeepsInFlightObservation(t *testing.T) {
	resetClaudeCodeCompactionLanesForTest()
	key, _ := NewClaudeCodeCompactionLaneKey("observed-session", "model", "auth")
	lane := LockClaudeCodeCompactionLane(key, time.Hour)
	if _, err := lane.Commit(ClaudeCodeCompactionState{EnvelopeHash: "observed-entry"}); err != nil {
		t.Fatalf("commit observed lane: %v", err)
	}
	revision := lane.BeginObservation()
	entry := lane.entry
	lane.Unlock()

	for i := 0; i < maxClaudeCodeCompactionLanes+maxClaudeCodeCompactionLanes/2; i++ {
		otherKey, _ := NewClaudeCodeCompactionLaneKey(fmt.Sprintf("observed-other-%d", i), "model", "auth")
		other := LockClaudeCodeCompactionLane(otherKey, time.Hour)
		other.Unlock()
	}

	claudeCodeCompactionLanes.Lock()
	mapped := claudeCodeCompactionLanes.entries[key]
	claudeCodeCompactionLanes.Unlock()
	if mapped != entry {
		lane.AbandonObservation()
		t.Fatal("pruning removed a lane with an in-flight terminal observation")
	}
	lane.AbandonObservation()
	lane.AbandonObservation()

	claudeCodeCompactionLanes.Lock()
	pins := entry.pins
	claudeCodeCompactionLanes.Unlock()
	if pins != 0 {
		t.Fatalf("idempotent observation abandon left %d pins, want 0", pins)
	}
	if err := lane.ObserveTerminal(revision, 100, 90, 5); err != nil {
		t.Fatalf("terminal call after abandon: %v", err)
	}
}

func waitForClaudeCodeCompactionPins(t *testing.T, entry *claudeCodeCompactionEntry, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		claudeCodeCompactionLanes.Lock()
		got := entry.pins
		claudeCodeCompactionLanes.Unlock()
		if got >= want {
			return
		}
		runtime.Gosched()
	}
	t.Fatalf("entry pins did not reach %d", want)
}

func TestClaudeCodeCompactionPersistenceErrorsAreSurfaced(t *testing.T) {
	resetClaudeCodeCompactionLanesForTest()
	parent := t.TempDir()
	notDirectory := filepath.Join(parent, "state-file")
	if err := os.WriteFile(notDirectory, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	key, _ := NewClaudeCodeCompactionLaneKey("error-session", "model", "auth")
	lane := LockClaudeCodeCompactionLane(key, time.Hour, notDirectory)
	if err := lane.PersistenceError(); err == nil {
		t.Fatal("expected state directory load error")
	}
	if _, err := lane.Commit(ClaudeCodeCompactionState{EnvelopeHash: "must-not-install"}); err == nil {
		t.Fatal("expected commit persistence error")
	}
	if err := lane.Clear(); err == nil {
		t.Fatal("expected clear persistence error")
	}
	revision := lane.BeginObservation()
	if got := lane.State().EnvelopeHash; got != "" {
		t.Fatalf("failed persistent commit installed in-memory state %q", got)
	}
	lane.Unlock()
	if err := lane.ObserveTerminal(revision, 100, 90, 4); err == nil {
		t.Fatal("expected terminal observation persistence error")
	}
}

func TestClaudeCodeCompactionTransientWriteFailureCanRecover(t *testing.T) {
	resetClaudeCodeCompactionLanesForTest()
	stateDir := filepath.Join(t.TempDir(), "state")
	key, _ := NewClaudeCodeCompactionLaneKey("retry-session", "model", "auth")
	lane := LockClaudeCodeCompactionLane(key, time.Hour, stateDir)
	defer lane.Unlock()
	if err := lane.PersistenceError(); err != nil {
		t.Fatalf("initial load: %v", err)
	}
	if err := os.Remove(stateDir); err != nil {
		t.Fatalf("remove empty state directory: %v", err)
	}
	if err := os.WriteFile(stateDir, []byte("temporarily not a directory"), claudeCodeCompactionStateFileMode); err != nil {
		t.Fatalf("replace state directory with file: %v", err)
	}
	if _, err := lane.Commit(ClaudeCodeCompactionState{EnvelopeHash: "first-attempt"}); err == nil {
		t.Fatal("expected transient persistence failure")
	}
	if lane.entry.lastWriteErr == nil {
		t.Fatal("transient write failure was not recorded")
	}
	if err := lane.PersistenceError(); err != nil {
		t.Fatalf("transient write error became a fatal lane error: %v", err)
	}
	if err := os.Remove(stateDir); err != nil {
		t.Fatalf("remove temporary state file: %v", err)
	}
	if _, err := lane.Commit(ClaudeCodeCompactionState{EnvelopeHash: "recovered"}); err != nil {
		t.Fatalf("retry commit after restoring filesystem: %v", err)
	}
	if lane.entry.lastWriteErr != nil {
		t.Fatalf("successful retry did not clear last write error: %v", lane.entry.lastWriteErr)
	}
	if got := lane.State().EnvelopeHash; got != "recovered" {
		t.Fatalf("recovered state envelope = %q, want recovered", got)
	}
}

func TestNewClaudeCodeCompactionLaneKeyRequiresAllDimensions(t *testing.T) {
	for _, tc := range []struct {
		session string
		model   string
		auth    string
	}{
		{"", "model", "auth"},
		{"session", "", "auth"},
		{"session", "model", ""},
	} {
		if _, ok := NewClaudeCodeCompactionLaneKey(tc.session, tc.model, tc.auth); ok {
			t.Fatalf("unexpected valid key for %#v", tc)
		}
	}
}
