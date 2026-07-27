package store

import (
	"errors"
	"testing"
	"time"
)

func TestUsageErrorInsertSelectGet(t *testing.T) {
	store := newTestPostgresStore(t, "usage_errors_test")
	ctx := cancelableTestCtx(t)
	us := NewUsageStore(store)

	apiKeys := NewAPIKeyStore(store)
	key, _, err := apiKeys.Create(ctx, "errk", "", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("Create api key: %v", err)
	}

	err1 := UsageError{
		RequestID:      "req-err-1",
		APIKeyID:       key.ID,
		Provider:       "anthropic",
		Model:          "claude-error",
		FailStatusCode: 429,
		ErrorMessage:   "rate limited: too many requests",
		RequestedAt:    now(),
	}
	if err := us.InsertError(ctx, err1); err != nil {
		t.Fatalf("InsertError: %v", err)
	}

	// SelectErrors should surface the row with the resolved key_alias (or key
	// name) instead of the sealed api_key_principal.
	filter := UsageFilter{APIKeyID: key.ID}
	rows, total, err := us.SelectErrors(ctx, filter, 1, 25)
	if err != nil {
		t.Fatalf("SelectErrors: %v", err)
	}
	if total != 1 || len(rows) != 1 {
		t.Fatalf("got total=%d rows=%d; want 1/1", total, len(rows))
	}
	r := rows[0]
	if r.RequestID != err1.RequestID {
		t.Errorf("RequestID = %q; want %q", r.RequestID, err1.RequestID)
	}
	if r.FailStatusCode != 429 {
		t.Errorf("FailStatusCode = %d; want 429", r.FailStatusCode)
	}
	if r.ErrorMessage != err1.ErrorMessage {
		t.Errorf("ErrorMessage = %q; want %q", r.ErrorMessage, err1.ErrorMessage)
	}
	if r.Provider != err1.Provider || r.Model != err1.Model {
		t.Errorf("Provider/Model = %q/%q; want %q/%q", r.Provider, r.Model, err1.Provider, err1.Model)
	}

	// GetError by id should return the same shape.
	got, err := us.GetError(ctx, r.ID)
	if err != nil {
		t.Fatalf("GetError: %v", err)
	}
	if got.ErrorMessage != err1.ErrorMessage || got.RequestID != err1.RequestID {
		t.Fatalf("GetError row mismatch: %+v", got)
	}

	// GetError on a missing id should surface ErrUsageErrorNotFound.
	if _, err := us.GetError(ctx, 1<<31); !errors.Is(err, ErrUsageErrorNotFound) {
		t.Fatalf("GetError(missing) = %v; want ErrUsageErrorNotFound", err)
	}
}

func TestUsageErrorBatchInsert(t *testing.T) {
	store := newTestPostgresStore(t, "usage_errors_batch")
	ctx := cancelableTestCtx(t)
	us := NewUsageStore(store)

	apiKeys := NewAPIKeyStore(store)
	key, _, err := apiKeys.Create(ctx, "bkerr", "", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("Create api key: %v", err)
	}
	errs := make([]UsageError, 3)
	for i := range errs {
		errs[i] = UsageError{
			APIKeyID:       key.ID,
			Provider:       "openai",
			Model:          "gpt-error",
			FailStatusCode: 500 + i,
			ErrorMessage:   "internal error",
			RequestedAt:    now(),
		}
	}
	if err := us.BatchInsertErrors(ctx, errs); err != nil {
		t.Fatalf("BatchInsertErrors: %v", err)
	}
	count, err := us.SelectErrorCount(ctx, UsageFilter{APIKeyID: key.ID})
	if err != nil {
		t.Fatalf("SelectErrorCount: %v", err)
	}
	if count != int64(len(errs)) {
		t.Fatalf("count = %d; want %d", count, len(errs))
	}
}

