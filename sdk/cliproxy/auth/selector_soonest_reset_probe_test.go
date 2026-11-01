package auth

import (
	"context"
	"strconv"
	"sync"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

const soonestResetDay = 24 * time.Hour

// soonestResetClaude is a Claude credential observed at observed whose weekly window resets
// weekly after the observation, with the given 5-hour status.
func soonestResetClaude(id string, observed time.Time, weekly time.Duration, fiveHour string) *Auth {
	return soonestResetAuth(id, "claude", observed, map[string]string{
		"Anthropic-Ratelimit-Unified-7d-Status":      "allowed",
		"Anthropic-Ratelimit-Unified-7d-Utilization": "0.40",
		"Anthropic-Ratelimit-Unified-7d-Reset":       soonestResetEpoch(observed.Add(weekly)),
		"Anthropic-Ratelimit-Unified-5h-Status":      fiveHour,
		"Anthropic-Ratelimit-Unified-5h-Reset":       soonestResetEpoch(observed.Add(time.Hour)),
	})
}

func soonestResetPicks(selector *SoonestResetSelector, now time.Time, n int, auths ...*Auth) []string {
	picks := make([]string, 0, n)
	for i := 0; i < n; i++ {
		picks = append(picks, selector.pick(auths, now).ID)
	}
	return picks
}

func assertSoonestResetPicks(t *testing.T, label string, got []string, want ...string) {
	t.Helper()
	if !equalStrings(got, want) {
		t.Fatalf("%s: picks = %v, want %v", label, got, want)
	}
}

// Accounts with no known reset get one probe request each, then rank by what they
// reported; one that reported nothing comes after the known resets.
func TestSoonestResetSelector_ProbesUnknownOnceThenRanks(t *testing.T) {
	t.Parallel()

	wall := soonestResetTestNow
	selector := &SoonestResetSelector{}
	a, b := &Auth{ID: "a", Provider: "claude"}, &Auth{ID: "b", Provider: "claude"}
	c := soonestResetClaude("c", wall, soonestResetDay, "rejected")
	d := soonestResetClaude("d", wall, 3*soonestResetDay, "allowed")

	assertSoonestResetPicks(t, "one probe each, then the known reset", soonestResetPicks(selector, wall, 4, a, b, c, d), "a", "b", "d", "d")

	// a's probe answered with a reset in 12 hours: it now ranks by it, ahead of d; b's
	// answer reported nothing, so it stays behind the known resets.
	learnedA := soonestResetClaude("a", wall.Add(time.Second), 12*time.Hour, "allowed")
	assertSoonestResetPicks(t, "learned reset", soonestResetPicks(selector, wall, 1, learnedA, b, c, d), "a")
	assertSoonestResetPicks(t, "known before probed", soonestResetPicks(selector, wall, 1, b, c, d), "d")
	assertSoonestResetPicks(t, "probed and unknown, still before used-up", soonestResetPicks(selector, wall, 1, b, c), "b")
	assertSoonestResetPicks(t, "used-up account is the last resort", soonestResetPicks(selector, wall, 1, c), "c")

	// When d's weekly reset passes, it is unknown again and gets a new probe.
	afterD := wall.Add(3*soonestResetDay + time.Second)
	learnedA = soonestResetClaude("a", afterD, 12*time.Hour, "allowed")
	assertSoonestResetPicks(t, "rollover re-arms the probe", soonestResetPicks(selector, afterD, 2, learnedA, b, d), "d", "a")
}

// An account answering with quota signals but no weekly reset is probed once; each new
// answer (a newer observation) must not re-arm the probe.
func TestSoonestResetSelector_AnswersWithoutResetDoNotReArmProbe(t *testing.T) {
	t.Parallel()

	wall := soonestResetTestNow
	selector := &SoonestResetSelector{}
	b := soonestResetClaude("b", wall, 2*soonestResetDay, "allowed")
	var picks []string
	for i := 0; i < 5; i++ {
		a := &Auth{ID: "a", Provider: "claude"}
		if i > 0 {
			seen := wall.Add(time.Duration(i) * time.Second)
			a = soonestResetAuth("a", "claude", seen, map[string]string{"Anthropic-Ratelimit-Unified-5h-Status": "allowed"})
		}
		picks = append(picks, selector.pick([]*Auth{a, b}, wall.Add(time.Duration(i)*time.Second)).ID)
	}
	assertSoonestResetPicks(t, "no re-arm", picks, "a", "b", "b", "b", "b")
}

// A probe that fails before any header arrives (no new observation) is spent.
func TestSoonestResetSelector_ProbeIsSpentWithoutAnswer(t *testing.T) {
	t.Parallel()

	wall := soonestResetTestNow
	selector := &SoonestResetSelector{}
	a := &Auth{ID: "a", Provider: "claude"}
	b := soonestResetClaude("b", wall, 2*soonestResetDay, "allowed")
	assertSoonestResetPicks(t, "probe then known", soonestResetPicks(selector, wall, 3, a, b), "a", "b", "b")
}

// A probed account that resets later than the known one gets its one request, then the
// traffic goes back.
func TestSoonestResetSelector_ProbedLaterResetHandsBack(t *testing.T) {
	t.Parallel()

	wall := soonestResetTestNow
	selector := &SoonestResetSelector{}
	a := soonestResetClaude("a", wall, soonestResetDay, "allowed")
	b := &Auth{ID: "b", Provider: "claude"}
	assertSoonestResetPicks(t, "probe", soonestResetPicks(selector, wall, 1, a, b), "b")
	b = soonestResetClaude("b", wall, 4*soonestResetDay, "allowed")
	assertSoonestResetPicks(t, "back to the sooner reset", soonestResetPicks(selector, wall, 3, a, b), "a", "a", "a")
}

// Codex answers with Reset-After-Seconds: 0 and no Reset-At: the reset is the observation
// time itself, which must not count as a new rollover on every answer.
func TestSoonestResetSelector_ZeroRelativeResetsDoNotReArmProbe(t *testing.T) {
	t.Parallel()

	wall := soonestResetTestNow
	selector := &SoonestResetSelector{}
	b := soonestResetClaude("b", wall, 2*soonestResetDay, "allowed")
	zero := map[string]string{
		"X-Codex-Secondary-Used-Percent":        "0",
		"X-Codex-Secondary-Window-Minutes":      "10080",
		"X-Codex-Secondary-Reset-After-Seconds": "0",
	}
	var picks []string
	for i := 0; i < 4; i++ {
		a := &Auth{ID: "a", Provider: "codex"}
		now := wall
		if i > 0 {
			seen := wall.Add(time.Duration(2*i) * time.Second)
			a = soonestResetAuth("a", "codex", seen, zero)
			now = seen.Add(time.Second)
		}
		picks = append(picks, selector.pick([]*Auth{a, b}, now).ID)
	}
	assertSoonestResetPicks(t, "zero delays", picks, "a", "b", "b", "b")
}

// Relative resets read around a boundary differ by rounding; rollovers less than half a
// window apart are the same one, so they never re-arm the probe twice.
func TestSoonestResetSelector_RolloversWithinGapAreOneRollover(t *testing.T) {
	t.Parallel()

	wall := soonestResetTestNow
	selector := &SoonestResetSelector{}
	b := soonestResetClaude("b", wall, 2*soonestResetDay, "allowed")
	codex := func(seen time.Time, after int) *Auth {
		return soonestResetAuth("a", "codex", seen, map[string]string{
			"X-Codex-Secondary-Window-Minutes":      "10080",
			"X-Codex-Secondary-Reset-After-Seconds": strconv.Itoa(after),
		})
	}
	boundary := wall.Add(600 * time.Second)
	assertSoonestResetPicks(t, "reset reached: probed once", soonestResetPicks(selector, boundary.Add(time.Second), 1, codex(wall, 600), b), "a")
	for i := 1; i < 4; i++ {
		// Lagging answers keep reporting one second left, each from a later observation.
		seen := boundary.Add(time.Duration(i) * time.Second)
		assertSoonestResetPicks(t, "lagging answer "+strconv.Itoa(i), soonestResetPicks(selector, seen.Add(2*time.Second), 1, codex(seen, 1), b), "b")
	}
	// A week later the window really rolls over again: one more probe.
	next := boundary.Add(7 * soonestResetDay)
	assertSoonestResetPicks(t, "next rollover", soonestResetPicks(selector, next.Add(time.Second), 2, codex(next.Add(-5*time.Second), 5), b), "a", "b")
}

// A probe made for one window's rollover must not suppress a later rollover of another.
func TestSoonestResetSelector_ProbeMarkersArePerWindow(t *testing.T) {
	t.Parallel()

	wall := soonestResetTestNow
	selector := &SoonestResetSelector{}
	b := soonestResetClaude("b", wall, 5*soonestResetDay, "allowed")
	r := wall.Add(600 * time.Second)
	// Only Secondary-Reset-At and no lengths: secondary is taken as weekly.
	partial := soonestResetAuth("a", "codex", wall, map[string]string{"X-Codex-Secondary-Reset-At": soonestResetEpoch(r)})
	// The probe's answer is complete: the weekly window is primary, resetting a day after
	// r; secondary is the 5-hour window.
	complete := soonestResetAuth("a", "codex", r.Add(time.Second), map[string]string{
		"X-Codex-Primary-Used-Percent":     "10",
		"X-Codex-Primary-Window-Minutes":   "10080",
		"X-Codex-Primary-Reset-At":         soonestResetEpoch(r.Add(soonestResetDay)),
		"X-Codex-Secondary-Used-Percent":   "10",
		"X-Codex-Secondary-Window-Minutes": "300",
		"X-Codex-Secondary-Reset-At":       soonestResetEpoch(r.Add(5 * time.Hour)),
	})

	if w := observedQuotaWindows("codex", partial.Quota.Signals, partial.Quota.ObservedAt, r.Add(time.Second)); w.rolledOver == nil || w.rolledOver.window != quotaWindowCodexSecondary {
		t.Fatalf("partial rollover = %+v, want codex secondary", w.rolledOver)
	}
	assertSoonestResetPicks(t, "secondary rolled over", soonestResetPicks(selector, r.Add(time.Second), 1, partial, b), "a")
	assertSoonestResetPicks(t, "primary resets sooner than b", soonestResetPicks(selector, r.Add(2*time.Second), 1, complete, b), "a")
	if w := observedQuotaWindows("codex", complete.Quota.Signals, complete.Quota.ObservedAt, r.Add(soonestResetDay+time.Second)); w.rolledOver == nil || w.rolledOver.window != quotaWindowCodexPrimary {
		t.Fatalf("complete rollover = %+v, want codex primary", w.rolledOver)
	}
	// Just after r + 1 day, primary rolls over: one probe, not suppressed by the
	// secondary's marker r (r + 1 day is within half of primary's week of r).
	assertSoonestResetPicks(t, "primary rolled over", soonestResetPicks(selector, r.Add(soonestResetDay+time.Second), 2, complete, b), "a", "b")
}

func TestObservedQuotaWindowsRollover(t *testing.T) {
	t.Parallel()

	now := soonestResetTestNow
	in := func(d time.Duration) string { return soonestResetEpoch(now.Add(d)) }
	tests := []struct {
		name       string
		provider   string
		signals    map[string]string
		observedAt time.Time
		want       *quotaRollover
	}{
		{
			name:       "claude weekly reset passed",
			provider:   "claude",
			signals:    map[string]string{"Anthropic-Ratelimit-Unified-7d-Reset": in(-time.Minute)},
			observedAt: now.Add(-time.Hour),
			want:       &quotaRollover{window: quotaWindowClaude7d, at: now.Add(-time.Minute), gap: 84 * time.Hour},
		},
		{
			name:       "claude weekly reset ahead",
			provider:   "claude",
			signals:    map[string]string{"Anthropic-Ratelimit-Unified-7d-Reset": in(time.Minute)},
			observedAt: now,
		},
		{
			name:       "claude five-hour reset passed is not a weekly rollover",
			provider:   "claude",
			signals:    map[string]string{"Anthropic-Ratelimit-Unified-5h-Reset": in(-time.Minute)},
			observedAt: now.Add(-time.Hour),
		},
		{
			name:     "codex relative weekly reset passed",
			provider: "codex",
			signals: map[string]string{
				"X-Codex-Secondary-Window-Minutes":      "10080",
				"X-Codex-Secondary-Reset-After-Seconds": "60",
			},
			observedAt: now.Add(-2 * time.Minute),
			want:       &quotaRollover{window: quotaWindowCodexSecondary, at: now.Add(-time.Minute), gap: 84 * time.Hour},
		},
		{
			name:     "codex zero delay names no rollover",
			provider: "codex",
			signals: map[string]string{
				"X-Codex-Secondary-Window-Minutes":      "10080",
				"X-Codex-Secondary-Reset-After-Seconds": "0",
			},
			observedAt: now.Add(-time.Minute),
		},
		{
			name:       "codex weekly window without length uses half the fallback window",
			provider:   "codex",
			signals:    map[string]string{"X-Codex-Secondary-Reset-At": in(-time.Second)},
			observedAt: now.Add(-time.Hour),
			want:       &quotaRollover{window: quotaWindowCodexSecondary, at: now.Add(-time.Second), gap: 150 * time.Minute},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := observedQuotaWindows(tt.provider, tt.signals, tt.observedAt, now).rolledOver
			switch {
			case tt.want == nil && got == nil:
			case tt.want == nil || got == nil || got.window != tt.want.window || !got.at.Equal(tt.want.at) || got.gap != tt.want.gap:
				t.Fatalf("rolledOver = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestSoonestResetProbed(t *testing.T) {
	t.Parallel()

	at := soonestResetTestNow
	gap := 84 * time.Hour
	rollover := func(window quotaWindowID, when time.Time) *quotaRollover {
		return &quotaRollover{window: window, at: when, gap: gap}
	}
	tests := []struct {
		name      string
		record    map[quotaWindowID]time.Time
		hasRecord bool
		rollover  *quotaRollover
		want      bool
	}{
		{name: "never probed", want: false},
		{name: "never probed with rollover", rollover: rollover(quotaWindowClaude7d, at), want: false},
		{name: "probed, no rollover since", record: map[quotaWindowID]time.Time{}, hasRecord: true, want: true},
		{name: "probed before any rollover, then rolled over", record: map[quotaWindowID]time.Time{}, hasRecord: true, rollover: rollover(quotaWindowClaude7d, at), want: false},
		{name: "probed for this rollover", record: map[quotaWindowID]time.Time{quotaWindowClaude7d: at}, hasRecord: true, rollover: rollover(quotaWindowClaude7d, at), want: true},
		{name: "rollover within gap", record: map[quotaWindowID]time.Time{quotaWindowClaude7d: at}, hasRecord: true, rollover: rollover(quotaWindowClaude7d, at.Add(gap)), want: true},
		{name: "rollover beyond gap", record: map[quotaWindowID]time.Time{quotaWindowClaude7d: at}, hasRecord: true, rollover: rollover(quotaWindowClaude7d, at.Add(gap+time.Second)), want: false},
		{name: "marker of another window", record: map[quotaWindowID]time.Time{quotaWindowCodexSecondary: at}, hasRecord: true, rollover: rollover(quotaWindowCodexPrimary, at), want: false},
	}
	for _, tt := range tests {
		if got := soonestResetProbed(tt.record, tt.hasRecord, tt.rollover); got != tt.want {
			t.Errorf("%s: soonestResetProbed() = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// Concurrent picks over fresh credentials reserve exactly one probe per credential.
func TestSoonestResetSelectorPick_ConcurrentProbesOncePerCredential(t *testing.T) {
	t.Parallel()

	selector := &SoonestResetSelector{}
	auths := []*Auth{
		{ID: "a", Provider: "claude"},
		{ID: "b", Provider: "claude"},
		{ID: "c", Provider: "codex"},
	}
	const goroutines = 50
	var mu sync.Mutex
	counts := make(map[string]int)
	start := make(chan struct{})
	var wg sync.WaitGroup
	errCh := make(chan error, goroutines)
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			picked, err := selector.Pick(context.Background(), "mixed", "", cliproxyexecutor.Options{}, auths)
			if err != nil {
				errCh <- err
				return
			}
			mu.Lock()
			counts[picked.ID]++
			mu.Unlock()
		}()
	}
	close(start)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("Pick() error = %v", err)
	}
	// Each credential got its one probe; once all were spent, the rest fill first.
	if counts["a"] != goroutines-2 || counts["b"] != 1 || counts["c"] != 1 {
		t.Fatalf("pick counts = %v, want a=%d b=1 c=1", counts, goroutines-2)
	}
	selector.mu.Lock()
	defer selector.mu.Unlock()
	if len(selector.probes) != 3 {
		t.Fatalf("probe records = %d, want 3", len(selector.probes))
	}
}

func TestSoonestResetSelector_InvalidateAuthReArmsProbe(t *testing.T) {
	t.Parallel()

	wall := soonestResetTestNow
	selector := &SoonestResetSelector{}
	a := &Auth{ID: "a", Provider: "claude"}
	b := soonestResetClaude("b", wall, soonestResetDay, "allowed")
	assertSoonestResetPicks(t, "probe then known", soonestResetPicks(selector, wall, 2, a, b), "a", "b")
	selector.InvalidateAuth("a")
	assertSoonestResetPicks(t, "re-added credential is probed again", soonestResetPicks(selector, wall, 2, a, b), "a", "b")

	affinity := NewSessionAffinitySelector(selector)
	t.Cleanup(affinity.Stop)
	affinity.InvalidateAuth("a")
	selector.mu.Lock()
	_, kept := selector.probes["a"]
	selector.mu.Unlock()
	if kept {
		t.Fatal("session affinity did not forward InvalidateAuth to its soonest-reset fallback")
	}
}

func TestSoonestResetSelector_ProbeRecordIsBounded(t *testing.T) {
	t.Parallel()

	selector := &SoonestResetSelector{}
	for i := 0; i < maxSoonestResetProbeEntries+10; i++ {
		selector.pick([]*Auth{{ID: "auth-" + strconv.Itoa(i), Provider: "claude"}}, soonestResetTestNow)
	}
	selector.mu.Lock()
	defer selector.mu.Unlock()
	if len(selector.probes) > maxSoonestResetProbeEntries {
		t.Fatalf("probe records = %d, want at most %d", len(selector.probes), maxSoonestResetProbeEntries)
	}
}
