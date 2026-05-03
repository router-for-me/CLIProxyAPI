package auth

import (
	"testing"
	"time"
)

func TestRefreshQueueSnapshotReturnsSortedQueuedAuths(t *testing.T) {
	now := time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC)
	manager := NewManager(nil, nil, nil)
	manager.auths["later"] = &Auth{
		ID:               "later",
		Provider:         "gemini",
		FileName:         "later.json",
		Status:           StatusActive,
		NextRefreshAfter: now.Add(2 * time.Hour),
		Metadata: map[string]any{
			"email": "later@example.com",
		},
	}
	manager.auths["soon"] = &Auth{
		ID:               "soon",
		Provider:         "gemini",
		FileName:         "soon.json",
		Status:           StatusActive,
		NextRefreshAfter: now.Add(30 * time.Minute),
		Metadata: map[string]any{
			"email": "soon@example.com",
		},
	}

	loop := newAuthAutoRefreshLoop(manager, time.Hour, 1)
	manager.refreshLoop = loop
	loop.rebuild(now)

	got := manager.RefreshQueueSnapshot(now)
	if len(got) != 2 {
		t.Fatalf("RefreshQueueSnapshot() len = %d, want 2", len(got))
	}
	if got[0].Auth.ID != "soon" {
		t.Fatalf("first auth ID = %q, want soon", got[0].Auth.ID)
	}
	if got[1].Auth.ID != "later" {
		t.Fatalf("second auth ID = %q, want later", got[1].Auth.ID)
	}
	if !got[0].NextRefreshAt.Equal(now.Add(30 * time.Minute)) {
		t.Fatalf("first next refresh = %s, want %s", got[0].NextRefreshAt, now.Add(30*time.Minute))
	}
	if !got[1].NextRefreshAt.Equal(now.Add(2 * time.Hour)) {
		t.Fatalf("second next refresh = %s, want %s", got[1].NextRefreshAt, now.Add(2*time.Hour))
	}
}

func TestRefreshQueueSnapshotAppliesDirtyReschedules(t *testing.T) {
	now := time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC)
	manager := NewManager(nil, nil, nil)
	auth := &Auth{
		ID:               "auth-1",
		Provider:         "gemini",
		FileName:         "auth-1.json",
		Status:           StatusActive,
		NextRefreshAfter: now.Add(time.Hour),
		Metadata: map[string]any{
			"email": "auth-1@example.com",
		},
	}
	manager.auths[auth.ID] = auth

	loop := newAuthAutoRefreshLoop(manager, time.Hour, 1)
	manager.refreshLoop = loop
	loop.rebuild(now)

	auth.NextRefreshAfter = now.Add(10 * time.Minute)
	loop.queueReschedule(auth.ID)

	got := manager.RefreshQueueSnapshot(now)
	if len(got) != 1 {
		t.Fatalf("RefreshQueueSnapshot() len = %d, want 1", len(got))
	}
	if !got[0].NextRefreshAt.Equal(now.Add(10 * time.Minute)) {
		t.Fatalf("next refresh = %s, want %s", got[0].NextRefreshAt, now.Add(10*time.Minute))
	}
}

func TestRefreshQueueSnapshotWithoutLoopReturnsEmpty(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	got := manager.RefreshQueueSnapshot(time.Date(2026, 5, 3, 10, 0, 0, 0, time.UTC))
	if len(got) != 0 {
		t.Fatalf("RefreshQueueSnapshot() len = %d, want 0", len(got))
	}
}
