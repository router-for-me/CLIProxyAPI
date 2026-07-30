package store

import (
	"testing"
	"time"
)

func now() time.Time { return time.Now().UTC() }

func TestComputeCostZeroPricing(t *testing.T) {
	if cost := ComputeCost(Pricing{}, 1000, 500, 100, 50, 25); cost != 0 {
		t.Fatalf("expected 0 cost for zero pricing; got %v", cost)
	}
}

func TestComputeCostSimple(t *testing.T) {
	p := Pricing{InputPer1M: 5.0, OutputPer1M: 15.0, ReasoningPer1M: 10.0, CachedInputPer1M: 1.0, CachedReadPer1M: 0.5}
	// 1M*5 (input) + 1M*15 (output) + 1M*10 (reasoning) + 1M*1 (cached input) + 1M*0.5 (cached read) = 31.5
	cost := ComputeCost(p, 1_000_000, 1_000_000, 1_000_000, 1_000_000, 1_000_000)
	const expect = 31.5
	if !approxEqual(cost, expect) {
		t.Fatalf("cost = %v; want %v", cost, expect)
	}
}

func TestComputeCostZeroTokens(t *testing.T) {
	p := Pricing{InputPer1M: 5.0}
	if cost := ComputeCost(p, 0, 0, 0, 0, 0); cost != 0 {
		t.Fatalf("cost = %v; want 0", cost)
	}
}

func TestComputeCostNegativeTokensTreatedAsZero(t *testing.T) {
	p := Pricing{InputPer1M: 5.0}
	if cost := ComputeCost(p, -100, 0, 0, 0, 0); cost != 0 {
		t.Fatalf("cost with negative tokens = %v; want 0", cost)
	}
}

func TestComputeCostCacheReadSeparateFromCachedInput(t *testing.T) {
	// Cached-input (cache-creation / write) and cached-read are billed at
	// different unit prices; both should contribute independently.
	p := Pricing{CachedInputPer1M: 1.0, CachedReadPer1M: 0.1}
	cost := ComputeCost(p, 0, 0, 0, 1_000_000, 1_000_000)
	// 1 + 0.1 = 1.1
	if !approxEqual(cost, 1.1) {
		t.Fatalf("cost = %v; want 1.1", cost)
	}
}

// TestSegmentCostsSumEqualsComputeCost guards the dashboard-breakdown contract:
// the per-segment dollar attribution (SegmentCosts, surfaced via
// FillCostBreakdown) must equal the persisted cost_usd (ComputeCost). With
// normalized parser output (cached_tokens strictly cache-read, cache_creation
// distinct), both functions consume the same token counts and must agree to
// within floating-point tolerance.
func TestSegmentCostsSumEqualsComputeCost(t *testing.T) {
	p := Pricing{InputPer1M: 5.0, OutputPer1M: 15.0, ReasoningPer1M: 10.0, CachedInputPer1M: 1.25, CachedReadPer1M: 0.5}
	// Anthropic-style event: input excludes cache; cache_read and
	// cache_creation are independent counters.
	const input, output, reasoning, cacheRead, cacheCreation = 600, 200, 50, 400, 300
	cost := ComputeCost(p, input, output, reasoning, cacheCreation, cacheRead)
	breakdown := SegmentCosts(p, input, output, reasoning, cacheRead, cacheCreation)
	// SegmentCosts argument order: input, output, reasoning, cachedRead, cacheCreation.
	// ComputeCost argument order:      input, output, reasoning, cached (creation), cacheRead.
	if !approxEqual(cost, breakdown.Sum()) {
		t.Fatalf("segment sum %v != ComputeCost %v", breakdown.Sum(), cost)
	}
	// Sanity: cache-read billed at the discount rate, cache-creation at the
	// write surcharge rate — they must NOT cancel out / mirror each other.
	if !approxEqual(breakdown.CachedRead, 0.5*400/1_000_000) {
		t.Fatalf("cached_read segment = %v, want %v", breakdown.CachedRead, 0.5*400/1_000_000)
	}
	if !approxEqual(breakdown.CacheCreation, 1.25*300/1_000_000) {
		t.Fatalf("cache_creation segment = %v, want %v", breakdown.CacheCreation, 1.25*300/1_000_000)
	}
}

