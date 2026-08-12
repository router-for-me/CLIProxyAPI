package store

import (
	"context"
	"testing"
	"time"
)

// rollupRow is the scan target for one usage_stat_day row, mirroring the table
// DDL in Migrate. The six PK columns are always non-NULL (the fold COALESCEs
// empty/NULL events to ”), so they scan into plain strings.
type rollupRow struct {
	StatDay      time.Time
	UserID       string
	APIKeyID     string
	Model        string
	Provider     string
	Source       string
	RequestCount int64
	FailCount    int64
	InputTokens  int64
	OutputTokens int64
	TotTokens    int64
	CostUSD      float64
}

// queryRollupRows loads every row from usage_stat_day ordered deterministically
// so assertions can compare exact sets.
func queryRollupRows(t *testing.T, us *UsageStore) []rollupRow {
	t.Helper()
	rows, err := us.db.Query(`SELECT stat_day, user_id, api_key_id, model, provider, source,
		request_count, fail_count, input_tokens, output_tokens, tot_tokens, cost_usd
		FROM ` + us.rollupTable + `
		ORDER BY stat_day, user_id, api_key_id, model, provider, source`)
	if err != nil {
		t.Fatalf("query rollup rows: %v", err)
	}
	defer rows.Close()
	var out []rollupRow
	for rows.Next() {
		var r rollupRow
		if err = rows.Scan(&r.StatDay, &r.UserID, &r.APIKeyID, &r.Model, &r.Provider,
			&r.Source, &r.RequestCount, &r.FailCount, &r.InputTokens, &r.OutputTokens,
			&r.TotTokens, &r.CostUSD); err != nil {
			t.Fatalf("scan rollup row: %v", err)
		}
		out = append(out, r)
	}
	if err = rows.Err(); err != nil {
		t.Fatalf("iterate rollup rows: %v", err)
	}
	return out
}

// findRollup returns the row matching the exact bucket, or false.
func findRollup(rows []rollupRow, user, apiKey, model, provider, source string) (rollupRow, bool) {
	for _, r := range rows {
		if r.UserID == user && r.APIKeyID == apiKey && r.Model == model &&
			r.Provider == provider && r.Source == source {
			return r, true
		}
	}
	return rollupRow{}, false
}

