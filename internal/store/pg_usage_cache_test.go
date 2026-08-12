package store

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestUsageFilterIncludeRequestID(t *testing.T) {
	if !UsageFilterIncludeRequestID(UsageFilter{RequestID: "req-1"}) {
		t.Fatal("expected RequestID set to be included")
	}
	if UsageFilterIncludeRequestID(UsageFilter{}) {
		t.Fatal("expected empty RequestID to be excluded")
	}
}

func TestUsageFilterKeyDistinct(t *testing.T) {
	base := func() UsageFilter {
		return UsageFilter{
			APIKeyID:  "ak-1",
			Principal: "user@example.com",
			Provider:  "claude",
			Model:     "claude-opus",
			UserID:    "u-1",
			From:      time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
			To:        time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC),
			GroupBy:   "model",
			Limit:     10,
		}
	}

	// Different predicate fields must produce different keys.
	fields := []UsageFilter{
		base(),
		func() UsageFilter { f := base(); f.APIKeyID = "ak-2"; return f }(),
		func() UsageFilter { f := base(); f.Principal = "other@example.com"; return f }(),
		func() UsageFilter { f := base(); f.Provider = "gemini"; return f }(),
		func() UsageFilter { f := base(); f.Model = "gemini-pro"; return f }(),
		func() UsageFilter { f := base(); f.UserID = "u-2"; return f }(),
		func() UsageFilter { f := base(); f.GroupBy = "provider"; return f }(),
		func() UsageFilter { f := base(); f.Limit = 99; return f }(),
	}
	seen := map[string]bool{usageFilterKey(fields[0]): true}
	for i, f := range fields[1:] {
		k := usageFilterKey(f)
		if seen[k] {
			t.Fatalf("field %d produced a colliding key", i+1)
		}
		seen[k] = true
	}

	// Time-range differences must matter.
	fromShifted := base()
	fromShifted.From = fromShifted.From.Add(time.Hour)
	if usageFilterKey(base()) == usageFilterKey(fromShifted) {
		t.Fatal("expected time-range (From) difference to change the key")
	}
	toShifted := base()
	toShifted.To = toShifted.To.Add(time.Hour)
	if usageFilterKey(base()) == usageFilterKey(toShifted) {
		t.Fatal("expected time-range (To) difference to change the key")
	}

	// RequestID must NOT change the key (it routes to the uncached path).
	if usageFilterKey(base()) != usageFilterKey(func() UsageFilter {
		f := base()
		f.RequestID = "req-xyz"
		return f
	}()) {
		t.Fatal("expected RequestID to be excluded from the cache key")
	}
}

// advanceableClock returns a controllable time source for cache tests. Advance
// the returned pointer's time to move the cache clock forward.
func advanceableClock() (*time.Time, func() time.Time) {
	t := time.Now()
	return &t, func() time.Time { return t }
}

// TestUsageCacheHitAndMiss verifies read-through: the loader runs on the first
// call, is skipped on a second call within TTL, and re-runs after TTL expiry.
func TestUsageCacheHitAndMiss(t *testing.T) {
	now, clock := advanceableClock()
	c := newUsageCacheWithClock(clock)
	var calls atomic.Int64
	load := func() ([]UsageAggregate, error) {
		calls.Add(1)
		return []UsageAggregate{{Bucket: "model", RequestCount: 1}}, nil
	}
	f := UsageFilter{GroupBy: "model"}

	got, err := c.getAggregate(nil, f, load)
	if err != nil || len(got) != 1 {
		t.Fatalf("first call: got %v err %v", got, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected 1 loader call, got %d", calls.Load())
	}

	// Second call within TTL must be served from cache (no loader).
	if _, err := c.getAggregate(nil, f, load); err != nil {
		t.Fatalf("second call: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected cache hit (still 1 loader call), got %d", calls.Load())
	}

	// After TTL, the loader must re-run (advance the fake clock past the TTL).
	*now = now.Add(usageCacheTTL + time.Millisecond)
	if _, err := c.getAggregate(nil, f, load); err != nil {
		t.Fatalf("post-TTL call: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("expected loader re-run after TTL (2 calls), got %d", calls.Load())
	}
}

// TestUsageCacheSingleFlight verifies that N concurrent callers for the same
// key share exactly one loader invocation. A slow loader with a barrier forces
// overlap.
func TestUsageCacheSingleFlight(t *testing.T) {
	c := newUsageCache()
	var calls atomic.Int64
	start := make(chan struct{})
	load := func() ([]UsageAggregate, error) {
		calls.Add(1)
		// Wait for all goroutines to be in flight before returning.
		<-start
		return []UsageAggregate{{Bucket: "total"}}, nil
	}
	f := UsageFilter{}

	const n = 50
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.getAggregate(nil, f, load); err != nil {
				errs <- err
			}
		}()
	}
	// Give goroutines a moment to reach the loader.
	time.Sleep(50 * time.Millisecond)
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("unexpected error: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected exactly 1 loader invocation, got %d", calls.Load())
	}
}

