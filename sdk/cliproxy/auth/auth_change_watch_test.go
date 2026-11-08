package auth

import "testing"

func TestAuthChangeWatchCoversNativeMutationsAndReload(t *testing.T) {
	store := newMemoryAuthTestStore()
	manager := NewManager(store, nil, nil)
	changes, stop := manager.WatchAuthChanges("watched")
	defer stop()
	other, stopOther := manager.WatchAuthChanges("other")
	defer stopOther()
	assertChanged := func() {
		t.Helper()
		select {
		case <-changes:
		default:
			t.Fatal("native mutation did not notify watcher")
		}
		select {
		case <-other:
			t.Fatal("unrelated watcher notified")
		default:
		}
	}
	auth, err := manager.Register(t.Context(), &Auth{ID: "watched", Provider: "antigravity", Metadata: map[string]any{"access_token": "initial"}})
	if err != nil {
		t.Fatal(err)
	}
	assertChanged()
	for _, mode := range []string{"refresh", "prepare", "replace"} {
		next := auth.Clone()
		next.Metadata["access_token"] = mode
		switch mode {
		case "refresh":
			auth, err = manager.UpdateRefreshedAuth(t.Context(), auth, next)
		case "prepare":
			auth, err = manager.UpdatePreparedAuth(t.Context(), auth, next)
		default:
			auth, err = manager.Update(t.Context(), next)
		}
		if err != nil {
			t.Fatal(err)
		}
		assertChanged()
	}
	// Slow subscribers never block manager mutations and receive a coalesced hint.
	for range 100 {
		auth, err = manager.Update(t.Context(), auth)
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(changes) != 1 {
		t.Fatalf("queued %d events, want coalesced notification", len(changes))
	}
	assertChanged()
	manager.Remove(t.Context(), auth.ID)
	assertChanged()
	// Reload invalidates all subscribed snapshots, including currently absent IDs.
	if err = manager.Load(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changes:
	default:
		t.Fatal("reload did not invalidate watcher")
	}
	stop()
	stop()
	if _, open := <-changes; open {
		t.Fatal("unsubscribe did not close watcher")
	}
	manager.mu.RLock()
	_, retained := manager.authChangeWatchers["watched"]
	manager.mu.RUnlock()
	if retained {
		t.Fatal("watcher retained after unsubscribe")
	}
}