// TestRunRollupIdempotent is the core correctness test: folding a known day of
// usage_events must produce the expected per-bucket totals, and re-running the
// fold over the same append-only rows must NOT double-count (the SET/overwrite
// fold yields identical totals on the second run).
func TestRunRollupIdempotent(t *testing.T) {
	pg := newTestPostgresStore(t, "test_rollup_idempotent")
	defer pg.Close()
	ensureMigrated(t, pg)
	us := NewUsageStore(pg)

	ctx := context.Background()
	// A single UTC calendar day. InsertEvent defaults RequestedAt to time.Now()
	// when zero, so bake explicit timestamps.
	day := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	rows := []UsageEvent{
		// user u1, two successful requests on model m1/provider p1.
		{UserID: "u1", Model: "m1", Provider: "p1", Source: "srv",
			InputTokens: 100, OutputTokens: 50, TotalTokens: 150, CostUSD: 0.01, RequestedAt: day.Add(time.Hour)},
		{UserID: "u1", Model: "m1", Provider: "p1", Source: "srv",
			InputTokens: 200, OutputTokens: 100, TotalTokens: 300, CostUSD: 0.02, RequestedAt: day.Add(2 * time.Hour)},
		// user u1, one FAILED request on model m1/provider p2 (different provider).
		{UserID: "u1", Model: "m1", Provider: "p2", Source: "srv", Failed: true,
			InputTokens: 50, OutputTokens: 0, TotalTokens: 50, CostUSD: 0.005, RequestedAt: day.Add(3 * time.Hour)},
		// user u2, one request on a different model/provider + empty source.
		{UserID: "u2", Model: "m2", Provider: "p1", Source: "",
			InputTokens: 10, OutputTokens: 5, TotalTokens: 15, CostUSD: 0.001, RequestedAt: day.Add(4 * time.Hour)},
		// No user_id / no api_key_id (NULL → folded to '').
		{Model: "m1", Provider: "p3", Source: "svc",
			InputTokens: 5, OutputTokens: 5, TotalTokens: 10, CostUSD: 0.0005, RequestedAt: day.Add(5 * time.Hour)},
	}
	for _, e := range rows {
		if err := us.InsertEvent(ctx, e); err != nil {
			t.Fatalf("insert usage event: %v", err)
		}
	}

	if err := us.RunRollup(ctx, day); err != nil {
		t.Fatalf("RunRollup: %v", err)
	}

	got := queryRollupRows(t, us)
	if len(got) != 4 {
		t.Fatalf("expected 4 rollup buckets, got %d: %+v", len(got), got)
	}

	r, ok := findRollup(got, "u1", "", "m1", "p1", "srv")
	if !ok {
		t.Fatalf("missing bucket u1/m1/p1: %+v", got)
	}
	if r.RequestCount != 2 || r.FailCount != 0 || r.InputTokens != 300 || r.OutputTokens != 150 || r.TotTokens != 450 {
		t.Fatalf("u1/m1/p1 totals wrong: %+v", r)
	}
	if r.CostUSD != 0.03 {
		t.Fatalf("u1/m1/p1 cost wrong: got %v want 0.03", r.CostUSD)
	}

	r, ok = findRollup(got, "u1", "", "m1", "p2", "srv")
	if !ok {
		t.Fatalf("missing bucket u1/m1/p2: %+v", got)
	}
	if r.RequestCount != 1 || r.FailCount != 1 || r.InputTokens != 50 || r.OutputTokens != 0 || r.TotTokens != 50 {
		t.Fatalf("u1/m1/p2 (failed) totals wrong: %+v", r)
	}

	r, ok = findRollup(got, "u2", "", "m2", "p1", "")
	if !ok {
		t.Fatalf("missing bucket u2/m2/p1/empty-source: %+v", got)
	}
	if r.RequestCount != 1 || r.FailCount != 0 || r.InputTokens != 10 || r.OutputTokens != 5 || r.TotTokens != 15 {
		t.Fatalf("u2/m2/p1 totals wrong: %+v", r)
	}

	r, ok = findRollup(got, "", "", "m1", "p3", "svc")
	if !ok {
		t.Fatalf("missing NULL-user bucket m1/p3: %+v", got)
	}
	if r.RequestCount != 1 || r.FailCount != 0 || r.TotTokens != 10 {
		t.Fatalf("NULL-user bucket totals wrong: %+v", r)
	}

	// The critical idempotency assertion: re-run the fold over the SAME
	// append-only rows. With SET (overwrite) semantics the totals must be
	// unchanged — no double counting.
	if err := us.RunRollup(ctx, day); err != nil {
		t.Fatalf("RunRollup (second run): %v", err)
	}
	got2 := queryRollupRows(t, us)
	if len(got2) != 4 {
		t.Fatalf("expected still 4 rollup buckets after re-run, got %d", len(got2))
	}
	for i := range got {
		if got[i] != got2[i] {
			t.Fatalf("rollup changed after idempotent re-run:\n before %+v\n after  %+v", got[i], got2[i])
		}
	}

	// A third run for good measure.
	if err := us.RunRollup(ctx, day); err != nil {
		t.Fatalf("RunRollup (third run): %v", err)
	}
	got3 := queryRollupRows(t, us)
	if len(got3) != 4 {
		t.Fatalf("expected still 4 rollup buckets after third run, got %d", len(got3))
	}
	for i := range got {
		if got[i] != got3[i] {
			t.Fatalf("rollup changed after third run:\n before %+v\n after  %+v", got[i], got3[i])
		}
	}
}

