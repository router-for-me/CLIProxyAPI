package auth

import (
	"testing"
	"time"
)

// mpAuth builds a minimal Auth for the pool-model cooldown tests. model may be
// empty for auths that carry no model state at all.
func mpAuth(id, provider string, disabled bool, model string, exceeded bool, retryAfter time.Time) *Auth {
	a := &Auth{ID: id, Provider: provider, Disabled: disabled}
	if model == "" {
		return a
	}
	state := &ModelState{Status: StatusActive}
	if exceeded {
		state.Unavailable = true
		state.NextRetryAfter = retryAfter
		state.Quota = QuotaState{Exceeded: true, NextRecoverAt: retryAfter}
	}
	a.ModelStates = map[string]*ModelState{canonicalModelKey(model): state}
	return a
}

func TestModelPoolCooldownsThresholds(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	model := "claude-opus-4-1"
	until := now.Add(10 * time.Minute)

	cases := []struct {
		name    string
		cooling int
		total   int
		want    bool
	}{
		{"2 of 3 meets the half rule", 2, 3, true},
		{"2 of 4 is half", 2, 4, true},
		{"3 of 4 is above half", 3, 4, true},
		{"1 of 2 is below minimum", 1, 2, false},
		{"2 of 2 is full", 2, 2, true},
		{"1 of 4 is nowhere close", 1, 4, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			agg := newModelPoolCooldowns()
			auths := make([]*Auth, 0, tc.total)
			for i := 0; i < tc.total; i++ {
				cooling := i < tc.cooling
				auths = append(auths, mpAuth(
					"auth-"+string(rune('a'+i)), "claude", false, model, cooling, until))
			}
			agg.record(auths, "claude", model, now)
			got, ok := agg.block([]string{"claude"}, model, now)
			if ok != tc.want {
				t.Fatalf("block() ok = %v, want %v (deadline %v)", ok, tc.want, got)
			}
			if ok && !got.Equal(until) {
				t.Fatalf("block() deadline = %v, want %v", got, until)
			}
		})
	}
}

func TestModelPoolCooldownsDeadlineIsMaxOfContributors(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	model := "claude-opus-4-1"
	short := now.Add(5 * time.Minute)
	long := now.Add(10 * time.Minute)
	auths := []*Auth{
		mpAuth("a", "claude", false, model, true, short),
		mpAuth("b", "claude", false, model, true, long),
		mpAuth("c", "claude", false, model, false, time.Time{}),
	}
	agg := newModelPoolCooldowns()
	agg.record(auths, "claude", model, now)
	deadline, ok := agg.block([]string{"claude"}, model, now.Add(6*time.Minute))
	if !ok {
		t.Fatalf("block() = false, want true (still inside longest contributor)")
	}
	if !deadline.Equal(long) {
		t.Fatalf("deadline = %v, want MAX of contributors %v", deadline, long)
	}
}

func TestModelPoolCooldownsLazyExpiry(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	model := "claude-opus-4-1"
	until := now.Add(10 * time.Minute)
	auths := []*Auth{
		mpAuth("a", "claude", false, model, true, until),
		mpAuth("b", "claude", false, model, true, until),
		mpAuth("c", "claude", false, model, false, time.Time{}),
	}
	agg := newModelPoolCooldowns()
	agg.record(auths, "claude", model, now)

	// At half-life the pool is still blocked.
	if _, ok := agg.block([]string{"claude"}, model, now.Add(5*time.Minute)); !ok {
		t.Fatalf("block() at half-life = false, want true")
	}
	// Past the deadline the read fails and lazily drops the entry.
	if _, ok := agg.block([]string{"claude"}, model, now.Add(11*time.Minute)); ok {
		t.Fatalf("block() after deadline = true, want false")
	}
	agg.mu.Lock()
	remaining := len(agg.entries)
	agg.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("entries after lazy expiry = %d, want 0", remaining)
	}
}

func TestModelPoolCooldownsStaleExceededNotCooling(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	model := "claude-opus-4-1"
	past := now.Add(-5 * time.Minute)

	// 2 of 4 nominal: one contributor carrying a STALE exceeded flag (retry
	// time already passed) must not count as cooling — 1 of 4 is nowhere near
	// the threshold, so no block.
	auths := []*Auth{
		mpAuth("a", "claude", false, model, true, now.Add(10*time.Minute)),
		mpAuth("b", "claude", false, model, true, past), // stale: not cooling
		mpAuth("c", "claude", false, model, false, time.Time{}),
		mpAuth("d", "claude", false, model, false, time.Time{}),
	}
	agg := newModelPoolCooldowns()
	agg.record(auths, "claude", model, now)
	if _, ok := agg.block([]string{"claude"}, model, now); ok {
		t.Fatalf("block() counted a stale exceeded auth, want false")
	}

	// Degenerate variant: BOTH would-be contributors are stale — even a pool
	// where they are the majority stays open.
	majorityStale := []*Auth{
		mpAuth("a", "claude", false, model, true, past),
		mpAuth("b", "claude", false, model, true, past),
		mpAuth("c", "claude", false, model, false, time.Time{}),
	}
	agg.record(majorityStale, "claude", model, now)
	if _, ok := agg.block([]string{"claude"}, model, now); ok {
		t.Fatalf("block() with majority-stale exceeded = true, want false")
	}
}

