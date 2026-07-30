package store

import (
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestUsageFlusherHandleUsageQueuesRecord(t *testing.T) {
	store := newTestPostgresStore(t, "flusher_test")
	ctx := cancelableTestCtx(t)
	us := NewUsageStore(store)
	apiKeys := NewAPIKeyStore(store)
	flusher := NewUsageFlusher(us, apiKeys, FlusherConfig{QueueCap: 10, FlushInterval: time.Hour, FlushBatchSize: 5})
	if err := flusher.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(flusher.Stop)

	key, _, err := apiKeys.Create(ctx, "fk", "", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Upsert pricing for the model so cost is computed.
	if err := us.UpsertPricing(ctx, Pricing{ID: "m", InputPer1M: 1.0, OutputPer1M: 2.0}); err != nil {
		t.Fatalf("UpsertPricing: %v", err)
	}

	rec := coreusage.Record{
		Provider: "test", Model: "m", APIKey: "fake-secret", AuthType: "api_key", Source: "test",
		RequestedAt: time.Now().UTC(),
		Detail: coreusage.Detail{
			InputTokens: 1_000_000, OutputTokens: 1_000_000,
			TotalTokens: 2_000_000,
		},
	}
	// We can't easily tie into the async loop without a sleep, but we can
	// exercise HandleUsage enqueues without dropping.
	for i := 0; i < 5; i++ {
		flusher.HandleUsage(ctx, rec)
	}
	// Drain queue synchronously so we can assert persistence.
	// (the auto-Hourly ticker won't fire during the test).
	// Force flush via the internal drain by stopping + restarting.
	flusher.Stop()

	aggs, err := us.SelectAggregate(ctx, UsageFilter{APIKeyID: key.ID, GroupBy: "model"})
	if err != nil {
		t.Fatalf("SelectAggregate: %v", err)
	}
	if len(aggs) == 0 {
		t.Fatal("expected at least one aggregated row; got none")
	}
	if aggs[0].RequestCount == 0 {
		t.Fatalf("expected non-zero request count; got 0; agg=%+v", aggs[0])
	}
	// The principal of fake-secret won't resolve to our PG-managed key
	// (different secret) — so the free-text api_key_principal column is used.
	// Cost should reflect pricing * 1M each = 1+2 = 3.
	if aggs[0].CostUSD < 14.99 || aggs[0].CostUSD > 15.01 {
		t.Errorf("cost = %v; want ~15.0", aggs[0].CostUSD)
	}
}

// TestUsageFlusherFallsBackToAliasPricing reproduces the reported cost=0 for
// glm-5.2: pricing rows are keyed by the client-facing alias (glm-5.2), but
// usage records carry the resolved upstream model as Model (glm-5.2-flex).
// Before the fix, GetPricing("glm-5.2-flex") missed and cost stayed 0.
// ResolvePricing must fall back to record.Alias so the alias-keyed row wins.
//
// Asserts on the cost_usd persisted to the event row (what the dashboard's
// Recent Events shows), filtered by the resolved model — the exact reported
// symptom.
func TestUsageFlusherFallsBackToAliasPricing(t *testing.T) {
	store := newTestPostgresStore(t, "flusher_alias_pricing")
	ctx := cancelableTestCtx(t)
	us := NewUsageStore(store)
	flusher := NewUsageFlusher(us, nil, FlusherConfig{QueueCap: 10, FlushInterval: time.Hour, FlushBatchSize: 5})
	if err := flusher.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(flusher.Stop)

	// Pricing authored under the client-facing alias glm-5.2, NOT the
	// resolved upstream glm-5.2-flex. This is how the catalog sync keys rows.
	if err := us.UpsertPricing(ctx, Pricing{ID: "glm-5.2", InputPer1M: 1.0, OutputPer1M: 2.0}); err != nil {
		t.Fatalf("UpsertPricing: %v", err)
	}

	// Record carries the resolved upstream model (glm-5.2-flex) as Model and
	// the client-requested name (glm-5.2) as Alias — the real glm-5.2 shape.
	flusher.HandleUsage(ctx, coreusage.Record{
		Provider: "zai", Model: "glm-5.2-flex", Alias: "glm-5.2",
		AuthType: "apikey", Source: "test2",
		RequestedAt: time.Now().UTC(),
		Detail: coreusage.Detail{
			InputTokens: 2_000_000, OutputTokens: 2_000_000,
			TotalTokens: 4_000_000,
		},
	})
	flusher.Stop()

	rows, _, err := us.SelectEvents(ctx, UsageFilter{Model: "glm-5.2-flex"}, 1, 25)
	if err != nil {
		t.Fatalf("SelectEvents: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 event for glm-5.2-flex; got %d", len(rows))
	}
	// 2M input @ $1/M + 2M output @ $2/M = 2 + 4 = 6. Before the fix this
	// was 0 because GetPricing("glm-5.2-flex") missed.
	if rows[0].CostUSD < 5.99 || rows[0].CostUSD > 6.01 {
		t.Errorf("cost_usd = %v; want ~6.0 (alias fallback to glm-5.2 pricing)", rows[0].CostUSD)
	}
	// Alias must be persisted so the breakdown resolver can fall back too.
	if rows[0].Alias != "glm-5.2" {
		t.Errorf("alias = %q; want glm-5.2", rows[0].Alias)
	}
}

func TestUsageFlusherDropsOnFullQueue(t *testing.T) {
	// Use a zero-cap queue via direct construction — we cannot pass cap=0
	// because NewUsageFlusher falls back to defaults. Instead, set cap=1
	// and flood it faster than the (long) ticker.
	store := newTestPostgresStore(t, "flusher_test2")
	ctx := cancelableTestCtx(t)
	us := NewUsageStore(store)
	flusher := NewUsageFlusher(us, nil, FlusherConfig{QueueCap: 1, FlushInterval: time.Hour, FlushBatchSize: 1})
	if err := flusher.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(flusher.Stop)
	// 1 record fits, the rest should drop because the queue holds 1 and the
	// loop will pick it up but not immediately enough for our synchronous check.
	for i := 0; i < 10; i++ {
		flusher.HandleUsage(ctx, coreusage.Record{Provider: "x", Model: "m", RequestedAt: time.Now().UTC()})
	}
	if flusher.Drops() == 0 {
		t.Fatal("expected drops > 0; got 0 (queue may have been drained, but some should drop on cap=1)")
	}
}

func TestUsageFlusherStartTwice(t *testing.T) {
	store := newTestPostgresStore(t, "flusher_start2")
	ctx := cancelableTestCtx(t)
	us := NewUsageStore(store)
	flusher := NewUsageFlusher(us, nil, DefaultFlusherConfig())
	if err := flusher.Start(ctx); err != nil {
		t.Fatalf("Start (1): %v", err)
	}
	if err := flusher.Start(ctx); err == nil {
		t.Fatal("expected error on second Start; got nil")
	}
	t.Cleanup(flusher.Stop)
}