// TestRunRollupPinsStatDayToUTC proves the bucket date is pinned to UTC: with
// the session timezone set to a non-UTC zone, an event near UTC midnight must
// still land on the event's UTC calendar day (not the session's). Without the
// AT TIME ZONE 'UTC' pin, date_trunc over a timestamptz truncates in the session
// timezone and this test fails.
func TestRunRollupPinsStatDayToUTC(t *testing.T) {
	pg := newTestPostgresStore(t, "test_rollup_utc_pin")
	defer pg.Close()
	ensureMigrated(t, pg)
	us := NewUsageStore(pg)
	ctx := context.Background()

	// Put the session in a zone far from UTC so any session-zone truncation
	// would visibly shift the bucket. Pacific is UTC-8: 01:30 UTC on day X is
	// still 17:30 the PREVIOUS day in the session zone.
	if _, err := us.db.ExecContext(ctx, `SET TIMEZONE = 'America/Los_Angeles'`); err != nil {
		t.Fatalf("set session timezone: %v", err)
	}
	// Cleanup: restore the session TZ so the pooled connection is left in a
	// sane state for any later reuse.
	defer func() {
		_, _ = us.db.ExecContext(context.Background(), `SET TIMEZONE = 'UTC'`)
	}()

	// 01:30 UTC on 2026-08-12 — in America/Los_Angeles this is 18:30 on
	// 2026-08-11, so an un-pinned truncation would produce a stat_day of
	// 2026-08-11 instead of 2026-08-12.
	event := UsageEvent{UserID: "u1", Model: "m1", Provider: "p1", Source: "svc",
		InputTokens: 10, OutputTokens: 10, TotalTokens: 20, CostUSD: 0.01,
		RequestedAt: time.Date(2026, 8, 12, 1, 30, 0, 0, time.UTC)}
	if err := us.InsertEvent(ctx, event); err != nil {
		t.Fatalf("insert usage event: %v", err)
	}
	day := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	if err := us.RunRollup(ctx, day); err != nil {
		t.Fatalf("RunRollup: %v", err)
	}
	got := queryRollupRows(t, us)
	if len(got) != 1 {
		t.Fatalf("expected 1 rollup bucket, got %d: %+v", len(got), got)
	}
	wantDay := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	if !got[0].StatDay.Equal(wantDay) {
		t.Fatalf("stat_day bucketed to %v in a non-UTC session; want UTC day %v", got[0].StatDay, wantDay)
	}
}

