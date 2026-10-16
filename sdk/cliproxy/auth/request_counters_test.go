package auth

import (
	"context"
	"testing"
)

func TestRequestCountersSnapshotAndApply(t *testing.T) {
	m := NewManager(nil, nil, nil)
	auth := &Auth{ID: "auth-counters-1", Provider: "codex"}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}

	for i := 0; i < 3; i++ {
		m.MarkResult(context.Background(), Result{
			AuthID:   auth.ID,
			Provider: auth.Provider,
			Model:    "gpt-5.5",
			Success:  true,
		})
	}
	m.MarkResult(context.Background(), Result{
		AuthID:   auth.ID,
		Provider: auth.Provider,
		Model:    "gpt-5.5",
		Success:  false,
	})

	if m.RequestCountersRevision() == 0 {
		t.Fatal("RequestCountersRevision must advance after recorded requests")
	}

	snapshot := m.RequestCountersSnapshot()
	if len(snapshot) != 1 {
		t.Fatalf("credentials = %d, want 1: %+v", len(snapshot), snapshot)
	}
	if snapshot[0].Success != 3 || snapshot[0].Failed != 1 {
		t.Fatalf("counters = (%d, %d), want (3, 1)", snapshot[0].Success, snapshot[0].Failed)
	}
	if len(snapshot[0].RecentRequests) == 0 {
		t.Fatal("expected at least one recent-request bucket")
	}

	// A fresh manager starts at zero and must adopt the persisted counters.
	m2 := NewManager(nil, nil, nil)
	auth2 := &Auth{ID: "auth-counters-1", Provider: "codex"}
	if _, errRegister := m2.Register(context.Background(), auth2); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	if before := m2.RequestCountersSnapshot(); len(before) != 1 || before[0].Success != 0 {
		t.Fatalf("fresh manager should start at zero: %+v", before)
	}
	if applied := m2.ApplyRequestCounters(snapshot); applied != 1 {
		t.Fatalf("applied = %d, want 1", applied)
	}

	restored := m2.RequestCountersSnapshot()
	if len(restored) != 1 {
		t.Fatalf("restored credentials = %d, want 1", len(restored))
	}
	if restored[0].Success != 3 || restored[0].Failed != 1 {
		t.Fatalf("restored counters = (%d, %d), want (3, 1)", restored[0].Success, restored[0].Failed)
	}
	if len(restored[0].RecentRequests) != len(snapshot[0].RecentRequests) {
		t.Fatalf("restored buckets = %d, want %d", len(restored[0].RecentRequests), len(snapshot[0].RecentRequests))
	}
	for i := range restored[0].RecentRequests {
		if restored[0].RecentRequests[i] != snapshot[0].RecentRequests[i] {
			t.Fatalf("bucket %d = %+v, want %+v", i, restored[0].RecentRequests[i], snapshot[0].RecentRequests[i])
		}
	}
}

func TestApplyRequestCountersIgnoresUnknownAndStale(t *testing.T) {
	m := NewManager(nil, nil, nil)
	auth := &Auth{ID: "auth-counters-2", Provider: "codex"}
	if _, errRegister := m.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	m.MarkResult(context.Background(), Result{AuthID: auth.ID, Provider: auth.Provider, Model: "m", Success: true})

	applied := m.ApplyRequestCounters([]AuthRequestCounts{
		{AuthID: "missing-auth", Success: 99},
		{AuthID: "auth-counters-2", Success: 0, Failed: 0},
	})
	if applied != 1 {
		t.Fatalf("applied = %d, want 1 (unknown auth skipped)", applied)
	}

	after := m.RequestCountersSnapshot()
	if len(after) != 1 || after[0].Success != 1 {
		t.Fatalf("counters must not move backwards: %+v", after)
	}
}
