package fpobserve

import "testing"

func TestPutSnapshotLatestAndCount(t *testing.T) {
	Reset()
	t.Cleanup(Reset)

	Put(Record{Account: "a1", Provider: "codex", UserAgent: "ua-old"}, 1000)
	Put(Record{Account: "a1", Provider: "codex", UserAgent: "ua-new"}, 1001) // same account → replace + count++
	Put(Record{Account: "b1", Provider: "claude", UserAgent: "uc"}, 1002)

	s := Snapshot()
	if len(s) != 2 {
		t.Fatalf("snapshot len = %d, want 2 (per-account latest)", len(s))
	}
	// Sorted by provider then account: "claude" < "codex" alphabetically.
	if s[0].Provider != "claude" || s[0].Account != "b1" {
		t.Fatalf("sort order wrong: got %s/%s, want claude/b1 first", s[0].Provider, s[0].Account)
	}
	codex := s[1]
	if codex.Provider != "codex" || codex.Account != "a1" {
		t.Fatalf("second entry wrong: got %s/%s, want codex/a1", codex.Provider, codex.Account)
	}
	if codex.UserAgent != "ua-new" {
		t.Fatalf("latest UA = %q, want ua-new (second Put replaces)", codex.UserAgent)
	}
	if codex.Count != 2 {
		t.Fatalf("count = %d, want 2 (incremented)", codex.Count)
	}
	if codex.LastSeenUnix != 1001 {
		t.Fatalf("last_seen = %d, want 1001", codex.LastSeenUnix)
	}

	Reset()
	if len(Snapshot()) != 0 {
		t.Fatal("Reset did not clear the store")
	}
}
