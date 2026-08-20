package store

import (
	"context"
	"testing"
	"time"
)

func insertTestEvent(t *testing.T, us *UsageStore, ctx context.Context, e UsageEvent) {
	t.Helper()
	if err := us.InsertEvent(ctx, e); err != nil {
		t.Fatalf("InsertEvent: %v", err)
	}
}

func TestSelectAutoRouterTierStats(t *testing.T) {
	store := newTestPostgresStore(t, "auto_router_tier_stats")
	ctx := cancelableTestCtx(t)
	us := NewUsageStore(store)

	insert := func(e UsageEvent) { insertTestEvent(t, us, ctx, e) }

	// Routed events across the 4 canonical tiers for router:smart.
	insert(UsageEvent{Provider: "p", Model: "m1", Tier: "simple", RouterID: "router:smart", RequestedAt: time.Now().UTC(), TotalTokens: 100, CostUSD: 1.0})
	insert(UsageEvent{Provider: "p", Model: "m1", Tier: "simple", RouterID: "router:smart", RequestedAt: time.Now().UTC(), TotalTokens: 200, CostUSD: 2.0})
	insert(UsageEvent{Provider: "p", Model: "m2", Tier: "medium", RouterID: "router:smart", RequestedAt: time.Now().UTC(), TotalTokens: 300, CostUSD: 3.0})
	insert(UsageEvent{Provider: "p", Model: "m3", Tier: "complex", RouterID: "router:smart", RequestedAt: time.Now().UTC(), TotalTokens: 400, CostUSD: 4.0})
	insert(UsageEvent{Provider: "p", Model: "m4", Tier: "reasoning", RouterID: "router:smart", RequestedAt: time.Now().UTC(), TotalTokens: 500, CostUSD: 5.0})
	// A different router's event must not leak in.
	insert(UsageEvent{Provider: "p", Model: "m1", Tier: "simple", RouterID: "router:other", RequestedAt: time.Now().UTC(), TotalTokens: 999, CostUSD: 99.0})
	// A non-routed event (NULL tier) must be excluded entirely.
	insert(UsageEvent{Provider: "p", Model: "m0", RequestedAt: time.Now().UTC(), TotalTokens: 50, CostUSD: 0.5})

	stats, err := us.SelectAutoRouterTierStats(ctx, UsageFilter{RouterID: "router:smart"})
	if err != nil {
		t.Fatalf("SelectAutoRouterTierStats: %v", err)
	}
	if len(stats) != 4 {
		t.Fatalf("expected exactly 4 canonical tiers; got %d: %+v", len(stats), stats)
	}
	byTier := map[string]AutoRouterTierStat{}
	for _, st := range stats {
		byTier[st.Tier] = st
	}
	// simple: 2 events, 300 tokens, $3
	if st := byTier["simple"]; st.RequestCount != 2 || st.TotalTokens != 300 || st.CostUSD != 3.0 {
		t.Errorf("simple = %+v; want 2/300/3.0", st)
	}
	// medium: 1 event, 300 tokens, $3
	if st := byTier["medium"]; st.RequestCount != 1 || st.TotalTokens != 300 || st.CostUSD != 3.0 {
		t.Errorf("medium = %+v; want 1/300/3.0", st)
	}
	// complex: 1 event
	if st := byTier["complex"]; st.RequestCount != 1 || st.TotalTokens != 400 || st.CostUSD != 4.0 {
		t.Errorf("complex = %+v; want 1/400/4.0", st)
	}
	// reasoning: 1 event
	if st := byTier["reasoning"]; st.RequestCount != 1 || st.TotalTokens != 500 || st.CostUSD != 5.0 {
		t.Errorf("reasoning = %+v; want 1/500/5.0", st)
	}

	// Empty RouterID must error.
	if _, err := us.SelectAutoRouterTierStats(ctx, UsageFilter{}); err == nil {
		t.Fatal("expected error for empty router_id")
	}
}

