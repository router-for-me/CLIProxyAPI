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
	flusher := NewUsageFlusher(us, apiKeys, nil, FlusherConfig{QueueCap: 10, FlushInterval: time.Hour, FlushBatchSize: 5})
	if err := flusher.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(flusher.Stop)

	// Use the key's real plaintext secret in the record so the flusher resolves
	// api_key_id via LookupByHash and the aggregate filter by key.ID below matches.
	key, secret, err := apiKeys.Create(ctx, "fk", "", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Upsert pricing for the model so cost is computed.
	if err := us.UpsertPricing(ctx, Pricing{ID: "m", InputPer1M: 1.0, OutputPer1M: 2.0}); err != nil {
		t.Fatalf("UpsertPricing: %v", err)
	}

	rec := coreusage.Record{
		Provider: "test", Model: "m", APIKey: secret, AuthType: "api_key", Source: "test",
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
	flusher := NewUsageFlusher(us, nil, nil, FlusherConfig{QueueCap: 10, FlushInterval: time.Hour, FlushBatchSize: 5})
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

func TestUsageFlusherAppliesModelGroupDiscount(t *testing.T) {
	store := newTestPostgresStore(t, "flusher_discount")
	ctx := cancelableTestCtx(t)
	us := NewUsageStore(store)
	apiKeys := NewAPIKeyStore(store)
	groups := NewModelGroupStore(store)
	flusher := NewUsageFlusher(us, apiKeys, groups, FlusherConfig{QueueCap: 10, FlushInterval: time.Hour, FlushBatchSize: 5})
	if err := flusher.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(flusher.Stop)

	// Create an API key whose principal the flusher can resolve via LookupByHash.
	// Create returns the plaintext secret (2nd return); we publish a record
	// carrying that exact principal so the discount lookup can resolve the
	// attached model group.
	key, plaintextSecret, err := apiKeys.Create(ctx, "dk", "", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Pricing: 1M input @ $1/M + 1M output @ $2/M = $3 pre-discount.
	if err := us.UpsertPricing(ctx, Pricing{ID: "m", InputPer1M: 1.0, OutputPer1M: 2.0}); err != nil {
		t.Fatalf("UpsertPricing: %v", err)
	}
	// Create a model group with a 20% default discount and attach it to the
	// key's policy. No per-model override is set, so the group default applies.
	dpct := 20.0
	grp, err := groups.Create(ctx, ModelGroup{
		Name: "discounted", AllowedModels: []string{"m"}, DiscountPct: &dpct,
	})
	if err != nil {
		t.Fatalf("Create group: %v", err)
	}
	if err := apiKeys.UpdatePolicy(ctx, key.ID, Policy{ModelGroupID: &grp.ID}); err != nil {
		t.Fatalf("UpdatePolicy: %v", err)
	}

	flusher.HandleUsage(ctx, coreusage.Record{
		Provider: "test", Model: "m", APIKey: plaintextSecret,
		AuthType: "api_key", Source: "test", RequestedAt: time.Now().UTC(),
		Detail: coreusage.Detail{
			InputTokens: 1_000_000, OutputTokens: 1_000_000, TotalTokens: 2_000_000,
		},
	})
	flusher.Stop()

	rows, _, err := us.SelectEvents(ctx, UsageFilter{APIKeyID: key.ID}, 1, 25)
	if err != nil {
		t.Fatalf("SelectEvents: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 event; got %d", len(rows))
	}
	row := rows[0]
	// Pre-discount = $3; with 20% off → $2.4 (post-discount stored as cost_usd).
	if row.CostUSD < 2.39 || row.CostUSD > 2.41 {
		t.Errorf("cost_usd = %v; want ~2.4 (3.0 × 0.8)", row.CostUSD)
	}
	if row.DiscountPct < 19.99 || row.DiscountPct > 20.01 {
		t.Errorf("discount_pct = %v; want 20", row.DiscountPct)
	}
	// original_cost_usd is the pre-discount figure, stamped persistently at
	// flush time (NOT re-derived on read). Must equal the pre-discount cost
	// (3.0) so the dashboard's "was $X" reads correctly even before
	// FillCostBreakdown runs.
	if row.OriginalCostUSD < 2.99 || row.OriginalCostUSD > 3.01 {
		t.Errorf("original_cost_usd = %v; want ~3.0 (persisted at flush, pre-discount)", row.OriginalCostUSD)
	}
}

// TestUsageFlusherAppliesPerModelDiscountCaseInsensitive guards that a
// per-model discount authored under a mixed-case model id (e.g. "GPT-4O")
// still applies to an event whose resolved model is lowercase ("gpt-4o").
// Before the fix, resolveDiscount did a direct map[strings.ToLower(model)]
// lookup that missed mixed-case keys, so the discount silently fell back to
// the group default (or 0) and cost_usd stayed undiscounted.
func TestUsageFlusherAppliesPerModelDiscountCaseInsensitive(t *testing.T) {
	store := newTestPostgresStore(t, "flusher_discount_permodel")
	ctx := cancelableTestCtx(t)
	us := NewUsageStore(store)
	apiKeys := NewAPIKeyStore(store)
	groups := NewModelGroupStore(store)
	flusher := NewUsageFlusher(us, apiKeys, groups, FlusherConfig{QueueCap: 10, FlushInterval: time.Hour, FlushBatchSize: 5})
	if err := flusher.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(flusher.Stop)

	key, plaintextSecret, err := apiKeys.Create(ctx, "dk2", "", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Pricing: 1M input @ $1/M + 1M output @ $2/M = $3 pre-discount.
	if err := us.UpsertPricing(ctx, Pricing{ID: "gpt-4o", InputPer1M: 1.0, OutputPer1M: 2.0}); err != nil {
		t.Fatalf("UpsertPricing: %v", err)
	}
	// Per-model discount authored under a MIXED-CASE key; no group-level
	// default. The event's resolved model is lowercase "gpt-4o".
	grp, err := groups.Create(ctx, ModelGroup{
		Name: "permodel", AllowedModels: []string{"gpt-4o"},
		ModelDiscountPcts: map[string]float64{"GPT-4O": 25},
	})
	if err != nil {
		t.Fatalf("Create group: %v", err)
	}
	if err := apiKeys.UpdatePolicy(ctx, key.ID, Policy{ModelGroupID: &grp.ID}); err != nil {
		t.Fatalf("UpdatePolicy: %v", err)
	}

	flusher.HandleUsage(ctx, coreusage.Record{
		Provider: "test", Model: "gpt-4o", APIKey: plaintextSecret,
		AuthType: "api_key", Source: "test", RequestedAt: time.Now().UTC(),
		Detail: coreusage.Detail{
			InputTokens: 1_000_000, OutputTokens: 1_000_000, TotalTokens: 2_000_000,
		},
	})
	flusher.Stop()

	rows, _, err := us.SelectEvents(ctx, UsageFilter{APIKeyID: key.ID}, 1, 25)
	if err != nil {
		t.Fatalf("SelectEvents: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 event; got %d", len(rows))
	}
	row := rows[0]
	// Pre-discount = $3; 25% off → $2.25. Before the fix the mixed-case key
	// missed and cost stayed at $3 (undiscounted).
	if row.CostUSD < 2.24 || row.CostUSD > 2.26 {
		t.Errorf("cost_usd = %v; want ~2.25 (3.0 × 0.75) — per-model discount must apply case-insensitively", row.CostUSD)
	}
	if row.DiscountPct < 24.99 || row.DiscountPct > 25.01 {
		t.Errorf("discount_pct = %v; want 25", row.DiscountPct)
	}
	// original_cost_usd is stamped at flush (pre-discount = $3).
	if row.OriginalCostUSD < 2.99 || row.OriginalCostUSD > 3.01 {
		t.Errorf("original_cost_usd = %v; want ~3.0 (persisted at flush, pre-discount)", row.OriginalCostUSD)
	}
}

func TestUsageFlusherDropsOnFullQueue(t *testing.T) {
	// Use a zero-cap queue via direct construction — we cannot pass cap=0
	// because NewUsageFlusher falls back to defaults. Instead, set cap=1
	// and flood it faster than the (long) ticker.
	store := newTestPostgresStore(t, "flusher_test2")
	ctx := cancelableTestCtx(t)
	us := NewUsageStore(store)
	flusher := NewUsageFlusher(us, nil, nil, FlusherConfig{QueueCap: 1, FlushInterval: time.Hour, FlushBatchSize: 1})
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
	flusher := NewUsageFlusher(us, nil, nil, DefaultFlusherConfig())
	if err := flusher.Start(ctx); err != nil {
		t.Fatalf("Start (1): %v", err)
	}
	if err := flusher.Start(ctx); err == nil {
		t.Fatal("expected error on second Start; got nil")
	}
	t.Cleanup(flusher.Stop)
}
