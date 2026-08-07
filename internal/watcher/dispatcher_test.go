package watcher

import (
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// TestPrepareAuthUpdatesLockedNilQueue verifies that prepareAuthUpdatesLocked
// returns Add updates even when authQueue is nil (i.e. before setAuthUpdateQueue
// is called at startup). This covers the bug fixed in this PR where the early
// nil-queue returns silently dropped all initial auth events.
func TestPrepareAuthUpdatesLockedNilQueue(t *testing.T) {
	w := &Watcher{} // currentAuths nil, authQueue nil

	auths := []*coreauth.Auth{{ID: "a", Provider: "p"}}
	updates := w.prepareAuthUpdatesLocked(auths, false)

	if len(updates) != 1 {
		t.Fatalf("expected 1 update, got %d: %+v", len(updates), updates)
	}
	if updates[0].Action != AuthUpdateActionAdd {
		t.Errorf("expected action Add, got %v", updates[0].Action)
	}
	if updates[0].ID != "a" {
		t.Errorf("expected ID %q, got %q", "a", updates[0].ID)
	}
}

// TestPrepareAuthUpdatesLockedNilQueueSubsequent verifies that on subsequent
// calls with existing state and a nil queue, the diff is still computed
// and currentAuths is updated.
func TestPrepareAuthUpdatesLockedNilQueueSubsequent(t *testing.T) {
	w := &Watcher{}

	// First call: populate state.
	auths := []*coreauth.Auth{{ID: "a", Provider: "p"}}
	w.prepareAuthUpdatesLocked(auths, false)

	// Second call with different auth: should produce Modify/Add, not nil.
	auths2 := []*coreauth.Auth{
		{ID: "a", Provider: "p"},
		{ID: "b", Provider: "p"},
	}
	updates := w.prepareAuthUpdatesLocked(auths2, false)

	if len(updates) != 1 {
		t.Fatalf("expected 1 Add update for new account, got %d: %+v", len(updates), updates)
	}
	if updates[0].Action != AuthUpdateActionAdd || updates[0].ID != "b" {
		t.Errorf("expected Add for 'b', got %+v", updates[0])
	}
}
