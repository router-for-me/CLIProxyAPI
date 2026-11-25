package auth

import (
	"context"
	"math"
	"strconv"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func claudeQuotaAuth(id string, now time.Time, sessionLeft float64, sessionReset time.Duration, weeklyLeft float64, weeklyReset time.Duration) *Auth {
	return &Auth{
		ID:       id,
		Provider: "claude",
		Quota: QuotaState{
			ObservedAt: now,
			Signals: map[string]string{
				"Anthropic-Ratelimit-Unified-5h-Utilization": strconv.FormatFloat(1-sessionLeft, 'f', 4, 64),
				"Anthropic-Ratelimit-Unified-5h-Reset":       strconv.FormatInt(now.Add(sessionReset).Unix(), 10),
				"Anthropic-Ratelimit-Unified-7d-Utilization": strconv.FormatFloat(1-weeklyLeft, 'f', 4, 64),
				"Anthropic-Ratelimit-Unified-7d-Reset":       strconv.FormatInt(now.Add(weeklyReset).Unix(), 10),
			},
		},
	}
}

func pickCounts(t *testing.T, selector Selector, auths []*Auth, picks int) map[string]int {
	t.Helper()
	counts := make(map[string]int)
	for i := 0; i < picks; i++ {
		picked, err := selector.Pick(context.Background(), "claude", "claude-opus-5-5", cliproxyexecutor.Options{}, auths)
		if err != nil {
			t.Fatalf("Pick() error = %v", err)
		}
		counts[picked.ID]++
	}
	return counts
}

// The motivating case: even draining would exhaust MG and AM weekly first and
// leave EG bottlenecked on its 5h window, so EG must receive the largest share.
func TestQuotaAwareSelectorFavorsMostWeeklyPace(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	auths := []*Auth{
		claudeQuotaAuth("am", now, 0.98, 5*time.Hour, 0.13, 42*time.Hour),
		claudeQuotaAuth("eg", now, 0.92, 4*time.Hour, 0.47, 86*time.Hour),
		claudeQuotaAuth("mg", now, 0.76, 3*time.Hour, 0.18, 50*time.Hour),
	}
	selector := &QuotaAwareSelector{nowFunc: func() time.Time { return now }}

	weights := quotaAwareWeights(auths, now)
	total := weights["am"] + weights["eg"] + weights["mg"]
	picks := 1000
	counts := pickCounts(t, selector, auths, picks)
	for _, id := range []string{"am", "eg", "mg"} {
		want := float64(picks) * weights[id] / total
		if math.Abs(float64(counts[id])-want) > 2 {
			t.Fatalf("auth %s picked %d times, want about %.0f (counts=%v)", id, counts[id], want, counts)
		}
	}
	if counts["eg"] <= counts["mg"] || counts["mg"] <= counts["am"] {
		t.Fatalf("want eg > mg > am, got %v", counts)
	}
}

func TestQuotaAwareSelectorSoonResettingWeeklyGetsMoreShare(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	// Less weekly left, but it resets in 2h: unused quota is lost at reset.
	soon := claudeQuotaAuth("soon", now, 1, 5*time.Hour, 0.13, 2*time.Hour)
	later := claudeQuotaAuth("later", now, 1, 5*time.Hour, 0.47, 86*time.Hour)
	weights := quotaAwareWeights([]*Auth{soon, later}, now)
	if weights["soon"] <= weights["later"] {
		t.Fatalf("soon-resetting weight %.4f should exceed %.4f", weights["soon"], weights["later"])
	}
}

func TestQuotaAwareSelectorTapersExhaustedSessionWindow(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	healthy := claudeQuotaAuth("healthy", now, 0.5, 3*time.Hour, 0.5, 72*time.Hour)
	tapered := claudeQuotaAuth("tapered", now, 0.03, 3*time.Hour, 0.5, 72*time.Hour)
	empty := claudeQuotaAuth("empty", now, 0, 3*time.Hour, 0.5, 72*time.Hour)
	weights := quotaAwareWeights([]*Auth{healthy, tapered, empty}, now)
	if got, want := weights["tapered"]/weights["healthy"], 0.03/quotaSessionTaperBelow; math.Abs(got-want) > 1e-9 {
		t.Fatalf("tapered/healthy ratio = %.4f, want %.4f", got, want)
	}
	if weights["empty"] != 0 {
		t.Fatalf("exhausted session weight = %.4f, want 0", weights["empty"])
	}
}

func TestQuotaAwareSelectorUnknownAndStaleSnapshotsGetMeanWeight(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	a := claudeQuotaAuth("a", now, 1, 5*time.Hour, 0.2, 50*time.Hour)
	b := claudeQuotaAuth("b", now, 1, 5*time.Hour, 0.6, 50*time.Hour)
	stale := claudeQuotaAuth("stale", now.Add(-quotaObservationMaxAge-time.Minute), 1, 5*time.Hour, 0.01, 200*time.Hour)
	unknown := &Auth{ID: "unknown", Provider: "claude"}
	weights := quotaAwareWeights([]*Auth{a, b, stale, unknown}, now)
	mean := (weights["a"] + weights["b"]) / 2
	if weights["stale"] != mean || weights["unknown"] != mean {
		t.Fatalf("stale=%.5f unknown=%.5f, want mean %.5f", weights["stale"], weights["unknown"], mean)
	}
}

func TestQuotaAwareSelectorWithoutSnapshotsSplitsEvenly(t *testing.T) {
	auths := []*Auth{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	counts := pickCounts(t, &QuotaAwareSelector{}, auths, 9)
	for _, auth := range auths {
		if counts[auth.ID] != 3 {
			t.Fatalf("counts = %v, want 3 each", counts)
		}
	}
}

func TestQuotaWindowsTreatPassedResetAsFresh(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	auth := claudeQuotaAuth("a", now.Add(-2*time.Hour), 0.1, time.Hour, 0.3, 100*time.Hour)
	short, long, ok := quotaWindowsFromSignals(auth, now)
	if !ok || short == nil || long == nil {
		t.Fatalf("quotaWindowsFromSignals ok=%v short=%v long=%v", ok, short, long)
	}
	if short.remaining != 1 || !short.resetAt.Equal(now.Add(5*time.Hour)) {
		t.Fatalf("short window = %+v, want fresh 5h window", *short)
	}
	if math.Abs(long.remaining-0.3) > 1e-9 {
		t.Fatalf("long remaining = %.4f, want 0.3", long.remaining)
	}
}

func TestQuotaWindowsParseCodexSignalsByWindowLength(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	observedAt := now.Add(-10 * time.Minute)
	auth := &Auth{
		ID:       "codex",
		Provider: "codex",
		Quota: QuotaState{
			ObservedAt: observedAt,
			Signals: map[string]string{
				// Weekly reported as primary: windows are ordered by length, not name.
				"X-Codex-Primary-Used-Percent":          "60",
				"X-Codex-Primary-Window-Minutes":        "10080",
				"X-Codex-Primary-Reset-At":              strconv.FormatInt(now.Add(48*time.Hour).Unix(), 10),
				"X-Codex-Secondary-Used-Percent":        "25",
				"X-Codex-Secondary-Window-Minutes":      "300",
				"X-Codex-Secondary-Reset-After-Seconds": "3600",
			},
		},
	}
	short, long, ok := quotaWindowsFromSignals(auth, now)
	if !ok || short == nil {
		t.Fatalf("quotaWindowsFromSignals ok=%v short=%v", ok, short)
	}
	if short.length != 5*time.Hour || math.Abs(short.remaining-0.75) > 1e-9 || !short.resetAt.Equal(observedAt.Add(time.Hour)) {
		t.Fatalf("short window = %+v", *short)
	}
	if long.length != 7*24*time.Hour || math.Abs(long.remaining-0.4) > 1e-9 {
		t.Fatalf("long window = %+v", *long)
	}
}

func TestQuotaAwareSelectorSkipsCooldownCredentials(t *testing.T) {
	now := time.Now()
	best := claudeQuotaAuth("best", now, 1, 5*time.Hour, 0.9, 10*time.Hour)
	best.Unavailable = true
	best.NextRetryAfter = now.Add(time.Hour)
	other := claudeQuotaAuth("other", now, 1, 5*time.Hour, 0.1, 100*time.Hour)
	counts := pickCounts(t, &QuotaAwareSelector{}, []*Auth{best, other}, 5)
	if counts["other"] != 5 {
		t.Fatalf("counts = %v, want all picks on available credential", counts)
	}
}
