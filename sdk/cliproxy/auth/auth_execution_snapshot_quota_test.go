package auth

import "testing"

func TestAuthPolicySnapshotDetachesEmptyQuotaMaps(t *testing.T) {
	a := &Auth{
		Metadata:    map[string]any{"access_token": "synthetic-a"},
		Quota:       QuotaState{Signals: map[string]string{}},
		ModelStates: map[string]*ModelState{"fixture-model": {Quota: QuotaState{Signals: map[string]string{}}}},
	}
	snapshot, err := snapshotAuthForPolicy(a)
	if err != nil || snapshot == nil || snapshot.Quota.Signals == nil || snapshot.ModelStates["fixture-model"].Quota.Signals == nil {
		t.Fatal("empty quota snapshot was not preserved")
	}
	snapshot.Quota.Signals["fixture"] = "changed"
	snapshot.ModelStates["fixture-model"].Quota.Signals["fixture"] = "changed"
	if len(a.Quota.Signals) != 0 || len(a.ModelStates["fixture-model"].Quota.Signals) != 0 {
		t.Fatal("policy snapshot insertion changed original quota state")
	}
}