// TestUsageErrorCountIsolationFromEvents guards the core architectural
// invariant: failed attempts live in usage_errors (counted toward
// failed_count / failure_rate), while usage_events holds successful responses
// only (counted toward request_count). The two tables must not cross-contaminate.
func TestUsageErrorCountIsolationFromEvents(t *testing.T) {
	store := newTestPostgresStore(t, "usage_errors_iso")
	ctx := cancelableTestCtx(t)
	us := NewUsageStore(store)

	apiKeys := NewAPIKeyStore(store)
	key, _, err := apiKeys.Create(ctx, "iso", "", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("Create api key: %v", err)
	}

	// Two successful events, one failed attempt, all under the same key/model.
	for i := 0; i < 2; i++ {
		if err := us.InsertEvent(ctx, UsageEvent{
			APIKeyID:    key.ID,
			Provider:    "anthropic",
			Model:       "iso-model",
			InputTokens: 100,
			RequestedAt: now(),
		}); err != nil {
			t.Fatalf("InsertEvent: %v", err)
		}
	}
	if err := us.InsertError(ctx, UsageError{
		APIKeyID:       key.ID,
		Provider:       "anthropic",
		Model:          "iso-model",
		FailStatusCode: 503,
		ErrorMessage:   "upstream timeout",
		RequestedAt:    now(),
	}); err != nil {
		t.Fatalf("InsertError: %v", err)
	}

	filter := UsageFilter{APIKeyID: key.ID}

	// Totals on usage_events must count only successes (2) and report zero
	// failed_count (the success table no longer carries failures).
	totals, err := us.SelectTotals(ctx, filter)
	if err != nil {
		t.Fatalf("SelectTotals: %v", err)
	}
	if totals.RequestCount != 2 {
		t.Errorf("RequestCount = %d; want 2 (successes only)", totals.RequestCount)
	}
	if totals.FailedCount != 0 {
		t.Errorf("FailedCount from usage_events = %d; want 0 (table holds no failures)", totals.FailedCount)
	}

	// SelectErrorCount on usage_errors must report exactly 1 failure.
	failed, err := us.SelectErrorCount(ctx, filter)
	if err != nil {
		t.Fatalf("SelectErrorCount: %v", err)
	}
	if failed != 1 {
		t.Errorf("SelectErrorCount = %d; want 1", failed)
	}

	// SelectErrors must not surface the success rows.
	errRows, errTotal, err := us.SelectErrors(ctx, filter, 1, 25)
	if err != nil {
		t.Fatalf("SelectErrors: %v", err)
	}
	if errTotal != 1 || len(errRows) != 1 {
		t.Fatalf("SelectErrors total=%d rows=%d; want 1/1", errTotal, len(errRows))
	}
	if errRows[0].FailStatusCode != 503 {
		t.Errorf("error row status = %d; want 503", errRows[0].FailStatusCode)
	}

	// SelectEvents must not surface the failed row.
	evRows, evTotal, err := us.SelectEvents(ctx, filter, 1, 25)
	if err != nil {
		t.Fatalf("SelectEvents: %v", err)
	}
	if evTotal != 2 || len(evRows) != 2 {
		t.Fatalf("SelectEvents total=%d rows=%d; want 2/2", evTotal, len(evRows))
	}
	for _, e := range evRows {
		if e.Failed {
			t.Errorf("events row marked failed; usage_events should hold successes only: %+v", e)
		}
	}
}

func TestSelectErrorAggregateGrouping(t *testing.T) {
	store := newTestPostgresStore(t, "usage_errors_agg")
	ctx := cancelableTestCtx(t)
	us := NewUsageStore(store)

	apiKeys := NewAPIKeyStore(store)
	key, _, err := apiKeys.Create(ctx, "agg", "", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("Create api key: %v", err)
	}

	// 3 errors for model-a, 1 for model-b.
	for i := 0; i < 3; i++ {
		if err := us.InsertError(ctx, UsageError{
			APIKeyID:    key.ID,
			Provider:    "openai",
			Model:       "model-a",
			RequestedAt: now(),
		}); err != nil {
			t.Fatalf("InsertError: %v", err)
		}
	}
	if err := us.InsertError(ctx, UsageError{
		APIKeyID:    key.ID,
		Provider:    "openai",
		Model:       "model-b",
		RequestedAt: now(),
	}); err != nil {
		t.Fatalf("InsertError b: %v", err)
	}

	aggs, err := us.SelectErrorAggregate(ctx, UsageFilter{APIKeyID: key.ID, GroupBy: "model"})
	if err != nil {
		t.Fatalf("SelectErrorAggregate: %v", err)
	}
	if len(aggs) != 2 {
		t.Fatalf("got %d agg rows; want 2", len(aggs))
	}
	// Find the model-a row and assert FailedCount == 3.
	for _, a := range aggs {
		if a.Bucket == "model-a" {
			if a.FailedCount != 3 {
				t.Errorf("model-a FailedCount = %d; want 3", a.FailedCount)
			}
		}
	}

	// Total aggregate (no GroupBy) should return FailedCount == 4.
	totals, err := us.SelectErrorAggregate(ctx, UsageFilter{APIKeyID: key.ID})
	if err != nil {
		t.Fatalf("SelectErrorAggregate totals: %v", err)
	}
	if len(totals) != 1 || totals[0].FailedCount != 4 {
		t.Fatalf("totals = %+v; want 1 row FailedCount=4", totals)
	}

	// Time-series should still resolve buckets without error on an errors-only table.
	ts, err := us.SelectErrorTimeSeries(ctx, UsageFilter{APIKeyID: key.ID}, "day")
	if err != nil {
		t.Fatalf("SelectErrorTimeSeries: %v", err)
	}
	// At least one bucket with a non-zero failure count.
	var anyNonZero bool
	for _, p := range ts {
		if p.FailedCount > 0 {
			anyNonZero = true
			break
		}
	}
	if !anyNonZero {
		t.Errorf("timeseries all-zero; expected at least one bucket with failures: %+v", ts)
	}

	// Top-N by error count should surface model-a first (3 failures).
	top, err := us.SelectErrorTop(ctx, UsageFilter{APIKeyID: key.ID}, "model", 10)
	if err != nil {
		t.Fatalf("SelectErrorTop: %v", err)
	}
	if len(top) != 2 || top[0].Key != "model-a" || top[0].FailedCount != 3 {
		t.Fatalf("top = %+v; want model-a first with FailedCount=3", top)
	}
}