// TestBackfillRollupMultiDay folds several prior days and asserts each day's
// totals land in its own bucket, and that the fold stays idempotent across the
// range when re-run.
func TestBackfillRollupMultiDay(t *testing.T) {
	pg := newTestPostgresStore(t, "test_rollup_backfill")
	defer pg.Close()
	ensureMigrated(t, pg)
	us := NewUsageStore(pg)

	ctx := context.Background()
	d1 := time.Date(2026, 8, 7, 9, 0, 0, 0, time.UTC)
	d2 := time.Date(2026, 8, 8, 9, 0, 0, 0, time.UTC)
	events := []UsageEvent{
		{UserID: "u1", Model: "m1", Provider: "p1", Source: "svc",
			InputTokens: 10, OutputTokens: 10, TotalTokens: 20, CostUSD: 0.01, RequestedAt: d1},
		{UserID: "u1", Model: "m1", Provider: "p1", Source: "svc",
			InputTokens: 10, OutputTokens: 10, TotalTokens: 20, CostUSD: 0.01, RequestedAt: d1.Add(time.Hour)},
		{UserID: "u1", Model: "m1", Provider: "p1", Source: "svc",
			InputTokens: 30, OutputTokens: 30, TotalTokens: 60, CostUSD: 0.03, RequestedAt: d2},
	}
	for _, e := range events {
		if err := us.InsertEvent(ctx, e); err != nil {
			t.Fatalf("insert usage event: %v", err)
		}
	}

	// Backfill the 2 days ending at d3 (exclusive): folds d1 and d2.
	d3 := d2.AddDate(0, 0, 1)
	if err := us.BackfillRollup(ctx, d3, 2); err != nil {
		t.Fatalf("BackfillRollup: %v", err)
	}
	got := queryRollupRows(t, us)
	if len(got) != 2 {
		t.Fatalf("expected 2 rollup buckets (one per day), got %d: %+v", len(got), got)
	}
	var d1Row, d2Row rollupRow
	for _, r := range got {
		switch {
		case r.StatDay.Equal(d1.Truncate(24 * time.Hour)):
			d1Row = r
		case r.StatDay.Equal(d2.Truncate(24 * time.Hour)):
			d2Row = r
		default:
			t.Fatalf("unexpected stat_day %v", r.StatDay)
		}
	}
	if d1Row.RequestCount != 2 || d1Row.InputTokens != 20 || d1Row.TotTokens != 40 {
		t.Fatalf("d1 totals wrong: %+v", d1Row)
	}
	if d2Row.RequestCount != 1 || d2Row.InputTokens != 30 || d2Row.TotTokens != 60 {
		t.Fatalf("d2 totals wrong: %+v", d2Row)
	}

	// Re-running the backfill must be idempotent (no double counting).
	if err := us.BackfillRollup(ctx, d3, 2); err != nil {
		t.Fatalf("BackfillRollup (re-run): %v", err)
	}
	got2 := queryRollupRows(t, us)
	if len(got2) != 2 {
		t.Fatalf("expected still 2 buckets after backfill re-run, got %d", len(got2))
	}
	for i := range got {
		if got[i] != got2[i] {
			t.Fatalf("backfill changed after idempotent re-run:\n before %+v\n after  %+v", got[i], got2[i])
		}
	}

	// days <= 0 is a no-op.
	if err := us.BackfillRollup(ctx, d3, 0); err != nil {
		t.Fatalf("BackfillRollup(0) should be a no-op: %v", err)
	}
}

// TestRunRollupUpdatesAfterNewEvents verifies the incremental contract: folding
// "today", adding events later, then folding again picks up the new events with
// SUMS (not replacing) — the overwrite fold regenerates the bucket from all rows
// in the window, so totals reflect the union without double-counting the first
// batch.
func TestRunRollupUpdatesAfterNewEvents(t *testing.T) {
	pg := newTestPostgresStore(t, "test_rollup_incremental")
	defer pg.Close()
	ensureMigrated(t, pg)
	us := NewUsageStore(pg)

	ctx := context.Background()
	day := time.Date(2026, 8, 11, 8, 0, 0, 0, time.UTC)
	base := UsageEvent{UserID: "u1", Model: "m1", Provider: "p1", Source: "svc",
		InputTokens: 100, OutputTokens: 100, TotalTokens: 200, CostUSD: 0.02, RequestedAt: day}
	if err := us.InsertEvent(ctx, base); err != nil {
		t.Fatalf("insert base event: %v", err)
	}
	if err := us.RunRollup(ctx, day); err != nil {
		t.Fatalf("RunRollup(first): %v", err)
	}
	got := queryRollupRows(t, us)
	if len(got) != 1 || got[0].RequestCount != 1 || got[0].InputTokens != 100 {
		t.Fatalf("after first fold: %+v", got)
	}

	// A second event lands in the same window.
	base.RequestedAt = day.Add(time.Hour)
	if err := us.InsertEvent(ctx, base); err != nil {
		t.Fatalf("insert second event: %v", err)
	}
	if err := us.RunRollup(ctx, day); err != nil {
		t.Fatalf("RunRollup(second): %v", err)
	}
	got = queryRollupRows(t, us)
	if len(got) != 1 {
		t.Fatalf("expected one bucket after second fold, got %d: %+v", len(got), got)
	}
	if got[0].RequestCount != 2 || got[0].InputTokens != 200 || got[0].TotTokens != 400 {
		t.Fatalf("bucket totals wrong after second fold (should be union, not double): %+v", got[0])
	}
}

