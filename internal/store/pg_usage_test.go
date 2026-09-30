package store

import (
	"encoding/json"
	"strings"
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

// TestFillCostBreakdown guards that FillCostBreakdown populates CostBreakdown
// and AppliedPricing in lockstep on UsageEventRow. AppliedPricing exposes the
// resolved unit rates so the dashboard's event detail modal can render a
// tokens × rate → cost derivation; it must mirror the rates that produced the
// breakdown. A missing pricing row leaves AppliedPricing present-but-all-zero
// so the dashboard can show "no pricing configured" rather than reading as free.
func TestFillCostBreakdown(t *testing.T) {
	store := newTestPostgresStore(t, "usage_fill_breakdown")
	ctx := cancelableTestCtx(t)
	us := NewUsageStore(store)

	pricing := Pricing{ID: "fill-priced", InputPer1M: 5.0, OutputPer1M: 15.0, ReasoningPer1M: 10.0}
	if err := us.UpsertPricing(ctx, pricing); err != nil {
		t.Fatalf("UpsertPricing: %v", err)
	}
	row := UsageEventRow{
		Model:           "fill-priced",
		InputTokens:     1_000_000,
		OutputTokens:    500_000,
		ReasoningTokens: 100_000,
	}
	rows := []UsageEventRow{row}
	if err := us.FillCostBreakdown(ctx, rows); err != nil {
		t.Fatalf("FillCostBreakdown: %v", err)
	}
	// FillCostBreakdown mutates slice elements in place (see
	// TestFillCostBreakdownSliceCopyConvention) — read the mutated element back
	// from the slice, not the original stack variable.
	filled := rows[0]
	if filled.CostBreakdown == nil {
		t.Fatalf("expected CostBreakdown to be populated; got nil")
	}
	if !approxEqual(filled.CostBreakdown.Sum(), ComputeCost(pricing, filled.InputTokens, filled.OutputTokens, filled.ReasoningTokens, 0, 0)) {
		t.Errorf("breakdown sum = %v; want %v", filled.CostBreakdown.Sum(), ComputeCost(pricing, filled.InputTokens, filled.OutputTokens, filled.ReasoningTokens, 0, 0))
	}
	if filled.AppliedPricing == nil {
		t.Fatalf("expected AppliedPricing to be populated; got nil")
	}
	if filled.AppliedPricing.ID != pricing.ID {
		t.Errorf("AppliedPricing.ID = %q; want %q", filled.AppliedPricing.ID, pricing.ID)
	}
	if !approxEqual(filled.AppliedPricing.InputPer1M, pricing.InputPer1M) || !approxEqual(filled.AppliedPricing.OutputPer1M, pricing.OutputPer1M) {
		t.Errorf("AppliedPricing = %+v; want rates %+v", filled.AppliedPricing, pricing)
	}

	// A missing pricing row yields a present-but-all-zero AppliedPricing so the
	// dashboard can show "no pricing configured" (HasRates() == false).
	missing := UsageEventRow{Model: "fill-unpriced", InputTokens: 1_000_000}
	missingRows := []UsageEventRow{missing}
	if err := us.FillCostBreakdown(ctx, missingRows); err != nil {
		t.Fatalf("FillCostBreakdown (unpriced): %v", err)
	}
	missingFilled := missingRows[0]
	if missingFilled.AppliedPricing == nil {
		t.Fatalf("expected AppliedPricing to be present-but-zero for an unpriced model; got nil")
	}
	if missingFilled.AppliedPricing.HasRates() {
		t.Errorf("unpriced model AppliedPricing.HasRates() = true; want false: %+v", missingFilled.AppliedPricing)
	}

	// nil store must return nil (no panic).
	var nilStore *UsageStore
	if err := nilStore.FillCostBreakdown(ctx, []UsageEventRow{row}); err != nil {
		t.Fatalf("nil store FillCostBreakdown = %v; want nil", err)
	}
}

// TestFillCostBreakdownSliceCopyConvention guards the calling convention the
// single-row management handlers (GetUsageEvent / GetUsageError) must use when
// invoking FillCostBreakdown / FillCostBreakdownErrors.
//
// Those helpers mutate rows[i] in place via index assignment, so the mutation
// lands only on the slice's backing-array element — NOT on a local variable
// that was copied into a slice literal `[]T{localVar}`. Without reassigning
// `localVar = rows[0]` after the call, the value serialized to the API keeps
// nil CostBreakdown/AppliedPricing and the dashboard's "Cost breakdown"
// section silently never renders (it gates on `e.cost_breakdown` being truthy).
// The list endpoints are unaffected because they pass their own slice variable.
//
// This test pins the convention with a stand-in mutator that mirrors
// FillCostBreakdown's `r := &rows[i]; r.Field = ...` shape, so a future revert
// to `[]T{event}` (which is tempting because it reads like it should work) is
// caught here without needing a Postgres integration run. The store-level
// FillCostBreakdown integration test (TestFillCostBreakdown above) is skipped
// without PGSTORE_TEST_DSN, so without this guard the bug would regress
// silently in CI.
func TestFillCostBreakdownSliceCopyConvention(t *testing.T) {
	// mutator mirrors FillCostBreakdown's element-wise mutation pattern.
	mutator := func(rows []UsageEventRow) {
		for i := range rows {
			r := &rows[i]
			b := CostBreakdown{Input: 1.5}
			r.CostBreakdown = &b
			p := Pricing{ID: r.Model, InputPer1M: 5.0}
			r.AppliedPricing = &p
		}
	}

	// Anti-pattern (the bug): wrapping a local in a fresh slice literal copies
	// it; the mutation lands on the copy and the local stays nil.
	buggy := UsageEventRow{Model: "m", InputTokens: 1_000_000}
	mutator([]UsageEventRow{buggy})
	if buggy.CostBreakdown != nil || buggy.AppliedPricing != nil {
		t.Fatalf("anti-pattern premise wrong: local was populated (CostBreakdown=%v AppliedPricing=%v); "+
			"test premise needs revisiting", buggy.CostBreakdown, buggy.AppliedPricing)
	}

	// Required pattern: keep the row in a slice variable, mutate, then reassign.
	rows := []UsageEventRow{{Model: "m", InputTokens: 1_000_000}}
	mutator(rows)
	event := rows[0]
	if event.CostBreakdown == nil {
		t.Fatalf("expected CostBreakdown to be populated after reassign; got nil")
	}
	if event.AppliedPricing == nil {
		t.Fatalf("expected AppliedPricing to be populated after reassign; got nil")
	}
	if !approxEqual(event.CostBreakdown.Input, 1.5) {
		t.Errorf("CostBreakdown.Input = %v; want 1.5", event.CostBreakdown.Input)
	}
	if event.AppliedPricing.ID != "m" || !approxEqual(event.AppliedPricing.InputPer1M, 5.0) {
		t.Errorf("AppliedPricing = %+v; want {ID:m InputPer1M:5}", event.AppliedPricing)
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

// TestPricingHasRates guards the discriminator ResolvePricing uses to decide
// whether to fall back to the alias: a missing row and an explicitly all-zero
// row both surface as an all-zero Pricing (GetPricing returns zero on miss),
// so HasRates must report false for both and true for any single non-zero
// rate. Pure function — no DB.
func TestPricingHasRates(t *testing.T) {
	if (Pricing{}).HasRates() {
		t.Errorf("zero Pricing HasRates = true; want false")
	}
	if (Pricing{ID: "m"}).HasRates() {
		t.Errorf("missing-row Pricing HasRates = true; want false")
	}
	cases := []Pricing{
		{InputPer1M: 0.001},
		{OutputPer1M: 0.001},
		{CachedInputPer1M: 0.001},
		{CachedReadPer1M: 0.001},
		{ReasoningPer1M: 0.001},
	}
	for i, p := range cases {
		if !p.HasRates() {
			t.Errorf("case %d: HasRates = false; want true for %+v", i, p)
		}
	}
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

func TestUsageStoreSelectTopNullDimensionKey(t *testing.T) {
	// Regression: SelectTop projects a nullable dimension column (e.g.
	// e.api_key_id) directly into TopEntry.Key (a plain string). When the
	// underlying column is NULL — as happens when the flusher persists an
	// event with no resolvable api_keys row — the scan failed with
	// "converting NULL to string is unsupported". COALESCE(<dimCol>, '') must
	// surface it as an empty key instead.
	store := newTestPostgresStore(t, "usage_top_null_key")
	ctx := cancelableTestCtx(t)
	us := NewUsageStore(store)

	// APIKeyID intentionally empty -> persisted as NULL.
	event := UsageEvent{
		Provider:     "anthropic",
		Model:        "test-model",
		InputTokens:  42,
		OutputTokens: 7,
		RequestedAt:  now(),
	}
	if err := us.InsertEvent(ctx, event); err != nil {
		t.Fatalf("InsertEvent: %v", err)
	}

	// Grouping by api_key_id / "key" dimension resolves dimCol to e.api_key_id,
	// which is NULL here. "model" is exercised too to ensure the COALESCE
	// wrapper composes with the GROUP BY/ORDER BY clauses.
	for _, dim := range []string{"api_key_id", "key", "model"} {
		top, err := us.SelectTop(ctx, UsageFilter{}, dim, "request_count", 10)
		if err != nil {
			t.Fatalf("SelectTop(dim=%q): %v", dim, err)
		}
		if len(top) != 1 {
			t.Fatalf("SelectTop(dim=%q) returned %d rows; want 1", dim, len(top))
		}
		var wantKey string
		if dim == "model" {
			wantKey = "test-model"
		}
		if top[0].Key != wantKey {
			t.Errorf("SelectTop(dim=%q) Key = %q; want %q", dim, top[0].Key, wantKey)
		}
		if top[0].RequestCount != 1 {
			t.Errorf("SelectTop(dim=%q) RequestCount = %d; want 1", dim, top[0].RequestCount)
		}
	}
}

func TestUsageStoreImportLiteLLMSpendLogs(t *testing.T) {
	store := newTestPostgresStore(t, "usage_spend_log_import")
	ctx := cancelableTestCtx(t)
	us := NewUsageStore(store)

	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	events := []UsageEvent{
		{RequestID: "req-1", Provider: "litellm", Model: "gpt-4o", CostUSD: 0.25, TotalTokens: 100, RequestedAt: base},
		{RequestID: "req-2", Provider: "litellm", Model: "gpt-4o", CostUSD: 0.5, TotalTokens: 200, RequestedAt: base.Add(time.Minute)},
		{RequestID: "", Provider: "litellm", Model: "gpt-4o", CostUSD: 0.1}, // skipped: no request_id
	}
	imported, err := us.ImportLiteLLMSpendLogs(ctx, events)
	if err != nil {
		t.Fatalf("ImportLiteLLMSpendLogs: %v", err)
	}
	if imported != 2 {
		t.Fatalf("imported = %d; want 2 (request_id-less row skipped)", imported)
	}

	// Re-import the same request_ids: idempotent, no new rows.
	imported2, err := us.ImportLiteLLMSpendLogs(ctx, events)
	if err != nil {
		t.Fatalf("re-import: %v", err)
	}
	if imported2 != 0 {
		t.Fatalf("re-import imported = %d; want 0 (dedup by request_id)", imported2)
	}

	// Total cost across both events is 0.75.
	aggs, err := us.SelectAggregate(ctx, UsageFilter{})
	if err != nil {
		t.Fatalf("SelectAggregate: %v", err)
	}
	if len(aggs) != 1 || aggs[0].CostUSD != 0.75 {
		t.Fatalf("total cost = %+v; want one aggregate with 0.75", aggs)
	}
}

func TestUsageStoreImportLiteLLMSpendLogsMixedDupes(t *testing.T) {
	store := newTestPostgresStore(t, "usage_spend_log_mixed")
	ctx := cancelableTestCtx(t)
	us := NewUsageStore(store)

	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	events := []UsageEvent{
		{RequestID: "req-a", Provider: "litellm", Model: "gpt-4o", CostUSD: 1.0, RequestedAt: base},
		{RequestID: "req-a", Provider: "litellm", Model: "gpt-4o", CostUSD: 2.0, RequestedAt: base}, // duplicate in same batch
		{RequestID: "req-b", Provider: "litellm", Model: "gpt-4o", CostUSD: 3.0, RequestedAt: base},
	}
	imported, err := us.ImportLiteLLMSpendLogs(ctx, events)
	if err != nil {
		t.Fatalf("ImportLiteLLMSpendLogs: %v", err)
	}
	// req-a inserted once (first wins via DO NOTHING), req-b inserted once.
	if imported != 2 {
		t.Fatalf("imported = %d; want 2 (in-batch dupes collapsed)", imported)
	}
	aggs, _ := us.SelectAggregate(ctx, UsageFilter{})
	if len(aggs) != 1 || aggs[0].CostUSD != 4.0 {
		t.Fatalf("total cost = %+v; want one aggregate with 4.0 (1.0 + 3.0)", aggs)
	}
}

func TestUsageStoreImportLiteLLMErrors(t *testing.T) {
	store := newTestPostgresStore(t, "usage_errors_import")
	ctx := cancelableTestCtx(t)
	us := NewUsageStore(store)

	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	errs := []UsageError{
		{RequestID: "err-1", Provider: "litellm", Model: "gpt-4o", FailStatusCode: 500, ErrorMessage: "upstream timeout", LatencyMs: 999, RequestedAt: base},
		{RequestID: "err-2", Provider: "litellm", Model: "gpt-4o", FailStatusCode: 429, ErrorMessage: "rate limited", RequestedAt: base.Add(time.Minute)},
		{RequestID: "", Provider: "litellm", Model: "gpt-4o", FailStatusCode: 500, ErrorMessage: "no id"}, // skipped
	}
	imported, err := us.ImportLiteLLMErrors(ctx, errs)
	if err != nil {
		t.Fatalf("ImportLiteLLMErrors: %v", err)
	}
	if imported != 2 {
		t.Fatalf("imported = %d; want 2 (request_id-less row skipped)", imported)
	}

	// Re-import the same request_ids: idempotent, no new rows.
	imported2, err := us.ImportLiteLLMErrors(ctx, errs)
	if err != nil {
		t.Fatalf("re-import: %v", err)
	}
	if imported2 != 0 {
		t.Fatalf("re-import imported = %d; want 0 (dedup by request_id)", imported2)
	}

	// Verify persisted rows carry the error message + status + latency.
	rows, total, err := us.SelectErrors(ctx, UsageFilter{}, 1, 200)
	if err != nil {
		t.Fatalf("SelectErrors: %v", err)
	}
	if total != 2 || len(rows) != 2 {
		t.Fatalf("total = %d, len = %d; want 2/2", total, len(rows))
	}
	byReq := map[string]UsageErrorRow{}
	for _, r := range rows {
		byReq[r.RequestID] = r
	}
	if r := byReq["err-1"]; r.ErrorMessage != "upstream timeout" || r.FailStatusCode != 500 || r.LatencyMs != 999 {
		t.Fatalf("err-1 persisted wrong: %+v", r)
	}
	if r := byReq["err-2"]; r.ErrorMessage != "rate limited" || r.FailStatusCode != 429 {
		t.Fatalf("err-2 persisted wrong: %+v", r)
	}
}

func TestUsageStoreImportLiteLLMErrorsInBatchDupes(t *testing.T) {
	store := newTestPostgresStore(t, "usage_errors_import_dupes")
	ctx := cancelableTestCtx(t)
	us := NewUsageStore(store)

	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	errs := []UsageError{
		{RequestID: "err-a", Provider: "litellm", Model: "gpt-4o", FailStatusCode: 500, ErrorMessage: "first", RequestedAt: base},
		{RequestID: "err-a", Provider: "litellm", Model: "gpt-4o", FailStatusCode: 500, ErrorMessage: "second", RequestedAt: base}, // dup in batch
		{RequestID: "err-b", Provider: "litellm", Model: "gpt-4o", FailStatusCode: 400, ErrorMessage: "bad", RequestedAt: base},
	}
	imported, err := us.ImportLiteLLMErrors(ctx, errs)
	if err != nil {
		t.Fatalf("ImportLiteLLMErrors: %v", err)
	}
	if imported != 2 {
		t.Fatalf("imported = %d; want 2 (in-batch dupes collapsed)", imported)
	}
}

// TestInsertEventPersistsEnergyAndProviderMetadata guards the Neuralwatt
// billing columns: InsertEvent must round-trip EnergyJoules (NULL when
// unmeasured) and ProviderMetadata (empty jsonb object when absent). The
// read paths project neither column yet, so the assertions query the raw
// columns directly.
func TestInsertEventPersistsEnergyAndProviderMetadata(t *testing.T) {
	store := newTestPostgresStore(t, "usage_energy_metadata")
	ctx := cancelableTestCtx(t)
	us := NewUsageStore(store)

	joules := 42.5
	withMeta := UsageEvent{
		RequestID:    "neu-with-meta",
		Provider:     "neuralwatt",
		Model:        "neuralwatt-deepseek-v4-pro",
		InputTokens:  10,
		OutputTokens: 5,
		TotalTokens:  15,
		EnergyJoules: &joules,
		ProviderMetadata: map[string]any{
			"neuralwatt": map[string]any{"request_cost_usd": 0.0034},
		},
		RequestedAt: now(),
	}
	if err := us.InsertEvent(ctx, withMeta); err != nil {
		t.Fatalf("InsertEvent (with metadata): %v", err)
	}
	withoutMeta := UsageEvent{
		RequestID:   "neu-without-meta",
		Provider:    "neuralwatt",
		Model:       "neuralwatt-deepseek-v4-pro",
		InputTokens: 1,
		RequestedAt: now(),
	}
	if err := us.InsertEvent(ctx, withoutMeta); err != nil {
		t.Fatalf("InsertEvent (without metadata): %v", err)
	}

	var energy *float64
	var metadata []byte
	if err := store.DB().QueryRowContext(ctx,
		`SELECT energy_joules, provider_metadata FROM `+store.UsageEventsTable()+` WHERE request_id = $1`,
		"neu-with-meta",
	).Scan(&energy, &metadata); err != nil {
		t.Fatalf("select with-meta row: %v", err)
	}
	if energy == nil || !approxEqual(*energy, 42.5) {
		t.Fatalf("energy_joules = %v; want 42.5", energy)
	}
	var parsed map[string]any
	if err := json.Unmarshal(metadata, &parsed); err != nil {
		t.Fatalf("provider_metadata not valid JSON: %v (%s)", err, metadata)
	}
	nw, ok := parsed["neuralwatt"].(map[string]any)
	if !ok {
		t.Fatalf("provider_metadata missing neuralwatt object: %s", metadata)
	}
	if cost, ok := nw["request_cost_usd"].(float64); !ok || !approxEqual(cost, 0.0034) {
		t.Fatalf("provider_metadata.neuralwatt.request_cost_usd = %v; want 0.0034 (%s)", nw["request_cost_usd"], metadata)
	}

	if err := store.DB().QueryRowContext(ctx,
		`SELECT energy_joules, provider_metadata FROM `+store.UsageEventsTable()+` WHERE request_id = $1`,
		"neu-without-meta",
	).Scan(&energy, &metadata); err != nil {
		t.Fatalf("select without-meta row: %v", err)
	}
	if energy != nil {
		t.Fatalf("energy_joules = %v; want NULL for unmeasured event", *energy)
	}
	if strings.TrimSpace(string(metadata)) != "{}" {
		t.Fatalf("provider_metadata = %s; want {} for event without metadata", metadata)
	}
}
