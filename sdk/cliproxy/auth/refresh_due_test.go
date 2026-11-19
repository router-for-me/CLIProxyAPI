package auth

import (
	"context"
	"testing"
	"time"
)

func TestRefreshDueRefreshesOnlyWhatTheLoopWould(t *testing.T) {
	exec := &countingRefreshExecutor{id: issue6199RefreshProvider}
	manager := NewManager(nil, &RoundRobinSelector{}, nil)
	manager.RegisterExecutor(exec)
	now := time.Now()
	registerIssue6199ExpiredAuth(t, manager, "due", now)
	fresh := &Auth{ID: "fresh", Provider: issue6199RefreshProvider, Status: StatusActive, LastRefreshedAt: now,
		Metadata: map[string]any{"access_token": "a", "refresh_token": "r", "expires_at": now.Add(24 * time.Hour).Format(time.RFC3339)}}
	if _, err := manager.Register(context.Background(), fresh); err != nil {
		t.Fatal(err)
	}
	got := manager.RefreshDue(context.Background())
	if len(got) != 1 || got[0].ID != "due" || !got[0].Success || exec.refreshCalls.Load() != 1 {
		t.Fatalf("results %+v, refresh calls %d", got, exec.refreshCalls.Load())
	}
}
