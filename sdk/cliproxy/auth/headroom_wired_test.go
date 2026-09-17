package auth

import (
	"context"
	"testing"
	"time"
)

// TestSchedulerHeadroom_UsesWiredLookup is the regression guard for Task 7:
// when the scheduler is wired with a usage_windows-backed lookup, the
// headroom selector MUST consult that lookup (not the StaticHeadroomLookup
// default). The test reuses the round-2 headroom test fixture but
// substitutes the production lookup instead of a StaticHeadroomLookup —
// the headroom selector must still pick the highest-remaining auth.
//
// This pins the wiring contract documented in scheduler.go:
// "Tests inject scripted lookups directly via scheduler.headroomLookup;
// production wires a usage_windows-backed implementation here at startup
// (Task 7 closes the loop on Task 5's stub)."
func TestSchedulerHeadroom_UsesWiredLookup(t *testing.T) {
	t.Parallel()

	const model = "headroom-wired-model"
	// Per-auth snapshots; each lookup resolves a different headroom so the
	// selector has a clear winner. The lookup returns the minimum across
	// windows; one row is enough to differentiate.
	snapshotFor := func(used, limit int64, authID string) AuthQuotaSnapshot {
		return AuthQuotaSnapshot{
			AuthID: authID,
			Windows: []WindowQuotaRead{
				{Size: "1h", Used: used, Limit: limit, HeadroomPct: headroomPctFor(used, limit)},
			},
		}
	}
	readers := map[string]*fakeUsageReader{
		"hr-wired-best": {snapshot: snapshotFor(100, 1000, "hr-wired-best")},
		"hr-wired-mid":  {snapshot: snapshotFor(500, 1000, "hr-wired-mid")},
		"hr-wired-low":  {snapshot: snapshotFor(900, 1000, "hr-wired-low")},
	}
	multi := &multiAuthReader{readers: readers}
	lookup := NewUsageWindowsHeadroomLookup(multi, time.Minute)

	scheduler := newHeadroomSchedulerForTest(t, model, nil, nil,
		&Auth{ID: "hr-wired-best", Provider: "gemini"},
		&Auth{ID: "hr-wired-mid", Provider: "gemini"},
		&Auth{ID: "hr-wired-low", Provider: "gemini"},
	)
	// Replace the nil lookup the helper left in place with the production
	// wired implementation. SetHeadroomLookup is the same setter production
	// uses, so this exercises the real wiring path.
	scheduler.SetHeadroomLookup(lookup)

	for i := 0; i < 5; i++ {
		auth, _ := pickHeadroomWithStrategy(t, scheduler, model, nil)
		if auth.ID != "hr-wired-best" {
			t.Fatalf("pick #%d = %q, want hr-wired-best (highest headroom via wired lookup)", i, auth.ID)
		}
	}
}

// headroomPctFor mirrors computeHeadroom's clamping rules. Used by the
// snapshot builder so the test does not import the management-package
// helper. Kept byte-identical with the production function so a future
// drift surfaces as a test failure here.
func headroomPctFor(used, limit int64) float64 {
	if limit <= 0 {
		return 100.0
	}
	pct := float64(limit-used) / float64(limit) * 100.0
	if pct < 0 {
		return 0.0
	}
	return pct
}

// multiAuthReader fans out GetAuthQuota calls to per-auth subreaders so
// the test can pin each auth's quota snapshot independently. It honors
// the AuthQuotaSnapshot.AuthID field; any auth with no preloaded
// snapshot returns an empty (no-limit) snapshot.
type multiAuthReader struct {
	readers map[string]*fakeUsageReader
}

func (m *multiAuthReader) GetAuthQuota(ctx context.Context, authID string) (AuthQuotaSnapshot, bool, error) {
	if r, ok := m.readers[authID]; ok {
		return r.GetAuthQuota(ctx, authID)
	}
	return AuthQuotaSnapshot{AuthID: authID}, false, nil
}