func TestFillCostBreakdownErrors(t *testing.T) {
	store := newTestPostgresStore(t, "usage_errors_breakdown")
	ctx := cancelableTestCtx(t)
	us := NewUsageStore(store)

	pricing := Pricing{ID: "err-priced", InputPer1M: 5.0, OutputPer1M: 15.0}
	if err := us.UpsertPricing(ctx, pricing); err != nil {
		t.Fatalf("UpsertPricing: %v", err)
	}
	row := UsageErrorRow{
		Model:        "err-priced",
		InputTokens:  1_000_000,
		OutputTokens: 1_000_000,
	}
	if err := us.FillCostBreakdownErrors(ctx, []UsageErrorRow{row}); err != nil {
		t.Fatalf("FillCostBreakdownErrors: %v", err)
	}
	if row.CostBreakdown == nil {
		t.Fatalf("expected CostBreakdown to be populated; got nil")
	}
	if !approxEqual(row.CostBreakdown.Sum(), ComputeCost(pricing, 1_000_000, 1_000_000, 0, 0, 0)) {
		t.Errorf("breakdown sum = %v; want %v", row.CostBreakdown.Sum(), ComputeCost(pricing, 1_000_000, 1_000_000, 0, 0, 0))
	}

	// nil store must return nil (no panic).
	var nilStore *UsageStore
	if err := nilStore.FillCostBreakdownErrors(ctx, []UsageErrorRow{row}); err != nil {
		t.Fatalf("nil store FillCostBreakdownErrors = %v; want nil", err)
	}
}

// TestUsageErrorTimeFilter guards that the requested_at WHERE bound is honored
// against usage_errors just like it is against usage_events.
func TestUsageErrorTimeFilter(t *testing.T) {
	store := newTestPostgresStore(t, "usage_errors_time")
	ctx := cancelableTestCtx(t)
	us := NewUsageStore(store)

	apiKeys := NewAPIKeyStore(store)
	key, _, err := apiKeys.Create(ctx, "tf", "", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("Create api key: %v", err)
	}

	past := time.Now().UTC().Add(-2 * time.Hour)
	future := now()
	// One old error, one recent error.
	for _, when := range []time.Time{past, future} {
		if err := us.InsertError(ctx, UsageError{
			APIKeyID:    key.ID,
			Provider:    "openai",
			Model:       "tfe",
			RequestedAt: when,
		}); err != nil {
			t.Fatalf("InsertError: %v", err)
		}
	}

	// Window covering only the recent row.
	cutoff := time.Now().UTC().Add(-1 * time.Hour)
	count, err := us.SelectErrorCount(ctx, UsageFilter{APIKeyID: key.ID, From: cutoff})
	if err != nil {
		t.Fatalf("SelectErrorCount: %v", err)
	}
	if count != 1 {
		t.Errorf("count in [cutoff, now] = %d; want 1", count)
	}

	// Full window sees both.
	countAll, err := us.SelectErrorCount(ctx, UsageFilter{APIKeyID: key.ID})
	if err != nil {
		t.Fatalf("SelectErrorCount all: %v", err)
	}
	if countAll != 2 {
		t.Errorf("count all = %d; want 2", countAll)
	}
}