// seedRollupGroupByUserData inserts a known two-day usage fixture (one full day,
// one partial day) and rolls up its first day, so the day-aligned fast path in
// selectAggregateMiss can be exercised against a populated usage_stat_day.
func seedRollupGroupByUserData(t *testing.T, us *UsageStore) (day, partial time.Time) {
	t.Helper()
	ctx := context.Background()
	fullDay := time.Date(2026, 8, 12, 0, 0, 0, 0, time.UTC)
	partial = time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	events := []UsageEvent{
		// ---- full day (2026-08-12): two users, no empty-user rows. ----
		// u1: two successful + data split across buckets that must all sum to u1.
		{UserID: "u1", Model: "m1", Provider: "p1", Source: "svc",
			InputTokens: 100, OutputTokens: 50, TotalTokens: 150, CostUSD: 0.01, RequestedAt: fullDay.Add(time.Hour)},
		{UserID: "u1", Model: "m1", Provider: "p2", Source: "svc", Failed: true,
			InputTokens: 50, OutputTokens: 0, TotalTokens: 50, CostUSD: 0.005, RequestedAt: fullDay.Add(2 * time.Hour)},
		// u2: one request.
		{UserID: "u2", Model: "m2", Provider: "p1", Source: "",
			InputTokens: 10, OutputTokens: 5, TotalTokens: 15, CostUSD: 0.001, RequestedAt: fullDay.Add(3 * time.Hour)},
		// An event with NO user_id and NO api_key_id: folded into the empty
		// placeholder bucket, which the fast path must drop via user_id <> ''.
		{Model: "m1", Provider: "p3", Source: "svc",
			InputTokens: 5, OutputTokens: 5, TotalTokens: 10, CostUSD: 0.0005, RequestedAt: fullDay.Add(4 * time.Hour)},
		// ---- partial day (2026-08-13): events only near the 12:00 UTC mark. ----
		{UserID: "u1", Model: "m1", Provider: "p1", Source: "svc",
			InputTokens: 400, OutputTokens: 200, TotalTokens: 600, CostUSD: 0.04, RequestedAt: partial.Add(12 * time.Hour)},
		{UserID: "u3", Model: "m3", Provider: "p1", Source: "svc",
			InputTokens: 20, OutputTokens: 10, TotalTokens: 30, CostUSD: 0.002, RequestedAt: partial.Add(12 * time.Hour)},
	}
	for _, e := range events {
		if err := us.InsertEvent(ctx, e); err != nil {
			t.Fatalf("insert usage event: %v", err)
		}
	}
	if err := us.RunRollup(ctx, fullDay); err != nil {
		t.Fatalf("RunRollup(fullDay): %v", err)
	}
	return fullDay, partial
}

// aggregateByBucket folds a []UsageAggregate into an index keyed by Bucket so
// tests can assert per-user values regardless of row order.
func aggregateByBucket(rows []UsageAggregate) map[string]UsageAggregate {
	m := make(map[string]UsageAggregate, len(rows))
	for _, r := range rows {
		m[r.Bucket] = r
	}
	return m
}