func TestAggregateGroupClause(t *testing.T) {
	cases := []struct {
		groupBy   string
		wantError bool
	}{
		{"", false},
		{"total", false},
		{"api_key_id", false},
		{"apikey", false},
		{"key", false},
		{"model", false},
		{"provider", false},
		{"day", false},
		{"hour", false},
		{"unknown", true},
	}
	for _, tc := range cases {
		_, _, err := aggregateGroupClause(tc.groupBy)
		if tc.wantError && err == nil {
			t.Errorf("groupBy %q: expected error, got nil", tc.groupBy)
		}
		if !tc.wantError && err != nil {
			t.Errorf("groupBy %q: unexpected error: %v", tc.groupBy, err)
		}
	}
}

func TestItoaSimple(t *testing.T) {
	cases := map[int]string{0: "0", 1: "1", 9: "9", 10: "10", 123: "123", 1234: "1234"}
	for in, want := range cases {
		if got := itoa(in); got != want {
			t.Errorf("itoa(%d) = %q; want %q", in, got, want)
		}
	}
}

func approxEqual(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 1e-6
}

func TestUsageStoreIntegrationSmoke(t *testing.T) {
	store := newTestPostgresStore(t, "usage_test")
	ctx := cancelableTestCtx(t)
	us := NewUsageStore(store)

	// Insert pricing for model "test-model" and verify lookup.
	pricing := Pricing{ID: "test-model", InputPer1M: 5.0, OutputPer1M: 15.0, ReasoningPer1M: 10.0}
	if err := us.UpsertPricing(ctx, pricing); err != nil {
		t.Fatalf("UpsertPricing: %v", err)
	}
	got, err := us.GetPricing(ctx, "test-model")
	if err != nil {
		t.Fatalf("GetPricing: %v", err)
	}
	if got.InputPer1M != pricing.InputPer1M {
		t.Errorf("InputPer1M = %v; want %v", got.InputPer1M, pricing.InputPer1M)
	}

	// Insert a usage event and verify cost computation.
	apiKeys := NewAPIKeyStore(store)
	key, secret, err := apiKeys.Create(ctx, "uk", "", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("Create api key: %v", err)
	}
	event := UsageEvent{
		APIKeyID:        key.ID,
		Provider:        "anthropic",
		Model:           "test-model",
		InputTokens:     1_000_000,
		OutputTokens:    1_000_000,
		ReasoningTokens: 1_000_000,
		RequestedAt:     now(),
	}
	event.CostUSD = ComputeCost(got, event.InputTokens, event.OutputTokens, event.ReasoningTokens, event.CachedTokens, 0)
	if err := us.InsertEvent(ctx, event); err != nil {
		t.Fatalf("InsertEvent: %v", err)
	}

	// Aggregate should see one request.
	aggs, err := us.SelectAggregate(ctx, UsageFilter{APIKeyID: key.ID, GroupBy: "model"})
	if err != nil {
		t.Fatalf("SelectAggregate: %v", err)
	}
	if len(aggs) != 1 || aggs[0].RequestCount != 1 {
		t.Fatalf("aggregate = %+v; want 1 row with count 1", aggs)
	}
	if !approxEqual(aggs[0].CostUSD, 30.0) {
		t.Fatalf("cost = %v; want 30.0", aggs[0].CostUSD)
	}

	// Upsert window.
	start := now().Truncate(time.Hour)
	end := start.Add(time.Hour)
	if err := us.UpsertWindow(ctx, key.ID, WindowTypeHourly, start, end, 1, event.TotalTokens, event.CostUSD); err != nil {
		t.Fatalf("UpsertWindow: %v", err)
	}
	w, err := us.GetWindow(ctx, key.ID, WindowTypeHourly, start)
	if err != nil {
		t.Fatalf("GetWindow: %v", err)
	}
	if w.RequestCount != 1 {
		t.Fatalf("window request count = %d; want 1", w.RequestCount)
	}
	// Idempotent increment.
	if err := us.UpsertWindow(ctx, key.ID, WindowTypeHourly, start, end, 1, 0, 0); err != nil {
		t.Fatalf("UpsertWindow (2): %v", err)
	}
	w, _ = us.GetWindow(ctx, key.ID, WindowTypeHourly, start)
	if w.RequestCount != 2 {
		t.Fatalf("window request count = %d; want 2", w.RequestCount)
	}

	_ = secret // not used here
}