func TestModelPoolCooldownsRecoveryOnSuccess(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	model := "claude-opus-4-1"
	until := now.Add(10 * time.Minute)
	a := mpAuth("a", "claude", false, model, true, until)
	b := mpAuth("b", "claude", false, model, true, until)
	c := mpAuth("c", "claude", false, model, false, time.Time{})
	d := mpAuth("d", "claude", false, model, false, time.Time{})
	agg := newModelPoolCooldowns()

	agg.record([]*Auth{a, b, c, d}, "claude", model, now)
	if _, ok := agg.block([]string{"claude"}, model, now); !ok {
		t.Fatalf("block() with 2 of 4 cooling = false, want true")
	}

	// Auth "b" succeeds: its model state clears; the caller re-records with the
	// updated slice and the count falls below the threshold.
	bSuccess := mpAuth("b", "claude", false, model, false, time.Time{})
	agg.record([]*Auth{a, bSuccess, c, d}, "claude", model, now)
	if _, ok := agg.block([]string{"claude"}, model, now); ok {
		t.Fatalf("block() after recovery = true, want false")
	}
	agg.mu.Lock()
	remaining := len(agg.entries)
	agg.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("entries after recovery = %d, want 0", remaining)
	}
}

func TestModelPoolCooldownsDisabledExcluded(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	model := "claude-opus-4-1"
	until := now.Add(10 * time.Minute)

	// A cooling auth that is DISABLED must not count in either direction: if
	// it were counted the threshold (cooling >= 2 AND cooling*2 >= total)
	// would be met, but with it excluded the count is 1 of 3 — no block.
	auths := []*Auth{
		mpAuth("a", "claude", false, model, true, until),
		mpAuth("b", "claude", true, model, true, until), // disabled but cooling: ignored
		mpAuth("c", "claude", false, model, false, time.Time{}),
		mpAuth("d", "claude", false, model, false, time.Time{}),
	}
	agg := newModelPoolCooldowns()
	agg.record(auths, "claude", model, now)
	if _, ok := agg.block([]string{"claude"}, model, now); ok {
		t.Fatalf("block() counted a disabled auth, want false")
	}
}

func TestModelPoolCooldownsDropAuth(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	model := "claude-opus-4-1"
	until := now.Add(10 * time.Minute)
	auths := []*Auth{
		mpAuth("a", "claude", false, model, true, until),
		mpAuth("b", "claude", false, model, true, until),
		mpAuth("c", "claude", false, model, false, time.Time{}),
	}
	agg := newModelPoolCooldowns()
	agg.record(auths, "claude", model, now)
	if _, ok := agg.block([]string{"claude"}, model, now); !ok {
		t.Fatalf("block() before drop = false, want true")
	}

	agg.dropAuth("a")
	agg.mu.Lock()
	remaining := len(agg.entries)
	agg.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("entries after dropAuth = %d, want 0 (below the 2-cooling minimum)", remaining)
	}
	if _, ok := agg.block([]string{"claude"}, model, now); ok {
		t.Fatalf("block() after drop = true, want false")
	}
}

func TestModelPoolCooldownsModelIsolation(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	blocked := "claude-opus-4-1"
	other := "claude-sonnet-4-5"
	until := now.Add(10 * time.Minute)
	auths := []*Auth{
		mpAuth("a", "claude", false, blocked, true, until),
		mpAuth("b", "claude", false, blocked, true, until),
		mpAuth("c", "claude", false, other, false, time.Time{}),
		mpAuth("d", "claude", false, other, false, time.Time{}),
	}
	agg := newModelPoolCooldowns()
	agg.record(auths, "claude", blocked, now)
	if _, ok := agg.block([]string{"claude"}, blocked, now); !ok {
		t.Fatalf("block() for blocked model = false, want true")
	}
	if _, ok := agg.block([]string{"claude"}, other, now); ok {
		t.Fatalf("block() for unaffected model = true, want false")
	}
}

func TestModelPoolCooldownsProviderIsolation(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	model := "claude-opus-4-1"
	until := now.Add(10 * time.Minute)
	auths := []*Auth{
		mpAuth("a", "claude", false, model, true, until),
		mpAuth("b", "claude", false, model, true, until),
		mpAuth("c", "codex", false, model, false, time.Time{}),
		mpAuth("d", "codex", false, model, false, time.Time{}),
	}
	agg := newModelPoolCooldowns()
	agg.record(auths, "claude", model, now)
	if _, ok := agg.block([]string{"claude"}, model, now); !ok {
		t.Fatalf("block() for blocked provider = false, want true")
	}
	if _, ok := agg.block([]string{"codex"}, model, now); ok {
		t.Fatalf("block() for unaffected provider = true, want false")
	}
}

func TestModelPoolCooldownsSuffixNormalizationAndEmpty(t *testing.T) {
	now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
	base := "claude-opus-4-1"
	until := now.Add(10 * time.Minute)

	// Thinking-suffix variants must collapse onto the same canonical model key
	// (canonicalModelKey strips a "(...)" thinking suffix).
	suffixed := base + "(8192)"
	auths := []*Auth{
		mpAuth("a", "claude", false, suffixed, true, until),
		mpAuth("b", "claude", false, suffixed, true, until),
	}
	agg := newModelPoolCooldowns()
	agg.record(auths, "claude", suffixed, now)
	if _, ok := agg.block([]string{"claude"}, base, now); !ok {
		t.Fatalf("block(%q) = false, want true (suffix alias of %q)", base, suffixed)
	}

	// Degenerate inputs never panic nor leave stale entries.
	agg.record(nil, "claude", base, now)
	agg.record([]*Auth{nil}, "", base, now)
	if _, ok := agg.block(nil, base, now); ok {
		t.Fatalf("block(nil providers) = true, want false")
	}
	if _, ok := agg.block([]string{"claude"}, "  ", now); ok {
		t.Fatalf("block(blank model) = true, want false")
	}
	if _, ok := agg.block([]string{""}, base+"(8192)", now); ok {
		t.Fatalf("block(blank provider) = true, want false")
	}
}
