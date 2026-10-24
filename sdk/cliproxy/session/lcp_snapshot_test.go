package session

import (
	"reflect"
	"testing"
	"time"
)

func TestMerklePrefixSnapshotRestorePreservesBinding(t *testing.T) {
	matcher := NewMerklePrefixMatcher(time.Hour)
	fingerprints := []string{"fp-one", "fp-two"}
	bound := matcher.BindFingerprintsWithResult("lcp:v1:test:model:caller", fingerprints, 1, "auth-b")
	if bound.SessionID == "" {
		t.Fatal("initial binding has no session ID")
	}

	snapshot := matcher.Snapshot()
	if len(snapshot) != 1 {
		t.Fatalf("snapshot groups = %d, want 1", len(snapshot))
	}

	restored := NewMerklePrefixMatcher(time.Hour)
	if got := restored.Restore(snapshot); got != 1 {
		t.Fatalf("restored groups = %d, want 1", got)
	}
	match, ok := restored.MatchFingerprints("lcp:v1:test:model:caller", fingerprints, 1)
	if !ok || match.AuthID != "auth-b" || match.SessionID != bound.SessionID {
		t.Fatalf("restored match = %#v, %v; want auth-b session %q", match, ok, bound.SessionID)
	}
}

func TestMerklePrefixRestoreDropsExpiredGroups(t *testing.T) {
	matcher := NewMerklePrefixMatcher(time.Hour)
	got := matcher.Restore([]MerklePrefixSnapshotEntry{{
		Namespace: "lcp:v1:test:model:caller", AuthID: "auth-a", MinPrefixLength: 1,
		Fingerprints: []string{"fp"}, ExpiresAt: time.Now().Add(-time.Second),
	}})
	if got != 0 {
		t.Fatalf("restored expired groups = %d, want 0", got)
	}
}

func TestMerklePrefixSnapshotRoundTripKeepsMatchingContext(t *testing.T) {
	matcher := NewMerklePrefixMatcher(time.Hour)
	bound := matcher.BindFingerprintsWithContext("lcp:v1:test:model:caller", []string{"fp-sys", "fp-one", "fp-two"}, []string{"fp-two"}, "env-digest", 2, "auth-b")
	if bound.SessionID == "" {
		t.Fatal("initial binding has no session ID")
	}
	original := matcher.Snapshot()
	if len(original) != 1 || original[0].EnvironmentDigest != "env-digest" || len(original[0].TailFingerprints) != 1 {
		t.Fatalf("snapshot lost matching context: %#v", original)
	}
	compaction := original[0]
	compaction.Namespace = "lcp:v1:test:model:other"
	compaction.SessionID = "lcp-compaction"
	compaction.ParentSessionID = bound.SessionID
	compaction.IsCompaction = true
	compaction.NodeKind = "compaction"
	entries := append(original, compaction)

	restored := NewMerklePrefixMatcher(time.Hour)
	if got := restored.Restore(entries); got != 2 {
		t.Fatalf("restored groups = %d, want 2", got)
	}
	if !reflect.DeepEqual(restored.Snapshot(), entries) {
		t.Fatalf("round trip changed entries:\n got  %#v\n want %#v", restored.Snapshot(), entries)
	}
}

func TestMerklePrefixRestoreCapsExpiryAtCurrentTTL(t *testing.T) {
	matcher := NewMerklePrefixMatcher(time.Minute)
	far := time.Now().Add(24 * time.Hour)
	if got := matcher.Restore([]MerklePrefixSnapshotEntry{{
		Namespace: "lcp:v1:test:model:caller", AuthID: "auth-a", MinPrefixLength: 1,
		Fingerprints: []string{"fp"}, ExpiresAt: far,
	}}); got != 1 {
		t.Fatalf("restored groups = %d, want 1", got)
	}
	snapshot := matcher.Snapshot()
	if len(snapshot) != 1 || snapshot[0].ExpiresAt.After(time.Now().Add(time.Minute)) {
		t.Fatalf("restored expiry not capped at the current TTL: %#v", snapshot)
	}
}

func TestMerklePrefixSnapshotKeepsRecencyAcrossRestore(t *testing.T) {
	matcher := NewMerklePrefixMatcher(time.Hour)
	namespace := "lcp:v1:test:model:caller"
	// Two equal-length trajectories sharing the first turn, bound to different
	// auths; the one used last must still win the tie after a restore.
	matcher.BindFingerprintsWithResult(namespace, []string{"fp-shared", "fp-b"}, 1, "auth-b")
	matcher.BindFingerprintsWithResult(namespace, []string{"fp-shared", "fp-a"}, 1, "auth-a")
	matcher.TouchFingerprintsWithContext(namespace, []string{"fp-shared", "fp-b"}, nil, "", 1, "auth-b")
	before, ok := matcher.MatchFingerprints(namespace, []string{"fp-shared"}, 1)
	if !ok || before.AuthID != "auth-b" {
		t.Fatalf("pre-restore match = %#v, %v; want auth-b", before, ok)
	}

	restored := NewMerklePrefixMatcher(time.Hour)
	if got := restored.Restore(matcher.Snapshot()); got != 2 {
		t.Fatalf("restored groups = %d, want 2", got)
	}
	after, ok := restored.MatchFingerprints(namespace, []string{"fp-shared"}, 1)
	if !ok || after.AuthID != "auth-b" {
		t.Fatalf("post-restore match = %#v, %v; want auth-b (most recently used)", after, ok)
	}
}
