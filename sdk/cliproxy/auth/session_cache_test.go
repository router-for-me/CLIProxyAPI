package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

func TestSessionCacheStatsCountsOnlyActiveBindings(t *testing.T) {
	t.Parallel()

	cache := NewSessionCache(time.Hour)
	defer cache.Stop()

	cache.setBinding("provider::session-a::model-a", "session-a", "auth-a")
	cache.setBinding("provider::session-a::model-b", "session-a", "auth-a")

	cache.mu.Lock()
	activeExpiry := cache.entries["provider::session-a::model-a"].expiresAt
	cache.entries["provider::expired::model-a"] = sessionEntry{
		authID:           "auth-b",
		logicalSessionID: "expired",
		expiresAt:        time.Now().Add(-time.Minute),
	}
	cache.mu.Unlock()

	stats := cache.Stats()
	if stats.ActiveBindings != 2 {
		t.Fatalf("Stats().ActiveBindings = %d, want 2", stats.ActiveBindings)
	}
	if stats.ActiveSessions != 1 {
		t.Fatalf("Stats().ActiveSessions = %d, want 1", stats.ActiveSessions)
	}
	cache.mu.RLock()
	gotExpiry := cache.entries["provider::session-a::model-a"].expiresAt
	cache.mu.RUnlock()
	if !gotExpiry.Equal(activeExpiry) {
		t.Fatalf("Stats() refreshed expiry to %v, want %v", gotExpiry, activeExpiry)
	}
}

func TestNilSessionCacheStats(t *testing.T) {
	t.Parallel()

	var cache *SessionCache
	if stats := cache.Stats(); stats.ActiveBindings != 0 || stats.ActiveSessions != 0 {
		t.Fatalf("Stats() = %+v, want zero value", stats)
	}
}

func TestManagerSessionAffinityStats(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Hour,
	})
	defer selector.Stop()
	auths := []*Auth{{ID: "auth-a"}}
	headers := make(http.Header)
	headers.Set("X-Session-ID", "session-a")
	opts := cliproxyexecutor.Options{Headers: headers}
	for _, model := range []string{"model-a", "model-b"} {
		if _, err := selector.Pick(context.Background(), "provider", model, opts, auths); err != nil {
			t.Fatalf("Pick(%q) error = %v", model, err)
		}
	}

	manager := NewManager(nil, selector, nil)
	stats, enabled := manager.SessionAffinityStats()
	if !enabled {
		t.Fatal("SessionAffinityStats() enabled = false, want true")
	}
	if stats.ActiveBindings != 2 {
		t.Fatalf("SessionAffinityStats().ActiveBindings = %d, want 2", stats.ActiveBindings)
	}
	if stats.ActiveSessions != 1 {
		t.Fatalf("SessionAffinityStats().ActiveSessions = %d, want 1", stats.ActiveSessions)
	}

	manager.SetSelector(&RoundRobinSelector{})
	stats, enabled = manager.SessionAffinityStats()
	if enabled {
		t.Fatal("SessionAffinityStats() enabled = true after selector change, want false")
	}
	if stats.ActiveBindings != 0 {
		t.Fatalf("SessionAffinityStats().ActiveBindings = %d after selector change, want 0", stats.ActiveBindings)
	}
	if stats.ActiveSessions != 0 {
		t.Fatalf("SessionAffinityStats().ActiveSessions = %d after selector change, want 0", stats.ActiveSessions)
	}
}

func TestSessionAffinityStatsGroupsFallbackKeyMigration(t *testing.T) {
	t.Parallel()

	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{
		Fallback: &RoundRobinSelector{},
		TTL:      time.Hour,
	})
	defer selector.Stop()
	auths := []*Auth{{ID: "auth-a"}}
	firstTurn := cliproxyexecutor.Options{OriginalRequest: []byte(`{
		"messages":[{"role":"user","content":"hello"}]
	}`)}
	laterTurn := cliproxyexecutor.Options{OriginalRequest: []byte(`{
		"messages":[
			{"role":"user","content":"hello"},
			{"role":"assistant","content":"hi"}
		]
	}`)}

	if _, err := selector.Pick(context.Background(), "provider", "model-a", firstTurn, auths); err != nil {
		t.Fatalf("first-turn Pick() error = %v", err)
	}
	if _, err := selector.Pick(context.Background(), "provider", "model-a", laterTurn, auths); err != nil {
		t.Fatalf("later-turn Pick() error = %v", err)
	}
	if _, err := selector.Pick(context.Background(), "provider", "model-b", laterTurn, auths); err != nil {
		t.Fatalf("second-model Pick() error = %v", err)
	}

	stats := selector.SessionAffinityStats()
	if stats.ActiveBindings != 3 {
		t.Fatalf("ActiveBindings = %d, want 3", stats.ActiveBindings)
	}
	if stats.ActiveSessions != 1 {
		t.Fatalf("ActiveSessions = %d, want 1", stats.ActiveSessions)
	}
}