// TestUsageCacheFailOpen verifies that a loader error is NOT cached: the next
// call runs the loader again and can recover.
func TestUsageCacheFailOpen(t *testing.T) {
	c := newUsageCache()
	var calls atomic.Int64
	load := func() ([]UsageAggregate, error) {
		n := calls.Add(1)
		if n == 1 {
			return nil, errTestCacheFailure
		}
		return []UsageAggregate{{Bucket: "ok"}}, nil
	}
	f := UsageFilter{}

	if _, err := c.getAggregate(nil, f, load); err == nil {
		t.Fatal("expected the first (failing) call to return an error")
	}
	// The failure must not have been cached: this call reruns and succeeds.
	got, err := c.getAggregate(nil, f, load)
	if err != nil {
		t.Fatalf("recovery call failed: %v", err)
	}
	if len(got) != 1 || got[0].Bucket != "ok" {
		t.Fatalf("unexpected recovery result: %v", got)
	}
	if calls.Load() != 2 {
		t.Fatalf("expected 2 loader calls (fail then recover), got %d", calls.Load())
	}

	// The successful result should now be cached: a third call does not re-run.
	if _, err := c.getAggregate(nil, f, load); err != nil {
		t.Fatalf("cached call failed: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("expected successful result cached (still 2 calls), got %d", calls.Load())
	}
}

// TestUsageCacheTTLExpiry verifies the loader re-runs once the TTL has elapsed.
func TestUsageCacheTTLExpiry(t *testing.T) {
	now, clock := advanceableClock()
	c := newUsageCacheWithClock(clock)
	var calls atomic.Int64
	load := func() ([]UsageAggregate, error) {
		calls.Add(1)
		return []UsageAggregate{{Bucket: "model"}}, nil
	}
	f := UsageFilter{GroupBy: "model"}

	if _, err := c.getAggregate(nil, f, load); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected 1 call, got %d", calls.Load())
	}
	// Advance the clock past the TTL: the loader must re-run.
	*now = now.Add(usageCacheTTL + time.Millisecond)
	if _, err := c.getAggregate(nil, f, load); err != nil {
		t.Fatalf("post-TTL call: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("expected loader re-run after TTL, got %d", calls.Load())
	}
}

// TestUsageCacheRowsVsTotalsDistinct verifies that aggregate-rows and totals
// for the same filter are cached independently (no cross-kind collision).
func TestUsageCacheRowsVsTotalsDistinct(t *testing.T) {
	c := newUsageCache()
	var rowCalls, totCalls atomic.Int64
	rowLoad := func() ([]UsageAggregate, error) {
		rowCalls.Add(1)
		return []UsageAggregate{{Bucket: "model", RequestCount: 7}}, nil
	}
	totLoad := func() (UsageAggregate, error) {
		totCalls.Add(1)
		return UsageAggregate{Bucket: "total", RequestCount: 42}, nil
	}
	f := UsageFilter{GroupBy: "model"}

	if _, err := c.getAggregate(nil, f, rowLoad); err != nil {
		t.Fatalf("rows call: %v", err)
	}
	tot, err := c.getTotals(nil, f, totLoad)
	if err != nil {
		t.Fatalf("totals call: %v", err)
	}
	if tot.RequestCount != 42 {
		t.Fatalf("expected totals count 42, got %d", tot.RequestCount)
	}
	// Both kinds loaded once each.
	if rowCalls.Load() != 1 || totCalls.Load() != 1 {
		t.Fatalf("expected 1 row + 1 totals call, got %d + %d", rowCalls.Load(), totCalls.Load())
	}
	// Both cached: no extra loader runs.
	if _, err := c.getAggregate(nil, f, rowLoad); err != nil {
		t.Fatalf("rows cached call: %v", err)
	}
	if _, err := c.getTotals(nil, f, totLoad); err != nil {
		t.Fatalf("totals cached call: %v", err)
	}
	if rowCalls.Load() != 1 || totCalls.Load() != 1 {
		t.Fatalf("expected cache hits (still 1 row + 1 totals call), got %d + %d", rowCalls.Load(), totCalls.Load())
	}
}

// errTestCacheFailure is a sentinel injected by failing test loaders.
var errTestCacheFailure = errTestCacheFailureType{}

type errTestCacheFailureType struct{}

func (errTestCacheFailureType) Error() string { return "test cache failure" }
