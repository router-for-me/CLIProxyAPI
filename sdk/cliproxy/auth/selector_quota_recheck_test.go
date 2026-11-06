package auth

import (
	"testing"
	"time"
)

func TestHighestPrioritySkipsCodexQuotaRecheckWhenAnotherCredentialIsReady(t *testing.T) {
	now := time.Now()
	exhausted := &Auth{
		ID:         "priority-exhausted",
		Provider:   "codex",
		Attributes: map[string]string{"priority": "1"},
		Quota:      QuotaState{RecheckUntil: now.Add(6 * 24 * time.Hour)},
	}
	ready := &Auth{
		ID:         "priority-ready",
		Provider:   "codex",
		Attributes: map[string]string{"priority": "0"},
	}

	got := highestPriorityAuths([]*Auth{exhausted, ready})
	if len(got) != 1 || got[0] != ready {
		t.Fatalf("selected %#v, want the ready lower-priority credential", idsOf(got))
	}

	onlyExhausted := highestPriorityAuths([]*Auth{exhausted})
	if len(onlyExhausted) != 1 || onlyExhausted[0] != exhausted {
		t.Fatalf("selected %#v, want the exhausted credential when it is the only candidate", idsOf(onlyExhausted))
	}
}

func TestAvailableAuthsSkipCodexQuotaRecheckAcrossPriorityTiers(t *testing.T) {
	now := time.Now()
	exhausted := &Auth{
		ID:         "tier-exhausted",
		Attributes: map[string]string{"priority": "2"},
		Quota:      QuotaState{RecheckUntil: now.Add(time.Hour)},
	}
	ready := &Auth{ID: "tier-ready"}
	got := availableAuthsFromPriorityBuckets(map[int][]*Auth{
		2: {exhausted},
		0: {ready},
	}, false)
	if len(got) != 1 || got[0] != ready {
		t.Fatalf("selected %#v, want the ready tier", idsOf(got))
	}
}

func idsOf(auths []*Auth) []string {
	ids := make([]string, 0, len(auths))
	for _, auth := range auths {
		if auth == nil {
			ids = append(ids, "")
			continue
		}
		ids = append(ids, auth.ID)
	}
	return ids
}