func TestSelectAutoRouterModelStats(t *testing.T) {
	store := newTestPostgresStore(t, "auto_router_model_stats")
	ctx := cancelableTestCtx(t)
	us := NewUsageStore(store)

	insert := func(e UsageEvent) { insertTestEvent(t, us, ctx, e) }

	// Two events for model a (total cost $5), one for model b ($3), one for c ($0.5).
	insert(UsageEvent{Provider: "p", Model: "m-a", Tier: "simple", RouterID: "router:smart", RequestedAt: time.Now().UTC(), TotalTokens: 100, CostUSD: 2.0})
	insert(UsageEvent{Provider: "p", Model: "m-a", Tier: "medium", RouterID: "router:smart", RequestedAt: time.Now().UTC(), TotalTokens: 200, CostUSD: 3.0})
	insert(UsageEvent{Provider: "p", Model: "m-b", Tier: "complex", RouterID: "router:smart", RequestedAt: time.Now().UTC(), TotalTokens: 300, CostUSD: 3.0})
	insert(UsageEvent{Provider: "p", Model: "m-c", Tier: "reasoning", RouterID: "router:smart", RequestedAt: time.Now().UTC(), TotalTokens: 50, CostUSD: 0.5})
	// Different router must not leak in.
	insert(UsageEvent{Provider: "p", Model: "m-zzz", Tier: "simple", RouterID: "router:other", RequestedAt: time.Now().UTC(), TotalTokens: 999, CostUSD: 99.0})

	stats, err := us.SelectAutoRouterModelStats(ctx, UsageFilter{RouterID: "router:smart"})
	if err != nil {
		t.Fatalf("SelectAutoRouterModelStats: %v", err)
	}
	if len(stats) != 3 {
		t.Fatalf("expected 3 models; got %d: %+v", len(stats), stats)
	}
	// Ordered by cost DESC: m-a (5), m-b (3), m-c (0.5).
	order := []string{"m-a", "m-b", "m-c"}
	for i, want := range order {
		if stats[i].Model != want {
			t.Errorf("stats[%d].Model = %q; want %q", i, stats[i].Model, want)
		}
	}
	// m-a: 2 requests, cost 5 → avg 2.5
	if stats[0].RequestCount != 2 || stats[0].CostUSD != 5.0 || stats[0].AvgCostPerReq != 2.5 {
		t.Errorf("m-a = %+v; want 2 req / $5 / $2.5 avg", stats[0])
	}
	// m-b: 1 request, cost 3 → avg 3
	if stats[1].RequestCount != 1 || stats[1].CostUSD != 3.0 || stats[1].AvgCostPerReq != 3.0 {
		t.Errorf("m-b = %+v; want 1 req / $3 / $3 avg", stats[1])
	}
	// m-c: 1 request, cost 0.5 → avg 0.5
	if stats[2].RequestCount != 1 || stats[2].CostUSD != 0.5 || stats[2].AvgCostPerReq != 0.5 {
		t.Errorf("m-c = %+v; want 1 req / $0.5 / $0.5 avg", stats[2])
	}

	// scoped by api_key_id.
	insert(UsageEvent{Provider: "p", Model: "m-d", APIKeyID: "key-1", Tier: "simple", RouterID: "router:smart", RequestedAt: time.Now().UTC(), TotalTokens: 10, CostUSD: 10.0})
	insert(UsageEvent{Provider: "p", Model: "m-d", APIKeyID: "key-2", Tier: "simple", RouterID: "router:smart", RequestedAt: time.Now().UTC(), TotalTokens: 10, CostUSD: 20.0})
	scoped, err := us.SelectAutoRouterModelStats(ctx, UsageFilter{RouterID: "router:smart", APIKeyID: "key-2"})
	if err != nil {
		t.Fatalf("SelectAutoRouterModelStats scoped: %v", err)
	}
	found := false
	for _, st := range scoped {
		if st.Model == "m-d" {
			found = true
			if st.RequestCount != 1 || st.CostUSD != 20.0 || st.AvgCostPerReq != 20.0 {
				t.Errorf("m-d scoped = %+v; want 1 req / $20 / $20 avg", st)
			}
		}
	}
	if !found {
		t.Error("expected m-d in api-key scoped result")
	}

	// Empty RouterID must error.
	if _, err := us.SelectAutoRouterModelStats(ctx, UsageFilter{}); err == nil {
		t.Fatal("expected error for empty router_id")
	}
}

