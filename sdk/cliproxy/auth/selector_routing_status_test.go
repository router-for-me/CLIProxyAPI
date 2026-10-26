package auth

import (
	"context"
	"testing"
	"time"
)

func TestQuotaRoutingSnapshot(t *testing.T) {
	selector := &SoonestResetFirstSelector{}
	manager := NewManager(nil, selector, nil)
	a, err := manager.Register(WithSkipPersist(context.Background()), &Auth{ID: "private-id", Provider: "codex"})
	if err != nil {
		t.Fatal(err)
	}
	hint := QuotaResetHint{ObservedAt: time.Now().Add(-time.Hour), ResetAt: time.Now().Add(time.Hour)}
	if err := manager.ReplaceQuotaResetHints(map[string]QuotaResetHint{a.ID: hint, "removed": hint}); err != nil {
		t.Fatal(err)
	}
	got := manager.QuotaRoutingSnapshot()
	if got.Strategy != "soonest-reset-first" || got.MaxAgeSeconds != 900 || len(got.Hints) != 1 || got.Hints[a.Index] != hint {
		t.Fatalf("unexpected snapshot: %+v", got)
	}
	// Include stale hints so the UI can explain expiry; return a detached copy.
	delete(got.Hints, a.Index)
	if len(manager.QuotaRoutingSnapshot().Hints) != 1 {
		t.Fatal("snapshot aliases live state")
	}
	affinity := NewSessionAffinitySelector(selector)
	defer affinity.Stop()
	manager.SetSelector(affinity)
	if !manager.QuotaRoutingSnapshot().SessionAffinity {
		t.Fatal("missing session affinity")
	}
	manager.SetSelector(&WeightedRoundRobinSelector{})
	got = manager.QuotaRoutingSnapshot()
	if got.Strategy != "weighted-round-robin" || len(got.Hints) != 0 {
		t.Fatalf("replacement: %+v", got)
	}
}
