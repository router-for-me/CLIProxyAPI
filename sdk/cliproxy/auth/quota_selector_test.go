package auth

import (
	"context"
	"math/rand/v2"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// newTestQuotaSelector returns a selector whose background poller is disarmed
// and whose randomness is seeded deterministically.
func newTestQuotaSelector(seed uint64) *QuotaAwareSelector {
	s := NewQuotaAwareSelector()
	s.startOnce.Do(func() {})
	src := rand.New(rand.NewPCG(seed, seed))
	s.randFloat = src.Float64
	return s
}

func (s *QuotaAwareSelector) setQuota(authID string, snapshot quotaSnapshot) {
	snapshot.fetchedAt = time.Now()
	s.mu.Lock()
	s.quotas[authID] = snapshot
	s.mu.Unlock()
}

func TestQuotaAwareSelector_ProportionalDistribution(t *testing.T) {
	t.Parallel()

	selector := newTestQuotaSelector(42)
	auths := []*Auth{
		{ID: "big", Provider: "claude"},
		{ID: "small", Provider: "claude"},
	}
	// big: 10% used everywhere -> 90 headroom; small: 70% used -> 30 headroom.
	selector.setQuota("big", quotaSnapshot{session: 10, weeklyAll: 10, scoped: map[string]float64{}})
	selector.setQuota("small", quotaSnapshot{session: 70, weeklyAll: 40, scoped: map[string]float64{}})

	counts := map[string]int{}
	for i := 0; i < 10000; i++ {
		got, err := selector.Pick(context.Background(), "claude", "claude-opus-5", cliproxyexecutor.Options{}, auths)
		if err != nil {
			t.Fatalf("Pick() #%d error = %v", i, err)
		}
		counts[got.ID]++
	}

	ratio := float64(counts["big"]) / float64(counts["small"])
	if ratio < 2.5 || ratio > 3.5 {
		t.Fatalf("pick ratio big/small = %.2f (big=%d small=%d), want ~3.0", ratio, counts["big"], counts["small"])
	}
}

func TestQuotaAwareSelector_ZeroHeadroomExcluded(t *testing.T) {
	t.Parallel()

	selector := newTestQuotaSelector(7)
	auths := []*Auth{
		{ID: "healthy", Provider: "claude"},
		{ID: "exhausted", Provider: "claude"},
	}
	selector.setQuota("healthy", quotaSnapshot{session: 20, weeklyAll: 20, scoped: map[string]float64{}})
	// 96% on the Opus weekly tier: excluded for opus models despite low overall use.
	selector.setQuota("exhausted", quotaSnapshot{session: 30, weeklyAll: 30, scoped: map[string]float64{"opus": 96}})

	for i := 0; i < 1000; i++ {
		got, err := selector.Pick(context.Background(), "claude", "claude-opus-5", cliproxyexecutor.Options{}, auths)
		if err != nil {
			t.Fatalf("Pick() #%d error = %v", i, err)
		}
		if got.ID != "healthy" {
			t.Fatalf("Pick() #%d selected exhausted auth %q", i, got.ID)
		}
	}
}

func TestQuotaAwareSelector_TierScopedOnlyAffectsMatchingModel(t *testing.T) {
	t.Parallel()

	selector := newTestQuotaSelector(7)
	auths := []*Auth{
		{ID: "opus-full", Provider: "claude"},
	}
	selector.setQuota("opus-full", quotaSnapshot{session: 10, weeklyAll: 10, scoped: map[string]float64{"opus": 100}})

	// Non-opus model: the Opus tier limit must not exclude the auth.
	got, err := selector.Pick(context.Background(), "claude", "claude-opus-5", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() error = %v", err)
	}
	if got.ID != "opus-full" {
		t.Fatalf("Pick() auth.ID = %q, want opus-full", got.ID)
	}

	// Opus model: same auth is exhausted, so selection degrades to round-robin
	// (it is the only candidate, so it is still returned via the fallback).
	got, err = selector.Pick(context.Background(), "claude", "claude-opus-5", cliproxyexecutor.Options{}, auths)
	if err != nil {
		t.Fatalf("Pick() opus error = %v", err)
	}
	if got.ID != "opus-full" {
		t.Fatalf("Pick() opus auth.ID = %q, want opus-full via fallback", got.ID)
	}
}

func TestQuotaAwareSelector_AllUnknownDegradesToRoundRobin(t *testing.T) {
	t.Parallel()

	selector := newTestQuotaSelector(1)
	auths := []*Auth{
		{ID: "b", Provider: "claude"},
		{ID: "a", Provider: "claude"},
		{ID: "c", Provider: "gemini"},
	}
	// No quota snapshots at all: every candidate is unknown.

	want := []string{"a", "b", "c", "a", "b"}
	for i, id := range want {
		got, err := selector.Pick(context.Background(), "mixed", "some-model", cliproxyexecutor.Options{}, auths)
		if err != nil {
			t.Fatalf("Pick() #%d error = %v", i, err)
		}
		if got.ID != id {
			t.Fatalf("Pick() #%d auth.ID = %q, want %q (round-robin order)", i, got.ID, id)
		}
	}
}

func TestQuotaAwareSelector_StaleQuotaCountsAsUnknown(t *testing.T) {
	t.Parallel()

	selector := newTestQuotaSelector(1)
	auths := []*Auth{
		{ID: "a", Provider: "claude"},
		{ID: "b", Provider: "claude"},
	}
	selector.mu.Lock()
	selector.quotas["a"] = quotaSnapshot{
		fetchedAt: time.Now().Add(-2 * quotaStaleAfter),
		session:   0, weeklyAll: 0, scoped: map[string]float64{},
	}
	selector.mu.Unlock()

	// "a" has huge headroom but the data is stale, "b" is unknown: both zero
	// weight, so round-robin order (sorted by ID) must apply.
	want := []string{"a", "b", "a", "b"}
	for i, id := range want {
		got, err := selector.Pick(context.Background(), "claude", "m", cliproxyexecutor.Options{}, auths)
		if err != nil {
			t.Fatalf("Pick() #%d error = %v", i, err)
		}
		if got.ID != id {
			t.Fatalf("Pick() #%d auth.ID = %q, want %q", i, got.ID, id)
		}
	}
}

func TestParseQuotaUsage(t *testing.T) {
	t.Parallel()

	body := []byte(`{"limits":[
		{"kind":"session","percent":41,"resets_at":"2026-07-29T12:00:00Z"},
		{"kind":"weekly_all","percent":63},
		{"kind":"weekly_scoped","percent":97,"scope":{"model":{"display_name":"Opus"}}}
	]}`)
	snapshot := parseQuotaUsage(body)
	if snapshot.session != 41 {
		t.Fatalf("session = %v, want 41", snapshot.session)
	}
	if snapshot.weeklyAll != 63 {
		t.Fatalf("weeklyAll = %v, want 63", snapshot.weeklyAll)
	}
	if got := snapshot.scoped["opus"]; got != 97 {
		t.Fatalf("scoped[opus] = %v, want 97", got)
	}
}