// TestUsageRequestIDFilter guards that the RequestID filter narrows both
// usage_events and usage_errors on the per-request correlation identifier, so
// the dashboard's Request ID search box can pinpoint a single row across both
// tables.
func TestUsageRequestIDFilter(t *testing.T) {
	store := newTestPostgresStore(t, "usage_request_id")
	ctx := cancelableTestCtx(t)
	us := NewUsageStore(store)

	apiKeys := NewAPIKeyStore(store)
	key, _, err := apiKeys.Create(ctx, "rid", "", "", nil, nil, nil)
	if err != nil {
		t.Fatalf("Create api key: %v", err)
	}

	// Two events with distinct request ids, one without.
	evtMatch := UsageEvent{
		APIKeyID:    key.ID,
		Provider:    "anthropic",
		Model:       "rid-model",
		RequestID:   "req-AAAA",
		RequestedAt: now(),
	}
	evtOther := UsageEvent{
		APIKeyID:    key.ID,
		Provider:    "anthropic",
		Model:       "rid-model",
		RequestID:   "req-BBBB",
		RequestedAt: now(),
	}
	evtNone := UsageEvent{
		APIKeyID:    key.ID,
		Provider:    "anthropic",
		Model:       "rid-model",
		RequestedAt: now(),
	}
	for _, ev := range []UsageEvent{evtMatch, evtOther, evtNone} {
		if err := us.InsertEvent(ctx, ev); err != nil {
			t.Fatalf("InsertEvent: %v", err)
		}
	}

	// Two errors with distinct request ids.
	errMatch := UsageError{
		APIKeyID:       key.ID,
		Provider:       "anthropic",
		Model:          "rid-model",
		RequestID:      "req-AAAA",
		FailStatusCode: 429,
		ErrorMessage:   "rate limited",
		RequestedAt:    now(),
	}
	errOther := UsageError{
		APIKeyID:       key.ID,
		Provider:       "anthropic",
		Model:          "rid-model",
		RequestID:      "req-BBBB",
		FailStatusCode: 500,
		ErrorMessage:   "internal error",
		RequestedAt:    now(),
	}
	for _, er := range []UsageError{errMatch, errOther} {
		if err := us.InsertError(ctx, er); err != nil {
			t.Fatalf("InsertError: %v", err)
		}
	}

	// Filter events by the matching request id: exactly one row.
	rows, total, err := us.SelectEvents(ctx, UsageFilter{APIKeyID: key.ID, RequestID: "req-AAAA"}, 1, 25)
	if err != nil {
		t.Fatalf("SelectEvents: %v", err)
	}
	if total != 1 || len(rows) != 1 || rows[0].RequestID != "req-AAAA" {
		t.Fatalf("events filter = total=%d rows=%d; want exactly 1 matching req-AAAA: %+v", total, len(rows), rows)
	}

	// Filter errors by the matching request id: exactly one row.
	errRows, errTotal, err := us.SelectErrors(ctx, UsageFilter{APIKeyID: key.ID, RequestID: "req-AAAA"}, 1, 25)
	if err != nil {
		t.Fatalf("SelectErrors: %v", err)
	}
	if errTotal != 1 || len(errRows) != 1 || errRows[0].RequestID != "req-AAAA" {
		t.Fatalf("errors filter = total=%d rows=%d; want exactly 1 matching req-AAAA: %+v", errTotal, len(errRows), errRows)
	}

	// No-filter baseline sees every row.
	allEvt, _, err := us.SelectEvents(ctx, UsageFilter{APIKeyID: key.ID}, 1, 25)
	if err != nil {
		t.Fatalf("SelectEvents all: %v", err)
	}
	if len(allEvt) != 3 {
		t.Errorf("events all = %d; want 3", len(allEvt))
	}
	allErr, _, err := us.SelectErrors(ctx, UsageFilter{APIKeyID: key.ID}, 1, 25)
	if err != nil {
		t.Fatalf("SelectErrors all: %v", err)
	}
	if len(allErr) != 2 {
		t.Errorf("errors all = %d; want 2", len(allErr))
	}

	// A request id that no row carries returns zero.
	noneRows, noneTotal, err := us.SelectEvents(ctx, UsageFilter{APIKeyID: key.ID, RequestID: "req-ZZZZ"}, 1, 25)
	if err != nil {
		t.Fatalf("SelectEvents miss: %v", err)
	}
	if noneTotal != 0 || len(noneRows) != 0 {
		t.Errorf("events miss = total=%d rows=%d; want 0", noneTotal, len(noneRows))
	}
}
