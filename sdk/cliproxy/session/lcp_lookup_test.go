package session

import (
	"reflect"
	"testing"
	"time"
)

func TestMerklePrefixMatcher_LookupSession_PartialExpiration_ControllableClock(t *testing.T) {
	mockNow := time.Date(2026, 9, 9, 10, 0, 0, 0, time.UTC)
	matcher := NewMerklePrefixMatcherWithConfig(MerklePrefixMatcherConfig{TTL: 10 * time.Minute})
	matcher.nowFunc = func() time.Time { return mockNow }

	ns := "lcp:v1::openai::gpt-4o::caller"

	// 1. Bind turn 1 at T0
	bindRes1 := matcher.BindFingerprintsWithResult(ns, []string{"turn1"}, 1, "auth-1")
	sessionID := bindRes1.SessionID
	if sessionID == "" {
		t.Fatalf("expected valid sessionID on bindRes1")
	}

	// 2. Advance clock by 5 minutes and bind turn 2 (conversation growth)
	mockNow = mockNow.Add(5 * time.Minute)
	bindRes2 := matcher.BindFingerprintsWithResult(ns, []string{"turn1", "turn2"}, 1, "auth-1")
	if bindRes2.SessionID != sessionID {
		t.Fatalf("expected grown conversation to share sessionID, got %q != %q", bindRes2.SessionID, sessionID)
	}

	// Record group 2 state
	matcher.mu.Lock()
	nsObj := matcher.groups[ns]
	group2 := nsObj.groups[sequenceKey([]string{"turn1", "turn2"})]
	if group2 == nil {
		matcher.mu.Unlock()
		t.Fatalf("expected group2 to exist")
	}
	expectedExpiresAt := group2.expiresAt
	expectedAccessNumber := group2.lastAccessNumber
	matcher.mu.Unlock()

	// 3. Explicitly expire group 1 while keeping group 2 active
	matcher.mu.Lock()
	group1 := nsObj.groups[sequenceKey([]string{"turn1"})]
	if group1 == nil {
		matcher.mu.Unlock()
		t.Fatalf("expected group1 to exist")
	}
	group1.expiresAt = mockNow.Add(-time.Minute)
	matcher.mu.Unlock()

	// 4. LookupSession: must return auth-1 without error, must not report false/unbound
	authIDs, nsResult, ok := matcher.LookupSession(sessionID)
	if !ok {
		t.Fatalf("expected ok=true for partially expired session, got false")
	}
	if !reflect.DeepEqual(authIDs, []string{"auth-1"}) {
		t.Fatalf("expected [auth-1], got %v", authIDs)
	}
	if nsResult != ns {
		t.Fatalf("expected namespace %q, got %q", ns, nsResult)
	}

	// 5. Assert group 1 was removed and group 2 was NOT mutated (TTL/accessNumber untouched)
	matcher.mu.Lock()
	if nsObj.groups[sequenceKey([]string{"turn1"})] != nil {
		t.Fatalf("expected expired group1 to be removed by LookupSession")
	}
	group2After := nsObj.groups[sequenceKey([]string{"turn1", "turn2"})]
	if group2After == nil {
		t.Fatalf("expected active group2 to still exist")
	}
	if !group2After.expiresAt.Equal(expectedExpiresAt) {
		t.Fatalf("group2 expiresAt was modified: original=%v, after=%v", expectedExpiresAt, group2After.expiresAt)
	}
	if group2After.lastAccessNumber != expectedAccessNumber {
		t.Fatalf("group2 lastAccessNumber was modified: original=%d, after=%d", expectedAccessNumber, group2After.lastAccessNumber)
	}
	matcher.mu.Unlock()

	// 6. Advance clock past group 2 expiration
	mockNow = mockNow.Add(15 * time.Minute)
	authIDsExpired, _, okExpired := matcher.LookupSession(sessionID)
	if okExpired || len(authIDsExpired) > 0 {
		t.Fatalf("expected ok=false for completely expired session, got %v ok=%v", authIDsExpired, okExpired)
	}
}

func TestMerklePrefixMatcher_LookupSession_ConflictingTrajectories(t *testing.T) {
	matcher := NewMerklePrefixMatcherWithConfig(MerklePrefixMatcherConfig{TTL: 10 * time.Minute})
	ns := "lcp:v1::openai::gpt-4o::caller"

	bindRes1 := matcher.BindFingerprintsWithResult(ns, []string{"turnA"}, 1, "auth-a")
	sessionID := bindRes1.SessionID

	// Manually inject a second group with different authID sharing the same sessionID
	matcher.mu.Lock()
	groupConflict := &lcpGroup{
		key:              sequenceKey([]string{"turnA", "turnB"}),
		namespace:        ns,
		authID:           "auth-b",
		sessionID:        sessionID,
		minPrefixLength:  1,
		fingerprints:     []string{"turnA", "turnB"},
		prefixKeys:       rollingPrefixKeys([]string{"turnA", "turnB"}),
		expiresAt:        time.Now().Add(10 * time.Minute),
		lastAccessNumber: matcher.nextAccessNumberLocked(),
	}
	matcher.addGroupLocked(groupConflict)
	matcher.mu.Unlock()

	authIDs, _, ok := matcher.LookupSession(sessionID)
	if !ok {
		t.Fatalf("expected ok=true")
	}
	expected := []string{"auth-a", "auth-b"}
	if !reflect.DeepEqual(authIDs, expected) {
		t.Fatalf("expected conflicting auths %v, got %v", expected, authIDs)
	}
}
