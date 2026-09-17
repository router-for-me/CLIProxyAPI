package auth

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// fakeUsageReader is a controllable UsageWindowsReader used by the
// headroom-lookup unit tests. It tracks the call count so tests can pin
// the cache-miss/fast-path ratio and returns whatever snapshot the test
// preloads.
type fakeUsageReader struct {
	snapshot AuthQuotaSnapshot
	err      error
	calls    atomic.Int64
	delay    time.Duration
}

func (f *fakeUsageReader) GetAuthQuota(ctx context.Context, authID string) (AuthQuotaSnapshot, bool, error) {
	f.calls.Add(1)
	if f.delay > 0 {
		select {
		case <-ctx.Done():
			return AuthQuotaSnapshot{}, true, ctx.Err()
		case <-time.After(f.delay):
		}
	}
	if f.err != nil {
		return AuthQuotaSnapshot{}, false, f.err
	}
	if f.snapshot.AuthID == "" {
		f.snapshot.AuthID = authID
	}
	return f.snapshot, false, nil
}

// TestHeadroomLookupReturnsMinAcrossWindows pins the most-constrained
// window semantics: the lookup returns the LOWEST headroom_pct across
// every window in the snapshot, never the average or the max.
func TestHeadroomLookupReturnsMinAcrossWindows(t *testing.T) {
	reader := &fakeUsageReader{
		snapshot: AuthQuotaSnapshot{
			AuthID:  "auth-min",
			Channel: "openai",
			Windows: []WindowQuotaRead{
				{Size: "1m", Used: 100, Limit: 1000, HeadroomPct: 90.0},
				{Size: "1h", Used: 950, Limit: 1000, HeadroomPct: 5.0},
				{Size: "1d", Used: 400, Limit: 1000, HeadroomPct: 60.0},
			},
		},
	}
	l := NewUsageWindowsHeadroomLookup(reader, time.Minute)
	if got := l.Headroom("auth-min"); got != 5.0 {
		t.Fatalf("Headroom = %.2f, want 5.0 (min across windows)", got)
	}
}

// TestHeadroomLookupUsesCacheForRepeatCalls pins the caching behavior:
// repeated calls for the same auth within the TTL MUST NOT hit the
// reader. With 50 lookups in a tight loop, the reader sees 1 call.
func TestHeadroomLookupUsesCacheForRepeatCalls(t *testing.T) {
	reader := &fakeUsageReader{
		snapshot: AuthQuotaSnapshot{
			AuthID:  "auth-cached",
			Channel: "claude",
			Windows: []WindowQuotaRead{
				{Size: "1h", Used: 200, Limit: 1000, HeadroomPct: 80.0},
			},
		},
	}
	l := NewUsageWindowsHeadroomLookup(reader, time.Minute)
	for i := 0; i < 50; i++ {
		if got := l.Headroom("auth-cached"); got != 80.0 {
			t.Fatalf("call %d: Headroom = %.2f, want 80.0", i, got)
		}
	}
	if got := reader.calls.Load(); got != 1 {
		t.Fatalf("reader.calls = %d, want 1 (cache should absorb 49 repeats)", got)
	}
}

// TestHeadroomLookupCollapsesErrorsToUnlimited pins the safety rail: when
// the underlying fetch errors (PG transient failure, timeout, missing
// row), the lookup MUST return 100.0 so a flaky PG cannot push the
// selector toward exhaustion. Comment in the type doc explains the bias.
func TestHeadroomLookupCollapsesErrorsToUnlimited(t *testing.T) {
	reader := &fakeUsageReader{
		err: errors.New("simulated PG outage"),
	}
	l := NewUsageWindowsHeadroomLookup(reader, time.Minute)
	if got := l.Headroom("auth-failing"); got != 100.0 {
		t.Fatalf("Headroom = %.2f, want 100.0 (errors collapse to unlimited)", got)
	}
}

