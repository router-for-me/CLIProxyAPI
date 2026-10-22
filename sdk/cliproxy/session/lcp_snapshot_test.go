package session

import (
	"testing"
	"time"
)

func TestMerklePrefixSnapshotRoundTripPreservesLineageAndContext(t *testing.T) {
	const namespace = "openai:model:caller"
	matcher := NewMerklePrefixMatcher(time.Hour)
	parent := matcher.BindWithResult(namespace, turnsFromTexts("1", "2", "3", "4", "5"), "parent")
	fork := matcher.BindWithResult(namespace, turnsFromTexts("1", "2", "branch"), "fork")
	compacted := matcher.BindWithResult(namespace, turnsFromTexts("summary", "4", "5", "6"), "parent")
	if !fork.IsFork || !compacted.IsCompaction {
		t.Fatal("fixture did not create fork and compaction bindings")
	}
	entries, _ := matcher.Snapshot()
	restored := NewMerklePrefixMatcher(time.Hour)
	restored.Restore(entries)
	for _, test := range []struct {
		name, authID string
		texts        []string
		binding      MerklePrefixBindResult
	}{
		{"parent", "parent", []string{"1", "2", "3", "4", "5", "next"}, parent},
		{"fork", "fork", []string{"1", "2", "branch", "next"}, fork},
		{"compaction", "parent", []string{"summary", "4", "5", "6", "next"}, compacted},
	} {
		t.Run(test.name, func(t *testing.T) {
			match, ok := restored.Match(namespace, turnsFromTexts(test.texts...))
			if !ok || match.AuthID != test.authID || match.SessionID != test.binding.SessionID ||
				match.ParentSessionID != test.binding.ParentSessionID || match.NodeKind != test.binding.NodeKind ||
				match.IsCompaction != test.binding.IsCompaction {
				t.Fatalf("restored match = %+v, %v, want binding %+v", match, ok, test.binding)
			}
		})
	}
	// The tail index must still recognize a second compaction without a common prefix.
	match, ok := restored.Match(namespace, turnsFromTexts("new summary", "5", "6", "7"))
	if !ok || !match.IsCompaction || match.AuthID != "parent" || match.ParentSessionID != compacted.SessionID {
		t.Fatalf("restored tail context lost: %+v, %v", match, ok)
	}
	if _, okOther := restored.Match("other-caller", turnsFromTexts("1", "2", "3")); okOther {
		t.Fatal("restored binding crossed caller namespaces")
	}
}

func TestMerklePrefixSnapshotPreservesRecencyAndDetachesData(t *testing.T) {
	matcher := NewMerklePrefixMatcher(time.Hour)
	left := matcher.BindFingerprintsWithResult("scope", []string{"root", "left"}, 1, "a")
	right := matcher.BindFingerprintsWithResult("scope", []string{"root", "right"}, 1, "b")
	matcher.MatchFingerprints("scope", []string{"root", "left"}, 1)
	entries, _ := matcher.Snapshot()
	restored := NewMerklePrefixMatcherWithConfig(MerklePrefixMatcherConfig{MaxGroups: 2})
	restored.Restore(entries)
	entries[0].Fingerprints[0] = "mutated"
	entries[1].TailFingerprints[0] = "mutated"
	match, ok := restored.MatchFingerprints("scope", []string{"root"}, 1)
	if !ok || match.AuthID != "a" {
		t.Fatalf("newest-match order lost: %+v, %v", match, ok)
	}
	restored.BindFingerprints("scope", []string{"unrelated"}, 1, "c")
	if _, _, okRight := restored.LookupSession(right.SessionID); okRight {
		t.Fatal("restore lost eviction order")
	}
	if _, _, okLeft := restored.LookupSession(left.SessionID); !okLeft {
		t.Fatal("restore evicted the newer binding")
	}
	if _, okOriginal := matcher.MatchFingerprints("scope", []string{"root", "right"}, 1); !okOriginal {
		t.Fatal("snapshot exposed mutable matcher data")
	}
}

func TestMerklePrefixSnapshotPreservesIndependentTailAndEnvironment(t *testing.T) {
	matcher := NewMerklePrefixMatcher(time.Hour)
	parent := matcher.BindFingerprintsWithContext("scope", []string{"bounded", "head"},
		[]string{"tail-1", "tail-2"}, "environment", 1, "a")
	entries, _ := matcher.Snapshot()
	restored := NewMerklePrefixMatcher(time.Hour)
	restored.Restore(entries)
	fingerprints := []string{"summary", "tail-1", "tail-2", "next"}
	match, ok := restored.MatchFingerprintsWithContext("scope", fingerprints, fingerprints, "environment", 1)
	if !ok || !match.IsCompaction || match.AuthID != "a" || match.ParentSessionID != parent.SessionID {
		t.Fatalf("restored independent tail or environment lost: %+v, %v", match, ok)
	}
	if _, okOther := restored.MatchFingerprintsWithContext("scope", fingerprints, fingerprints, "other-environment", 1); okOther {
		t.Fatal("restored binding crossed environments")
	}
}

func TestMerklePrefixSnapshotRestoreBoundsAndGenerationGuard(t *testing.T) {
	now := time.Date(2026, time.October, 22, 0, 0, 0, 0, time.UTC)
	const ttl = 5 * time.Minute
	matcher := NewMerklePrefixMatcherWithConfig(MerklePrefixMatcherConfig{
		TTL: ttl, MaxGroups: 2, NowFunc: func() time.Time { return now },
	})
	entry := func(key, authID string) MerklePrefixSnapshotEntry {
		return MerklePrefixSnapshotEntry{
			Namespace: "scope", AuthID: authID, Fingerprints: []string{key},
			MinPrefixLength: 1, ExpiresAt: now.Add(time.Hour),
		}
	}
	expired, invalid := entry("expired", "expired"), entry("invalid", "invalid")
	expired.ExpiresAt = now
	invalid.MinPrefixLength = 2
	matcher.Restore([]MerklePrefixSnapshotEntry{
		entry("old", "old"), entry("duplicate", "old-duplicate"), expired, invalid,
		entry("duplicate", "new-duplicate"), entry("new", "new"),
	})
	entries, _ := matcher.Snapshot()
	if len(entries) != 2 || entries[0].AuthID != "new-duplicate" || entries[1].AuthID != "new" {
		t.Fatalf("restore failed validation, deduplication, or capacity pruning: %+v", entries)
	}
	for _, item := range entries {
		if !item.ExpiresAt.Equal(now.Add(ttl)) {
			t.Fatalf("restored expiry = %v, want current TTL", item.ExpiresAt)
		}
	}
	old, _ := matcher.MatchFingerprints("scope", []string{"new"}, 1)
	matcher.Restore(entries)
	if matcher.RemoveSessionBefore("scope", old.SessionID, "new", old.AccessNumber) {
		t.Fatal("pre-restore failure removed a restored binding")
	}
	now = now.Add(ttl)
	if current, _ := matcher.Snapshot(); len(current) != 0 {
		t.Fatal("snapshot retained expired groups")
	}
}