// TestSelectAggregateDayAlignedRollupByUser proves the day-aligned group-by-user
// fast path is correct: with a single fully-rolled-up day it returns per-user
// sums sourced from usage_stat_day (not a usage_events scan), and drops the
// empty placeholder user bucket.
func TestSelectAggregateDayAlignedRollupByUser(t *testing.T) {
	pg := newTestPostgresStore(t, "test_user_rollup_fast")
	defer pg.Close()
	ensureMigrated(t, pg)
	us := NewUsageStore(pg)

	day, _ := seedRollupGroupByUserData(t, us)

	rows, err := us.SelectAggregate(context.Background(), UsageFilter{
		GroupBy: "user_id",
		From:    day,
		To:      day.Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("SelectAggregate: %v", err)
	}
	got := aggregateByBucket(rows)

	for _, user := range []string{"u1", "u2"} {
		if _, ok := got[user]; !ok {
			t.Fatalf("missing user bucket %q in %+v", user, got)
		}
	}
	u1 := got["u1"]
	if u1.RequestCount != 2 || u1.FailedCount != 1 {
		t.Fatalf("u1 request/fail counts wrong: %+v", u1)
	}
	if u1.InputTokens != 150 || u1.OutputTokens != 50 || u1.TotalTokens != 200 {
		t.Fatalf("u1 token sums wrong: %+v", u1)
	}
	if u1.CostUSD != 0.015 {
		t.Fatalf("u1 cost wrong: got %v want 0.015", u1.CostUSD)
	}
	// The rollup tracks neither reasoning nor cached tokens; both project as 0.
	if u1.ReasoningTokens != 0 || u1.CachedTokens != 0 {
		t.Fatalf("u1 reasoning/cached must be 0 from the rollup: %+v", u1)
	}
	u2 := got["u2"]
	if u2.RequestCount != 1 || u2.InputTokens != 10 || u2.OutputTokens != 5 || u2.TotalTokens != 15 {
		t.Fatalf("u2 totals wrong: %+v", u2)
	}
	// The empty-user placeholder bucket must be excluded from the rollup path.
	if _, ok := got[""]; ok {
		t.Fatalf("empty user bucket must be dropped by the rollup fast path: %+v", got)
	}
	if len(rows) != 2 {
		t.Fatalf("expected exactly 2 user buckets from rollup, got %d: %+v", len(rows), rows)
	}
}

// TestSelectAggregateDayAlignedRollupConsistency is the key correctness
// invariant: over a complete day, the rollup fast path and the on-the-fly
// usage_events scan MUST produce identical per-user sums. It proves the fast
// path is a faithful substitute for the query it replaces.
func TestSelectAggregateDayAlignedRollupConsistency(t *testing.T) {
	pg := newTestPostgresStore(t, "test_user_rollup_consistent")
	defer pg.Close()
	ensureMigrated(t, pg)
	us := NewUsageStore(pg)

	day, _ := seedRollupGroupByUserData(t, us)
	ctx := context.Background()
	filter := UsageFilter{GroupBy: "user_id", From: day, To: day.Add(24 * time.Hour)}

	rollup, err := us.SelectAggregate(ctx, filter)
	if err != nil {
		t.Fatalf("rollup SelectAggregate: %v", err)
	}
	// Force the on-the-fly path by disabling the rollup table — the fallback
	// guard is rollupTable == "", so an un-named table routes to usage_events.
	noRollup := *us
	noRollup.rollupTable = ""
	onFly, err := noRollup.SelectAggregate(ctx, filter)
	if err != nil {
		t.Fatalf("on-the-fly SelectAggregate: %v", err)
	}
	if len(rollup) != len(onFly) {
		t.Fatalf("row count mismatch: rollup %d vs on-the-fly %d\nrollup %+v\nonfly %+v",
			len(rollup), len(onFly), rollup, onFly)
	}
	for i := range rollup {
		if rollup[i] != onFly[i] {
			t.Fatalf("row %d mismatch (rollup vs on-the-fly):\n  rollup %+v\n  onfly  %+v", i, rollup[i], onFly[i])
		}
	}
}

// TestSelectAggregateDayAlignedRollupUserFilterFallback proves that a non-empty
// user_id filter (a page key on one of the rollup's six dimensions) does NOT
// take the fast path — it must route to the filtered usage_events scan and
// still return the correct per-user answer for the whole day.
func TestSelectAggregateDayAlignedRollupUserFilterFallback(t *testing.T) {
	pg := newTestPostgresStore(t, "test_user_rollup_filter")
	defer pg.Close()
	ensureMigrated(t, pg)
	us := NewUsageStore(pg)

	day, _ := seedRollupGroupByUserData(t, us)
	ctx := context.Background()
	filter := UsageFilter{GroupBy: "user_id", UserID: "u1", From: day, To: day.Add(24 * time.Hour)}

	rows, err := us.SelectAggregate(ctx, filter)
	if err != nil {
		t.Fatalf("SelectAggregate (filtered): %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected exactly 1 bucket for u1, got %d: %+v", len(rows), rows)
	}
	got := rows[0]
	if got.Bucket != "u1" {
		t.Fatalf("bucket = %q, want u1", got.Bucket)
	}
	if got.RequestCount != 2 || got.InputTokens != 150 || got.OutputTokens != 50 || got.CostUSD != 0.015 {
		t.Fatalf("u1 whole-day filtered totals wrong: %+v", got)
	}
	// The filtered path runs over usage_events, which DOES project real
	// reasoning/cached tokens (0 here) — the point is correctness of counts.
	if got.TotalTokens != 200 {
		t.Fatalf("u1 total tokens wrong: %+v", got)
	}
}

// TestSelectAggregateNonAlignedRollupFallback proves a non-midnight From (a
// partial-day window) does NOT take the rollup path: it falls back to the
// usage_events scan and returns only the events at/after the partial bound.
func TestSelectAggregateNonAlignedRollupFallback(t *testing.T) {
	pg := newTestPostgresStore(t, "test_user_rollup_non_aligned")
	defer pg.Close()
	ensureMigrated(t, pg)
	us := NewUsageStore(pg)

	_, partial := seedRollupGroupByUserData(t, us)
	ctx := context.Background()
	// From = noon UTC, To = end of the partial day. Neither is a midnight, so
	// the fast path must not engage. Only the partial-day rows (which land
	// exactly at the From bound) are in scope.
	filter := UsageFilter{GroupBy: "user_id",
		From: partial.Add(12 * time.Hour),
		To:   partial.Add(40 * time.Hour),
	}
	rows, err := us.SelectAggregate(ctx, filter)
	if err != nil {
		t.Fatalf("SelectAggregate (non-aligned): %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 users (u1,u3) in the partial window, got %d: %+v", len(rows), rows)
	}
	got := aggregateByBucket(rows)
	if got["u1"].RequestCount != 1 || got["u1"].InputTokens != 400 || got["u1"].CostUSD != 0.04 {
		t.Fatalf("u1 partial totals wrong: %+v", got["u1"])
	}
	if got["u3"].RequestCount != 1 || got["u3"].InputTokens != 20 || got["u3"].CostUSD != 0.002 {
		t.Fatalf("u3 partial totals wrong: %+v", got["u3"])
	}
	// Sanity: the full-day rollup rows must NOT bleed into the partial window
	// (the fullDay data has u2 with cost 0.001; it must be absent here).
	if _, ok := got["u2"]; ok {
		t.Fatalf("u2 must not appear in a partial-day window: %+v", got)
	}
}

// TestSelectAggregateNoWindowRollupByUser proves the both-bounds-zero case still
// fast-paths: all rolled-up users' totals are returned from usage_stat_day with
// no time predicates.
func TestSelectAggregateNoWindowRollupByUser(t *testing.T) {
	pg := newTestPostgresStore(t, "test_user_rollup_nowindow")
	defer pg.Close()
	ensureMigrated(t, pg)
	us := NewUsageStore(pg)

	seedRollupGroupByUserData(t, us)

	rows, err := us.SelectAggregate(context.Background(), UsageFilter{GroupBy: "user_id"})
	if err != nil {
		t.Fatalf("SelectAggregate (no window): %v", err)
	}
	got := aggregateByBucket(rows)
	// Only the rolled-up full day is in usage_stat_day; u1 and u2 totals match
	// that day, and u3 (only in the un-rolled partial day) must be absent.
	if got["u1"].RequestCount != 2 || got["u1"].CostUSD != 0.015 || got["u1"].TotalTokens != 200 {
		t.Fatalf("u1 no-window totals wrong: %+v", got["u1"])
	}
	if got["u2"].RequestCount != 1 || got["u2"].CostUSD != 0.001 {
		t.Fatalf("u2 no-window totals wrong: %+v", got["u2"])
	}
	if _, ok := got["u3"]; ok {
		t.Fatalf("u3 must not appear (partial day was never rolled up): %+v", got)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 user buckets with no window, got %d: %+v", len(rows), rows)
	}
}