// TestHeadroomLookupCollapsesTimeoutToUnlimited covers the same safety
// rail for the per-call timeout path. The reader blocks longer than the
// configured timeout; the lookup returns 100.0 without panicking.
func TestHeadroomLookupCollapsesTimeoutToUnlimited(t *testing.T) {
	reader := &fakeUsageReader{
		delay: 2 * time.Second,
	}
	l := NewUsageWindowsHeadroomLookup(reader, time.Minute)
	start := time.Now()
	got := l.Headroom("auth-slow")
	elapsed := time.Since(start)
	if elapsed > 1*time.Second {
		t.Fatalf("Headroom call took %s, want <1s (timeout fired)", elapsed)
	}
	if got != 100.0 {
		t.Fatalf("Headroom = %.2f, want 100.0 (timeout collapses to unlimited)", got)
	}
}

// TestHeadroomLookupTTLExpiryEvicts pins the cache expiry: after the
// configured TTL, the next call must hit the reader again. We use a
// short 50ms TTL + a short sleep so the test runs in well under a second.
func TestHeadroomLookupTTLExpiryEvicts(t *testing.T) {
	reader := &fakeUsageReader{
		snapshot: AuthQuotaSnapshot{
			AuthID:  "auth-ttl",
			Channel: "gemini",
			Windows: []WindowQuotaRead{
				{Size: "1h", Used: 500, Limit: 1000, HeadroomPct: 50.0},
			},
		},
	}
	l := NewUsageWindowsHeadroomLookup(reader, 50*time.Millisecond)
	if got := l.Headroom("auth-ttl"); got != 50.0 {
		t.Fatalf("first call Headroom = %.2f, want 50.0", got)
	}
	if got := l.Headroom("auth-ttl"); got != 50.0 {
		t.Fatalf("cached call Headroom = %.2f, want 50.0", got)
	}
	if got := reader.calls.Load(); got != 1 {
		t.Fatalf("reader.calls = %d before TTL, want 1", got)
	}
	time.Sleep(70 * time.Millisecond)
	if got := l.Headroom("auth-ttl"); got != 50.0 {
		t.Fatalf("post-TTL Headroom = %.2f, want 50.0", got)
	}
	if got := reader.calls.Load(); got != 2 {
		t.Fatalf("reader.calls = %d after TTL, want 2", got)
	}
}

// TestHeadroomLookupEmptySnapshotIsUnlimited pins the missing-data path:
// when the reader returns a snapshot with no windows (e.g. an OAuth/auth
// that has not yet recorded any usage), the lookup returns 100.0 (no
// quota configured). This matches the StaticHeadroomLookup{} semantics
// that headroom_test.go pins for unknown auths.
func TestHeadroomLookupEmptySnapshotIsUnlimited(t *testing.T) {
	reader := &fakeUsageReader{
		snapshot: AuthQuotaSnapshot{AuthID: "auth-empty"},
	}
	l := NewUsageWindowsHeadroomLookup(reader, time.Minute)
	if got := l.Headroom("auth-empty"); got != 100.0 {
		t.Fatalf("Headroom = %.2f, want 100.0 (no windows)", got)
	}
}

// TestHeadroomLookupNilReaderIsUnlimited guards the defensive nil-check
// in the Headroom fast path. nil reader MUST NOT panic; the lookup must
// return 100.0 like the static stub.
func TestHeadroomLookupNilReaderIsUnlimited(t *testing.T) {
	l := NewUsageWindowsHeadroomLookup(nil, time.Minute)
	if got := l.Headroom("any-auth"); got != 100.0 {
		t.Fatalf("Headroom = %.2f, want 100.0 (nil reader)", got)
	}
	var nilLookup *UsageWindowsHeadroomLookup
	if got := nilLookup.Headroom("any-auth"); got != 100.0 {
		t.Fatalf("nil lookup Headroom = %.2f, want 100.0", got)
	}
}