func TestUsageStoreSelectEventsNullAPIKeyID(t *testing.T) {
	// Regression: the flusher records only api_key_principal when no
	// api_keys row can be resolved, persisting NULL in api_key_id. The
	// event/errors SELECT path projects that column into a plain string,
	// so a NULL previously failed the scan ("converting NULL to string is
	// unsupported"). COALESCE(e.api_key_id, '') must surface it as "".
	store := newTestPostgresStore(t, "usage_null_apikey")
	ctx := cancelableTestCtx(t)
	us := NewUsageStore(store)

	event := UsageEvent{
		// APIKeyID intentionally empty -> persisted as NULL.
		Provider:     "anthropic",
		Model:        "test-model",
		InputTokens:  42,
		OutputTokens: 7,
		RequestedAt:  now(),
	}
	if err := us.InsertEvent(ctx, event); err != nil {
		t.Fatalf("InsertEvent: %v", err)
	}

	rows, _, err := us.SelectEvents(ctx, UsageFilter{}, 1, 25)
	if err != nil {
		t.Fatalf("SelectEvents: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("SelectEvents returned %d rows; want 1", len(rows))
	}
	if rows[0].APIKeyID != "" {
		t.Errorf("APIKeyID = %q; want empty for NULL api_key_id", rows[0].APIKeyID)
	}
	if rows[0].Model != "test-model" {
		t.Errorf("Model = %q; want test-model", rows[0].Model)
	}
	if rows[0].InputTokens != 42 {
		t.Errorf("InputTokens = %d; want 42", rows[0].InputTokens)
	}

	got, err := us.GetEvent(ctx, rows[0].ID)
	if err != nil {
		t.Fatalf("GetEvent: %v", err)
	}
	if got.APIKeyID != "" {
		t.Errorf("GetEvent APIKeyID = %q; want empty for NULL api_key_id", got.APIKeyID)
	}
}

func TestUsageStoreBatchInsert(t *testing.T) {
	store := newTestPostgresStore(t, "usage_test2")
	ctx := cancelableTestCtx(t)
	us := NewUsageStore(store)
	apiKeys := NewAPIKeyStore(store)
	key, _, err := apiKeys.Create(ctx, "bk", "", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	events := make([]UsageEvent, 5)
	for i := range events {
		events[i] = UsageEvent{
			APIKeyID:    key.ID,
			Provider:    "test",
			Model:       "m",
			InputTokens: int64(i + 1),
			RequestedAt: now(),
		}
	}
	if err := us.BatchInsertEvents(ctx, events); err != nil {
		t.Fatalf("BatchInsertEvents: %v", err)
	}
	aggs, err := us.SelectAggregate(ctx, UsageFilter{APIKeyID: key.ID, GroupBy: "api_key_id"})
	if err != nil {
		t.Fatalf("SelectAggregate: %v", err)
	}
	if len(aggs) != 1 || aggs[0].RequestCount != 5 {
		t.Fatalf("aggregate = %+v; want 5", aggs)
	}
}