// TestSelectAutoRouterModelStatsExcludesNonRouted verifies that the per-model
// aggregation excludes events whose tier is NULL (non-routed rows), even if
// their router_id matches the filter. Without the NULL-tier guard, a non-routed
// row that happens to carry a router_id would leak into the per-target-model
// rollup and skew the cost totals.
func TestSelectAutoRouterModelStatsExcludesNonRouted(t *testing.T) {
	store := newTestPostgresStore(t, "auto_router_model_stats_unrouted")
	ctx := cancelableTestCtx(t)
	us := NewUsageStore(store)

	insert := func(e UsageEvent) { insertTestEvent(t, us, ctx, e) }

	// One routed event — must show up.
	insert(UsageEvent{Provider: "p", Model: "m-a", Tier: "simple", RouterID: "router:smart", RequestedAt: time.Now().UTC(), TotalTokens: 100, CostUSD: 2.0})
	// A non-routed event (NULL tier) that shares the router_id — must NOT leak in.
	insert(UsageEvent{Provider: "p", Model: "m-leak", Tier: "", RouterID: "router:smart", RequestedAt: time.Now().UTC(), TotalTokens: 999, CostUSD: 99.0})

	stats, err := us.SelectAutoRouterModelStats(ctx, UsageFilter{RouterID: "router:smart"})
	if err != nil {
		t.Fatalf("SelectAutoRouterModelStats: %v", err)
	}
	if len(stats) != 1 {
		t.Fatalf("expected 1 model (the null-tier row excluded); got %d: %+v", len(stats), stats)
	}
	if stats[0].Model != "m-a" {
		t.Errorf("model = %q, want m-a", stats[0].Model)
	}
	if stats[0].CostUSD != 2.0 {
		t.Errorf("cost = %v, want 2.0 (null-tier row must not contribute)", stats[0].CostUSD)
	}
}

func TestSelectAggregateRouterIDFilter(t *testing.T) {
	store := newTestPostgresStore(t, "aggregate_router_id")
	ctx := cancelableTestCtx(t)
	us := NewUsageStore(store)

	insert := func(e UsageEvent) { insertTestEvent(t, us, ctx, e) }
	insert(UsageEvent{Provider: "p", Model: "m", Tier: "simple", RouterID: "router:smart", RequestedAt: time.Now().UTC(), TotalTokens: 100, CostUSD: 1.0})
	insert(UsageEvent{Provider: "p", Model: "m", Tier: "simple", RouterID: "router:other", RequestedAt: time.Now().UTC(), TotalTokens: 100, CostUSD: 2.0})
	insert(UsageEvent{Provider: "p", Model: "m", RequestedAt: time.Now().UTC(), TotalTokens: 100, CostUSD: 3.0})

	// Unfiltered → 3 rows.
	all, err := us.SelectAggregate(ctx, UsageFilter{Model: "m"})
	if err != nil {
		t.Fatalf("SelectAggregate(all): %v", err)
	}
	if len(all) != 1 || all[0].RequestCount != 3 {
		t.Fatalf("unfiltered = %+v; want 3 requests", all)
	}

	// Filtered by router:smart → 1 request costing $1.
	smart, err := us.SelectAggregate(ctx, UsageFilter{Model: "m", RouterID: "router:smart"})
	if err != nil {
		t.Fatalf("SelectAggregate(smart): %v", err)
	}
	if len(smart) != 1 || smart[0].RequestCount != 1 {
		t.Fatalf("router:smart = %+v; want 1 request", smart)
	}
	if smart[0].CostUSD != 1.0 {
		t.Errorf("router:smart cost = %v; want 1.0", smart[0].CostUSD)
	}
}
