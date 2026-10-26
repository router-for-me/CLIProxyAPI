package auth

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

var soonestResetTestNow = time.Date(2026, time.October, 25, 12, 0, 0, 0, time.UTC)

func epochSignal(t time.Time) string {
	return strconv.FormatInt(t.Unix(), 10)
}

// claudeSubscription builds a Claude OAuth credential observed at observedAt with signals.
func claudeSubscription(id string, observedAt time.Time, signals map[string]string) *Auth {
	return &Auth{
		ID:       id,
		Provider: "claude",
		Metadata: map[string]any{"access_token": "token-" + id},
		Quota:    QuotaState{ObservedAt: observedAt, Signals: signals},
	}
}

// claudeWeeklySignals is a healthy unified snapshot taken at now with the given weekly reset.
func claudeWeeklySignals(now, reset time.Time) map[string]string {
	return map[string]string{
		"Anthropic-Ratelimit-Unified-5h-Status":      "allowed",
		"Anthropic-Ratelimit-Unified-5h-Utilization": "0.1",
		"Anthropic-Ratelimit-Unified-5h-Reset":       epochSignal(now.Add(2 * time.Hour)),
		"Anthropic-Ratelimit-Unified-7d-Status":      "allowed",
		"Anthropic-Ratelimit-Unified-7d-Utilization": "0.4",
		"Anthropic-Ratelimit-Unified-7d-Reset":       epochSignal(reset),
	}
}

func codexSubscription(id string, observedAt time.Time, signals map[string]string) *Auth {
	return &Auth{
		ID:       id,
		Provider: "codex",
		Metadata: map[string]any{"access_token": "token-" + id},
		Quota:    QuotaState{ObservedAt: observedAt, Signals: signals},
	}
}

type soonestResetRankCase struct {
	name      string
	auth      *Auth
	wantClass int
	wantReset time.Time
	exhausted bool
}

func checkSoonestResetRanks(t *testing.T, now time.Time, tests []soonestResetRankCase) {
	t.Helper()
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			rank := soonestResetFactsFor(test.auth).rank(now, false)
			if rank.class != test.wantClass {
				t.Fatalf("class = %d, want %d", rank.class, test.wantClass)
			}
			if !rank.weeklyReset.Equal(test.wantReset) {
				t.Fatalf("weekly reset = %v, want %v", rank.weeklyReset, test.wantReset)
			}
			if rank.exhausted != test.exhausted {
				t.Fatalf("exhausted = %v, want %v", rank.exhausted, test.exhausted)
			}
		})
	}
}

func TestSoonestResetRankClaude(t *testing.T) {
	t.Parallel()

	now := soonestResetTestNow
	observed := now.Add(-time.Minute)
	checkSoonestResetRanks(t, now, []soonestResetRankCase{
		{
			name:      "known weekly reset",
			auth:      claudeSubscription("a", observed, claudeWeeklySignals(now, now.Add(48*time.Hour))),
			wantClass: soonestResetClassKnown,
			wantReset: now.Add(48 * time.Hour),
		},
		{
			name:      "never observed subscription is probed",
			auth:      claudeSubscription("a", time.Time{}, nil),
			wantClass: soonestResetClassProbe,
		},
		{
			name:      "rolled over weekly window is probed again",
			auth:      claudeSubscription("a", now.Add(-8*24*time.Hour), claudeWeeklySignals(now.Add(-8*24*time.Hour), now.Add(-time.Hour))),
			wantClass: soonestResetClassProbe,
		},
		{
			// A per-minute 429 replaces the snapshot without any usage window (Issue #6720 shape).
			name: "rate limit snapshot without usage windows is probed",
			auth: claudeSubscription("a", observed, map[string]string{
				"Retry-After":                       "30",
				"Anthropic-Ratelimit-Unified-Reset": epochSignal(now.Add(time.Minute)),
			}),
			wantClass: soonestResetClassProbe,
		},
		{
			// An overage rejection can report the weekly window without its reset (Issue #5915 shape).
			name: "weekly window without a reset is probed",
			auth: claudeSubscription("a", observed, map[string]string{
				"Anthropic-Ratelimit-Unified-7d-Status":      "allowed",
				"Anthropic-Ratelimit-Unified-7d-Utilization": "0.69",
			}),
			wantClass: soonestResetClassProbe,
		},
		{
			name: "plan reporting only a five hour window is a subscription without weekly reset",
			auth: claudeSubscription("a", observed, map[string]string{
				"Anthropic-Ratelimit-Unified-5h-Utilization": "0.2",
				"Anthropic-Ratelimit-Unified-5h-Reset":       epochSignal(now.Add(time.Hour)),
			}),
			wantClass: soonestResetClassSubscription,
		},
		{
			name: "api key never probes",
			auth: &Auth{
				ID:         "a",
				Provider:   "claude",
				Attributes: map[string]string{AttributeAPIKey: "sk-ant-api03-test"},
			},
			wantClass: soonestResetClassOther,
		},
		{
			name: "setup token configured as api key is a subscription",
			auth: &Auth{
				ID:         "a",
				Provider:   "claude",
				Attributes: map[string]string{AttributeAPIKey: "sk-ant-oat01-test", AttributeAuthKind: AuthKindAPIKey},
			},
			wantClass: soonestResetClassProbe,
		},
		{
			name: "rejected weekly window is exhausted",
			auth: claudeSubscription("a", observed, map[string]string{
				"Anthropic-Ratelimit-Unified-7d-Status": "rejected",
				"Anthropic-Ratelimit-Unified-7d-Reset":  epochSignal(now.Add(24 * time.Hour)),
			}),
			wantClass: soonestResetClassKnown,
			wantReset: now.Add(24 * time.Hour),
			exhausted: true,
		},
		{
			name: "rejected weekly window without a reset stays exhausted for its length",
			auth: claudeSubscription("a", now.Add(-24*time.Hour), map[string]string{
				"Anthropic-Ratelimit-Unified-7d-Status": "rejected",
			}),
			wantClass: soonestResetClassProbe,
			exhausted: true,
		},
		{
			name: "used up five hour window is exhausted until its reset",
			auth: claudeSubscription("a", observed, map[string]string{
				"Anthropic-Ratelimit-Unified-5h-Utilization": "1.0",
				"Anthropic-Ratelimit-Unified-5h-Reset":       epochSignal(now.Add(time.Hour)),
				"Anthropic-Ratelimit-Unified-7d-Reset":       epochSignal(now.Add(24 * time.Hour)),
			}),
			wantClass: soonestResetClassKnown,
			wantReset: now.Add(24 * time.Hour),
			exhausted: true,
		},
		{
			// Observed 2h ago, so observation plus the 5h length is still ahead: only the
			// stated reset, which passed, makes the window usable again.
			name: "used up window whose stated reset passed is usable",
			auth: claudeSubscription("a", now.Add(-2*time.Hour), map[string]string{
				"Anthropic-Ratelimit-Unified-5h-Utilization": "1.0",
				"Anthropic-Ratelimit-Unified-5h-Reset":       epochSignal(now.Add(-time.Hour)),
				"Anthropic-Ratelimit-Unified-7d-Reset":       epochSignal(now.Add(24 * time.Hour)),
			}),
			wantClass: soonestResetClassKnown,
			wantReset: now.Add(24 * time.Hour),
		},
	})
}

func TestSoonestResetRankCodexFindsWeeklyWindowByLength(t *testing.T) {
	t.Parallel()

	now := soonestResetTestNow
	observed := now.Add(-time.Minute)
	codex := func(signals map[string]string) *Auth { return codexSubscription("codex-a", observed, signals) }
	checkSoonestResetRanks(t, now, []soonestResetRankCase{
		{
			name: "pro plan carries the weekly window in primary",
			auth: codex(map[string]string{
				"X-Codex-Primary-Used-Percent":   "51",
				"X-Codex-Primary-Window-Minutes": "10080",
				"X-Codex-Primary-Reset-At":       epochSignal(now.Add(72 * time.Hour)),
			}),
			wantClass: soonestResetClassKnown,
			wantReset: now.Add(72 * time.Hour),
		},
		{
			name: "zero filled secondary placeholder does not hide the weekly primary",
			auth: codex(map[string]string{
				"X-Codex-Primary-Used-Percent":          "20",
				"X-Codex-Primary-Window-Minutes":        "10080",
				"X-Codex-Primary-Reset-At":              epochSignal(now.Add(24 * time.Hour)),
				"X-Codex-Secondary-Used-Percent":        "0",
				"X-Codex-Secondary-Window-Minutes":      "0",
				"X-Codex-Secondary-Reset-After-Seconds": "0",
			}),
			wantClass: soonestResetClassKnown,
			wantReset: now.Add(24 * time.Hour),
		},
		{
			name: "zero filled secondary placeholder beside a short primary is no weekly window",
			auth: codex(map[string]string{
				"X-Codex-Primary-Used-Percent":          "10",
				"X-Codex-Primary-Window-Minutes":        "300",
				"X-Codex-Primary-Reset-At":              epochSignal(now.Add(time.Hour)),
				"X-Codex-Secondary-Used-Percent":        "0",
				"X-Codex-Secondary-Window-Minutes":      "0",
				"X-Codex-Secondary-Reset-After-Seconds": "0",
			}),
			wantClass: soonestResetClassSubscription,
		},
		{
			name: "weekly primary beats a secondary without a length",
			auth: codex(map[string]string{
				"X-Codex-Primary-Window-Minutes": "10080",
				"X-Codex-Primary-Reset-At":       epochSignal(now.Add(24 * time.Hour)),
				"X-Codex-Secondary-Used-Percent": "5",
				"X-Codex-Secondary-Reset-At":     epochSignal(now.Add(2 * time.Hour)),
			}),
			wantClass: soonestResetClassKnown,
			wantReset: now.Add(24 * time.Hour),
		},
		{
			name: "weekly window in secondary, relative reset",
			auth: codex(map[string]string{
				"X-Codex-Primary-Used-Percent":          "10",
				"X-Codex-Primary-Window-Minutes":        "300",
				"X-Codex-Primary-Reset-At":              epochSignal(now.Add(time.Hour)),
				"X-Codex-Secondary-Used-Percent":        "20",
				"X-Codex-Secondary-Window-Minutes":      "10080",
				"X-Codex-Secondary-Reset-After-Seconds": "7200",
			}),
			wantClass: soonestResetClassKnown,
			wantReset: observed.Add(2 * time.Hour),
		},
		{
			name: "additional limit namespaces are ignored",
			auth: codex(map[string]string{
				"X-Codex-Primary-Used-Percent":               "10",
				"X-Codex-Primary-Window-Minutes":             "300",
				"X-Codex-Primary-Reset-At":                   epochSignal(now.Add(time.Hour)),
				"X-Codex-Bengalfox-Secondary-Window-Minutes": "10080",
				"X-Codex-Bengalfox-Secondary-Reset-At":       epochSignal(now.Add(24 * time.Hour)),
			}),
			wantClass: soonestResetClassSubscription,
		},
		{
			name: "without lengths secondary is weekly",
			auth: codex(map[string]string{
				"X-Codex-Primary-Reset-At":   epochSignal(now.Add(time.Hour)),
				"X-Codex-Secondary-Reset-At": epochSignal(now.Add(96 * time.Hour)),
			}),
			wantClass: soonestResetClassKnown,
			wantReset: now.Add(96 * time.Hour),
		},
		{
			name: "lone secondary without a length is weekly",
			auth: codex(map[string]string{
				"X-Codex-Secondary-Reset-At": epochSignal(now.Add(30 * time.Hour)),
			}),
			wantClass: soonestResetClassKnown,
			wantReset: now.Add(30 * time.Hour),
		},
		{
			name: "lone primary without a length is the short window",
			auth: codex(map[string]string{
				"X-Codex-Primary-Used-Percent": "15",
				"X-Codex-Primary-Reset-At":     epochSignal(now.Add(30 * time.Hour)),
			}),
			wantClass: soonestResetClassSubscription,
		},
		{
			name: "two short windows have no weekly window",
			auth: codex(map[string]string{
				"X-Codex-Primary-Window-Minutes":   "60",
				"X-Codex-Primary-Reset-At":         epochSignal(now.Add(30 * time.Minute)),
				"X-Codex-Secondary-Window-Minutes": "300",
				"X-Codex-Secondary-Reset-At":       epochSignal(now.Add(4 * time.Hour)),
			}),
			wantClass: soonestResetClassSubscription,
		},
		{
			name: "used up short window is exhausted",
			auth: codex(map[string]string{
				"X-Codex-Primary-Used-Percent":     "100",
				"X-Codex-Primary-Window-Minutes":   "300",
				"X-Codex-Primary-Reset-At":         epochSignal(now.Add(time.Hour)),
				"X-Codex-Secondary-Used-Percent":   "60",
				"X-Codex-Secondary-Window-Minutes": "10080",
				"X-Codex-Secondary-Reset-At":       epochSignal(now.Add(24 * time.Hour)),
			}),
			wantClass: soonestResetClassKnown,
			wantReset: now.Add(24 * time.Hour),
			exhausted: true,
		},
		{
			name: "used up window with a zero delay ends at the observation",
			auth: codex(map[string]string{
				"X-Codex-Primary-Used-Percent":        "100",
				"X-Codex-Primary-Window-Minutes":      "300",
				"X-Codex-Primary-Reset-After-Seconds": "0",
				"X-Codex-Secondary-Window-Minutes":    "10080",
				"X-Codex-Secondary-Reset-At":          epochSignal(now.Add(24 * time.Hour)),
			}),
			wantClass: soonestResetClassKnown,
			wantReset: now.Add(24 * time.Hour),
		},
		{
			name: "rolled over weekly window is probed again",
			auth: codex(map[string]string{
				"X-Codex-Primary-Window-Minutes": "10080",
				"X-Codex-Primary-Reset-At":       epochSignal(now.Add(-time.Minute)),
			}),
			wantClass: soonestResetClassProbe,
		},
		{
			name: "codex api key never probes",
			auth: &Auth{
				ID:         "codex-key",
				Provider:   "codex",
				Attributes: map[string]string{AttributeAPIKey: "sk-test"},
			},
			wantClass: soonestResetClassOther,
		},
	})
}

func TestSoonestResetRankDevinUsesPolledWeeklyReset(t *testing.T) {
	t.Parallel()

	now := soonestResetTestNow
	reset := now.Add(36 * time.Hour)
	auth := &Auth{
		ID:       "devin-a",
		Provider: "devin",
		Quota: QuotaState{ObservedAt: now.Add(-time.Minute), Signals: map[string]string{
			"weekly_quota_remaining_percent": "40%",
			"weekly_quota_reset_at":          reset.Format(time.RFC3339),
			"daily_quota_remaining_percent":  "0%",
			"daily_quota_reset_at":           now.Add(3 * time.Hour).Format(time.RFC3339),
		}},
	}
	rank := soonestResetFactsFor(auth).rank(now, false)
	if rank.class != soonestResetClassKnown || !rank.weeklyReset.Equal(reset) {
		t.Fatalf("rank = %+v, want known reset %v", rank, reset)
	}
	if !rank.exhausted {
		t.Fatal("exhausted = false, want true while the daily quota is used up")
	}
	if rank := soonestResetFactsFor(&Auth{ID: "devin-b", Provider: "devin"}).rank(now, false); rank.class != soonestResetClassOther {
		t.Fatalf("unobserved devin class = %d, want other (its quota is polled, not probed)", rank.class)
	}
}

func TestParseQuotaResetTime(t *testing.T) {
	t.Parallel()

	for raw, want := range map[string]time.Time{
		"1787695200":           time.Unix(1787695200, 0),
		"1787695200.5":         time.Unix(1787695200, int64(500*time.Millisecond)),
		"2026-10-26T12:00:00Z": time.Date(2026, time.October, 26, 12, 0, 0, 0, time.UTC),
	} {
		got, ok := parseQuotaResetTime(raw)
		if !ok || !got.Equal(want) {
			t.Fatalf("parseQuotaResetTime(%q) = %v, %v; want %v", raw, got, ok, want)
		}
	}
	for _, raw := range []string{"", "soon", "NaN", "-5", "0", "1e300"} {
		if got, ok := parseQuotaResetTime(raw); ok {
			t.Fatalf("parseQuotaResetTime(%q) = %v, want rejection", raw, got)
		}
	}
}

func TestPickSoonestResetAuthOrdering(t *testing.T) {
	t.Parallel()

	now := soonestResetTestNow
	observed := now.Add(-time.Minute)
	known3d := claudeSubscription("known-3d", observed, claudeWeeklySignals(now, now.Add(72*time.Hour)))
	known1d := claudeSubscription("known-1d", observed, claudeWeeklySignals(now, now.Add(24*time.Hour)))
	exhausted := claudeSubscription("exhausted", observed, map[string]string{
		"Anthropic-Ratelimit-Unified-7d-Status": "rejected",
		"Anthropic-Ratelimit-Unified-7d-Reset":  epochSignal(now.Add(time.Hour)),
	})
	probe := claudeSubscription("probe", time.Time{}, nil)
	noWeekly := claudeSubscription("no-weekly", observed, map[string]string{
		"Anthropic-Ratelimit-Unified-5h-Reset": epochSignal(now.Add(time.Hour)),
	})
	apiKey := &Auth{ID: "a-api-key", Provider: "claude", Attributes: map[string]string{AttributeAPIKey: "sk-ant-api03"}}

	steps := []struct {
		auths []*Auth
		want  string
	}{
		{auths: []*Auth{apiKey, noWeekly, exhausted, known3d, known1d, probe}, want: "probe"},
		{auths: []*Auth{apiKey, noWeekly, exhausted, known3d, known1d}, want: "known-1d"},
		{auths: []*Auth{apiKey, noWeekly, exhausted, known3d}, want: "known-3d"},
		{auths: []*Auth{apiKey, noWeekly, exhausted}, want: "no-weekly"},
		{auths: []*Auth{apiKey, exhausted}, want: "a-api-key"},
		{auths: []*Auth{exhausted}, want: "exhausted"},
	}
	for index, step := range steps {
		if got := pickSoonestResetAuth(step.auths, now, nil, nil, true); got == nil || got.ID != step.want {
			t.Fatalf("step %d picked %v, want %s", index, got, step.want)
		}
	}

	twinB := claudeSubscription("twin-b", observed, claudeWeeklySignals(now, now.Add(24*time.Hour)))
	twinA := claudeSubscription("twin-a", observed, claudeWeeklySignals(now, now.Add(24*time.Hour)))
	if got := pickSoonestResetAuth([]*Auth{twinB, twinA}, now, nil, nil, true); got.ID != "twin-a" {
		t.Fatalf("tie picked %s, want lowest ID twin-a", got.ID)
	}
}

func TestPickSoonestResetAuthProbesEachUnknownOnce(t *testing.T) {
	t.Parallel()

	now := soonestResetTestNow
	unknownA := claudeSubscription("unknown-a", time.Time{}, nil)
	unknownB := claudeSubscription("unknown-b", time.Time{}, nil)
	known := claudeSubscription("known", now.Add(-time.Minute), claudeWeeklySignals(now, now.Add(96*time.Hour)))
	auths := []*Auth{known, unknownB, unknownA}
	var probes soonestResetProbes

	want := []string{"unknown-a", "unknown-b", "known", "known"}
	for index, wantID := range want {
		if got := pickSoonestResetAuth(auths, now.Add(time.Duration(index)*time.Second), nil, &probes, true); got.ID != wantID {
			t.Fatalf("pick %d = %s, want %s", index, got.ID, wantID)
		}
	}
	// Without a known reset, pending probes rank as subscriptions ahead of other credentials.
	apiKey := &Auth{ID: "a-api-key", Provider: "claude", Attributes: map[string]string{AttributeAPIKey: "sk-ant-api03"}}
	if got := pickSoonestResetAuth([]*Auth{apiKey, unknownB, unknownA}, now.Add(time.Minute), nil, &probes, true); got.ID != "unknown-a" {
		t.Fatalf("pick with pending probes = %s, want subscription unknown-a", got.ID)
	}
	// An answer that never arrives frees the credential for another probe after the hold.
	later := now.Add(soonestResetProbeHold + time.Second)
	if got := pickSoonestResetAuth(auths, later, nil, &probes, true); got.ID != "unknown-a" {
		t.Fatalf("pick after hold = %s, want probe unknown-a", got.ID)
	}
	if len(probes) != 1 {
		t.Fatalf("probes = %v, want only the fresh probe after expired entries are pruned", probes)
	}
}

func TestPickSoonestResetAuthWithoutRecordingKeepsProbeAvailable(t *testing.T) {
	t.Parallel()

	now := soonestResetTestNow
	unknownA := claudeSubscription("unknown-a", time.Time{}, nil)
	unknownB := claudeSubscription("unknown-b", time.Time{}, nil)
	auths := []*Auth{unknownB, unknownA}
	var probes soonestResetProbes
	for index := 0; index < 2; index++ {
		if got := pickSoonestResetAuth(auths, now, nil, &probes, false); got.ID != "unknown-a" {
			t.Fatalf("unrecorded pick %d = %s, want unknown-a", index, got.ID)
		}
	}
	if len(probes) != 0 {
		t.Fatalf("probes = %v, want none recorded", probes)
	}
	pickSoonestResetAuth(auths, now, nil, &probes, true)
	if got := pickSoonestResetAuth(auths, now, nil, &probes, false); got.ID != "unknown-b" {
		t.Fatalf("unrecorded pick after a probe = %s, want unknown-b (pending probes are respected)", got.ID)
	}
}

func TestSoonestResetSelectorPickReservesProbes(t *testing.T) {
	t.Parallel()

	newSelectors := map[string]func() Selector{
		"bare": func() Selector { return &SoonestResetSelector{} },
		"session affinity fallback": func() Selector {
			return NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{Fallback: &SoonestResetSelector{}, TTL: time.Hour})
		},
	}
	for name, newSelector := range newSelectors {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			selector := newSelector()
			if stoppable, ok := selector.(StoppableSelector); ok {
				t.Cleanup(stoppable.Stop)
			}
			auths := []*Auth{claudeSubscription("unknown-b", time.Time{}, nil), claudeSubscription("unknown-a", time.Time{}, nil)}
			pick := func(ctx context.Context) string {
				t.Helper()
				got, errPick := selector.Pick(ctx, "claude", "", cliproxyexecutor.Options{}, auths)
				if errPick != nil {
					t.Fatalf("Pick() error = %v", errPick)
				}
				return got.ID
			}
			if got := pick(withoutSoonestResetProbe(context.Background())); got != "unknown-a" {
				t.Fatalf("suppressed pick = %s, want unknown-a", got)
			}
			if got := pick(context.Background()); got != "unknown-a" {
				t.Fatalf("first probe = %s, want unknown-a (suppressed picks do not reserve)", got)
			}
			if got := pick(context.Background()); got != "unknown-b" {
				t.Fatalf("second pick = %s, want unknown-b while unknown-a's probe is pending", got)
			}
		})
	}
}

func TestSoonestResetSelectorPickRespectsPriorityTiers(t *testing.T) {
	t.Parallel()

	now := time.Now()
	observed := now.Add(-time.Minute)
	low := claudeSubscription("low-priority-soonest", observed, claudeWeeklySignals(now, now.Add(time.Hour)))
	high := claudeSubscription("high-priority-later", observed, claudeWeeklySignals(now, now.Add(96*time.Hour)))
	high.Attributes = map[string]string{"priority": "10"}

	got, errPick := (&SoonestResetSelector{}).Pick(context.Background(), "claude", "", cliproxyexecutor.Options{}, []*Auth{low, high})
	if errPick != nil {
		t.Fatalf("Pick() error = %v", errPick)
	}
	if got.ID != high.ID {
		t.Fatalf("Pick() = %s, want the higher priority tier %s", got.ID, high.ID)
	}
}

func registerSoonestResetModels(t *testing.T, provider string, authIDs []string, models ...string) {
	t.Helper()
	reg := registry.GetGlobalRegistry()
	infos := make([]*registry.ModelInfo, 0, len(models))
	for _, model := range models {
		infos = append(infos, &registry.ModelInfo{ID: model})
	}
	for _, id := range authIDs {
		reg.RegisterClient(id, provider, infos)
		t.Cleanup(func() { reg.UnregisterClient(id) })
	}
}

func TestSchedulerPick_SoonestResetUsesFreshQuotaAcrossModelShards(t *testing.T) {
	t.Parallel()

	now := time.Now()
	observed := now.Add(-time.Minute)
	registerSoonestResetModels(t, "claude", []string{"sr-shard-a", "sr-shard-b"}, "sr-shard-model-x", "sr-shard-model-y")

	authA := claudeSubscription("sr-shard-a", observed, claudeWeeklySignals(now, now.Add(72*time.Hour)))
	authB := claudeSubscription("sr-shard-b", time.Time{}, nil)
	scheduler := newSchedulerForTest(&SoonestResetSelector{}, authA, authB)

	pick := func(model string) string {
		t.Helper()
		got, errPick := scheduler.pickSingle(context.Background(), "claude", model, cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickSingle(%s) error = %v", model, errPick)
		}
		return got.ID
	}
	// b is an unobserved subscription, so it is probed once; the next pick, on another
	// model, sees the pending probe and falls back to a's known reset.
	if got := pick("sr-shard-model-x"); got != "sr-shard-b" {
		t.Fatalf("model x picked %s, want probe sr-shard-b", got)
	}
	if got := pick("sr-shard-model-y"); got != "sr-shard-a" {
		t.Fatalf("model y picked %s, want sr-shard-a while b's probe is pending", got)
	}

	// A result on model x teaches b's reset; then a's reset moves sooner. Targeted updates
	// refresh only model x's shard, so model y must rank with the provider-level snapshots.
	scheduler.upsertAuthResult(claudeSubscription("sr-shard-b", now, claudeWeeklySignals(now, now.Add(24*time.Hour))), []string{"sr-shard-model-x"}, false)
	if got := pick("sr-shard-model-y"); got != "sr-shard-b" {
		t.Fatalf("model y picked %s, want soonest reset sr-shard-b from fresh provider metadata", got)
	}
	authA.Quota.Signals = claudeWeeklySignals(now, now.Add(12*time.Hour))
	scheduler.upsertAuthResult(authA, []string{"sr-shard-model-x"}, false)
	if got := pick("sr-shard-model-y"); got != "sr-shard-a" {
		t.Fatalf("model y picked %s, want soonest reset sr-shard-a from fresh provider metadata", got)
	}

	tried := map[string]struct{}{"sr-shard-a": {}}
	got, errPick := scheduler.pickSingle(context.Background(), "claude", "sr-shard-model-y", cliproxyexecutor.Options{}, tried)
	if errPick != nil || got.ID != "sr-shard-b" {
		t.Fatalf("retry pick = %v, %v; want sr-shard-b", got, errPick)
	}
}

func TestSchedulerPick_SoonestResetRanksAcrossMixedProviders(t *testing.T) {
	t.Parallel()

	now := time.Now()
	observed := now.Add(-time.Minute)
	claudeLater := claudeSubscription("claude-later", observed, claudeWeeklySignals(now, now.Add(96*time.Hour)))
	codexSooner := codexSubscription("codex-sooner", observed, map[string]string{
		"X-Codex-Primary-Window-Minutes": "10080",
		"X-Codex-Primary-Reset-At":       epochSignal(now.Add(24 * time.Hour)),
	})
	geminiKey := &Auth{ID: "a-gemini-key", Provider: "gemini", Attributes: map[string]string{AttributeAPIKey: "key"}}
	scheduler := newSchedulerForTest(&SoonestResetSelector{}, claudeLater, codexSooner, geminiKey)

	providers := []string{"gemini", "claude", "codex"}
	for index := 0; index < 3; index++ {
		got, provider, errPick := scheduler.pickMixed(context.Background(), providers, "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickMixed() #%d error = %v", index, errPick)
		}
		if got.ID != "codex-sooner" || provider != "codex" {
			t.Fatalf("pickMixed() #%d = %s/%s, want codex-sooner/codex", index, got.ID, provider)
		}
	}
	got, provider, errPick := scheduler.pickMixed(context.Background(), providers, "", cliproxyexecutor.Options{}, map[string]struct{}{"codex-sooner": {}})
	if errPick != nil || got.ID != "claude-later" || provider != "claude" {
		t.Fatalf("pickMixed(tried codex) = %v/%s, %v; want claude-later/claude", got, provider, errPick)
	}
}

func TestSchedulerPickMixed_SoonestResetReservesProbes(t *testing.T) {
	t.Parallel()

	now := time.Now()
	claudeUnknown := claudeSubscription("claude-unknown", time.Time{}, nil)
	codexKnown := codexSubscription("codex-known", now.Add(-time.Minute), map[string]string{
		"X-Codex-Primary-Window-Minutes": "10080",
		"X-Codex-Primary-Reset-At":       epochSignal(now.Add(48 * time.Hour)),
	})
	scheduler := newSchedulerForTest(&SoonestResetSelector{}, claudeUnknown, codexKnown)
	providers := []string{"codex", "claude"}
	pick := func(ctx context.Context) string {
		t.Helper()
		got, _, errPick := scheduler.pickMixed(ctx, providers, "", cliproxyexecutor.Options{}, nil)
		if errPick != nil {
			t.Fatalf("pickMixed() error = %v", errPick)
		}
		return got.ID
	}
	if got := pick(withoutSoonestResetProbe(context.Background())); got != "claude-unknown" {
		t.Fatalf("suppressed pick = %s, want claude-unknown", got)
	}
	if got := pick(context.Background()); got != "claude-unknown" {
		t.Fatalf("probe pick = %s, want claude-unknown (suppressed picks do not reserve)", got)
	}
	if got := pick(context.Background()); got != "codex-known" {
		t.Fatalf("pick while probe pending = %s, want codex-known", got)
	}
}

func TestSchedulerPickMixed_SoonestResetTiesResolveToLowestIDAcrossProviders(t *testing.T) {
	t.Parallel()

	claudeKey := &Auth{ID: "b-claude-key", Provider: "claude", Attributes: map[string]string{AttributeAPIKey: "sk-ant-api03"}}
	geminiKey := &Auth{ID: "a-gemini-key", Provider: "gemini", Attributes: map[string]string{AttributeAPIKey: "key"}}
	scheduler := newSchedulerForTest(&SoonestResetSelector{}, claudeKey, geminiKey)

	got, provider, errPick := scheduler.pickMixed(context.Background(), []string{"claude", "gemini"}, "", cliproxyexecutor.Options{}, nil)
	if errPick != nil || got.ID != "a-gemini-key" || provider != "gemini" {
		t.Fatalf("pickMixed() = %v/%s, %v; want lowest ID a-gemini-key/gemini like the legacy selector", got, provider, errPick)
	}
}

func TestSchedulerPickMixed_SoonestResetUsesFreshQuotaAcrossModelShards(t *testing.T) {
	t.Parallel()

	now := time.Now()
	observed := now.Add(-time.Minute)
	registerSoonestResetModels(t, "claude", []string{"sr-mixed-claude"}, "sr-mixed-model-x", "sr-mixed-model-y")
	registerSoonestResetModels(t, "codex", []string{"sr-mixed-codex"}, "sr-mixed-model-x", "sr-mixed-model-y")

	claudeAuth := claudeSubscription("sr-mixed-claude", observed, claudeWeeklySignals(now, now.Add(48*time.Hour)))
	codexAuth := codexSubscription("sr-mixed-codex", observed, map[string]string{
		"X-Codex-Primary-Window-Minutes": "10080",
		"X-Codex-Primary-Reset-At":       epochSignal(now.Add(24 * time.Hour)),
	})
	scheduler := newSchedulerForTest(&SoonestResetSelector{}, claudeAuth, codexAuth)
	providers := []string{"claude", "codex"}
	for _, model := range []string{"sr-mixed-model-x", "sr-mixed-model-y"} {
		got, _, errPick := scheduler.pickMixed(context.Background(), providers, model, cliproxyexecutor.Options{}, nil)
		if errPick != nil || got.ID != "sr-mixed-codex" {
			t.Fatalf("pickMixed(%s) = %v, %v; want sr-mixed-codex", model, got, errPick)
		}
	}

	// A result on model x moves Claude's weekly reset ahead of Codex's.
	claudeAuth.Quota.Signals = claudeWeeklySignals(now, now.Add(6*time.Hour))
	scheduler.upsertAuthResult(claudeAuth, []string{"sr-mixed-model-x"}, false)
	got, provider, errPick := scheduler.pickMixed(context.Background(), providers, "sr-mixed-model-y", cliproxyexecutor.Options{}, nil)
	if errPick != nil || got.ID != "sr-mixed-claude" || provider != "claude" {
		t.Fatalf("pickMixed(model y) = %v/%s, %v; want sr-mixed-claude from fresh provider metadata", got, provider, errPick)
	}
}

func TestManager_InitializesSchedulerForSoonestResetSelector(t *testing.T) {
	t.Parallel()

	manager := NewManager(nil, &SoonestResetSelector{}, nil)
	if manager.scheduler.strategy != schedulerStrategySoonestReset {
		t.Fatalf("scheduler strategy = %v, want %v", manager.scheduler.strategy, schedulerStrategySoonestReset)
	}
	if !manager.useSchedulerFastPath() {
		t.Fatal("soonest-reset must use the scheduler fast path as a built-in selector")
	}
	manager.SetSelector(&FillFirstSelector{})
	manager.SetSelector(&SoonestResetSelector{})
	if manager.scheduler.strategy != schedulerStrategySoonestReset {
		t.Fatalf("scheduler strategy after switch = %v, want %v", manager.scheduler.strategy, schedulerStrategySoonestReset)
	}
}

// soonestResetQuotaExecutor answers every request with Claude unified quota headers whose
// weekly reset depends on the credential, like the upstream does.
type soonestResetQuotaExecutor struct {
	mu     sync.Mutex
	resets map[string]time.Time
	picked []string
}

func (*soonestResetQuotaExecutor) Identifier() string { return "claude" }

func (e *soonestResetQuotaExecutor) Execute(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	e.picked = append(e.picked, auth.ID)
	reset := e.resets[auth.ID]
	e.mu.Unlock()
	internallogging.SetResponseHeaders(ctx, http.Header{
		"Anthropic-Ratelimit-Unified-Status":         []string{"allowed"},
		"Anthropic-Ratelimit-Unified-7d-Status":      []string{"allowed"},
		"Anthropic-Ratelimit-Unified-7d-Utilization": []string{"0.2"},
		"Anthropic-Ratelimit-Unified-7d-Reset":       []string{epochSignal(reset)},
	})
	return cliproxyexecutor.Response{Payload: []byte(`{}`)}, nil
}

func (*soonestResetQuotaExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "not implemented"}
}

func (*soonestResetQuotaExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) {
	return auth, nil
}

func (e *soonestResetQuotaExecutor) CountTokens(ctx context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.mu.Lock()
	e.picked = append(e.picked, "count:"+auth.ID)
	reset := e.resets[auth.ID]
	e.mu.Unlock()
	internallogging.SetResponseHeaders(ctx, http.Header{
		"Anthropic-Ratelimit-Unified-7d-Reset": []string{epochSignal(reset)},
	})
	return cliproxyexecutor.Response{Payload: []byte(`{"input_tokens":1}`)}, nil
}

func (*soonestResetQuotaExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, &Error{HTTPStatus: http.StatusNotImplemented, Message: "not implemented"}
}

func (e *soonestResetQuotaExecutor) pickedIDs() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.picked...)
}

func TestManagerSoonestResetLearnsResetsThenSpendsSoonest(t *testing.T) {
	selectors := map[string]func() Selector{
		"scheduler fast path": func() Selector { return &SoonestResetSelector{} },
		"session affinity fallback": func() Selector {
			return NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{Fallback: &SoonestResetSelector{}, TTL: time.Hour})
		},
	}
	for name, newSelector := range selectors {
		t.Run(name, func(t *testing.T) {
			selector := newSelector()
			if stoppable, ok := selector.(StoppableSelector); ok {
				t.Cleanup(stoppable.Stop)
			}
			now := time.Now()
			prefix := "sr-manager-" + strconv.Itoa(len(name)) + "-"
			executor := &soonestResetQuotaExecutor{resets: map[string]time.Time{
				prefix + "a": now.Add(5 * 24 * time.Hour),
				prefix + "b": now.Add(3 * 24 * time.Hour),
				prefix + "c": now.Add(24 * time.Hour),
			}}
			manager := NewManager(nil, selector, nil)
			manager.RegisterExecutor(executor)
			modelX := prefix + "model-x"
			modelY := prefix + "model-y"
			ids := []string{prefix + "a", prefix + "b", prefix + "c"}
			for _, id := range ids {
				if _, errRegister := manager.Register(context.Background(), &Auth{
					ID:       id,
					Provider: "claude",
					Status:   StatusActive,
					Metadata: map[string]any{"access_token": "token-" + id},
				}); errRegister != nil {
					t.Fatalf("Register(%s) error = %v", id, errRegister)
				}
			}
			registerSoonestResetModels(t, "claude", ids, modelX, modelY)

			execute := func(model string) {
				t.Helper()
				if _, errExecute := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{}); errExecute != nil {
					t.Fatalf("Execute(%s) error = %v", model, errExecute)
				}
			}
			// Model y probes a first, which also builds y's shard before b and c are observed.
			execute(modelY)
			for index := 0; index < 4; index++ {
				execute(modelX)
			}
			execute(modelY)

			want := []string{prefix + "a", prefix + "b", prefix + "c", prefix + "c", prefix + "c", prefix + "c"}
			got := executor.pickedIDs()
			if len(got) != len(want) {
				t.Fatalf("picked = %v, want %v", got, want)
			}
			for index := range want {
				if got[index] != want[index] {
					t.Fatalf("picked = %v, want %v", got, want)
				}
			}
		})
	}
}

func TestManagerSoonestResetTokenCountsAndSelectAuthDoNotSpendProbes(t *testing.T) {
	now := time.Now()
	prefix := "sr-count-"
	executor := &soonestResetQuotaExecutor{resets: map[string]time.Time{
		prefix + "a": now.Add(24 * time.Hour),
		prefix + "b": now.Add(5 * 24 * time.Hour),
	}}
	manager := NewManager(nil, &SoonestResetSelector{}, nil)
	manager.RegisterExecutor(executor)
	model := prefix + "model"
	ids := []string{prefix + "a", prefix + "b"}
	for _, id := range ids {
		if _, errRegister := manager.Register(context.Background(), &Auth{
			ID:       id,
			Provider: "claude",
			Status:   StatusActive,
			Metadata: map[string]any{"access_token": "token-" + id},
		}); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", id, errRegister)
		}
	}
	registerSoonestResetModels(t, "claude", ids, model)

	for index := 0; index < 2; index++ {
		selected, errSelect := manager.SelectAuth(context.Background(), "claude", model, cliproxyexecutor.Options{})
		if errSelect != nil || selected.ID != prefix+"a" {
			t.Fatalf("SelectAuth() #%d = %v, %v; want %sa", index, selected, errSelect, prefix)
		}
	}
	if _, errCount := manager.ExecuteCount(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{}); errCount != nil {
		t.Fatalf("ExecuteCount() error = %v", errCount)
	}
	for index := 0; index < 3; index++ {
		if _, errExecute := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{}); errExecute != nil {
			t.Fatalf("Execute() #%d error = %v", index, errExecute)
		}
	}
	// The count neither learns a's reset nor reserves it, so generation probes a, then b,
	// then settles on a, whose weekly window resets first.
	want := []string{"count:" + prefix + "a", prefix + "a", prefix + "b", prefix + "a"}
	got := executor.pickedIDs()
	if len(got) != len(want) {
		t.Fatalf("picked = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("picked = %v, want %v", got, want)
		}
	}
}

// claudeFastModeRefusalSignals is the quota snapshot of Claude's request-scoped fast-mode
// credit refusal: unified quota headers without any usage window.
func claudeFastModeRefusalSignals(now time.Time) map[string]string {
	return map[string]string{
		"Anthropic-Ratelimit-Unified-Reset":                   epochSignal(now.Add(5 * 24 * time.Hour)),
		"Anthropic-Ratelimit-Unified-Overage-Disabled-Reason": "org_level_disabled_until",
	}
}

func TestSoonestResetFactsCarryKeepsWindowsAcrossWindowlessSnapshots(t *testing.T) {
	t.Parallel()

	now := soonestResetTestNow
	reset := now.Add(24 * time.Hour)
	known := soonestResetFactsFor(claudeSubscription("a", now.Add(-time.Minute), claudeWeeklySignals(now, reset)))
	windowless := soonestResetFactsFor(claudeSubscription("a", now, claudeFastModeRefusalSignals(now)))
	unobserved := soonestResetFactsFor(claudeSubscription("a", time.Time{}, nil))
	if rank := windowless.rank(now, true); rank.class != soonestResetClassSubscription {
		t.Fatalf("pending windowless class = %d, want subscription without carried facts", rank.class)
	}
	for name, facts := range map[string]soonestResetFacts{"windowless": windowless, "unobserved": unobserved} {
		rank := facts.carry(known).rank(now, true)
		if rank.class != soonestResetClassKnown || !rank.weeklyReset.Equal(reset) {
			t.Fatalf("%s carried rank = %+v, want known reset %v", name, rank, reset)
		}
	}
	// A snapshot that reports windows replaces the previous facts, even when it is worse.
	exhausted := soonestResetFactsFor(claudeSubscription("a", now, map[string]string{
		"Anthropic-Ratelimit-Unified-7d-Status": "rejected",
		"Anthropic-Ratelimit-Unified-7d-Reset":  epochSignal(now.Add(time.Hour)),
	}))
	if rank := exhausted.carry(known).rank(now, false); !rank.exhausted {
		t.Fatalf("windowed snapshot rank = %+v, want it to replace the carried facts", rank)
	}
	// With nothing to carry, a windowless snapshot still asks for a probe.
	if rank := windowless.carry(soonestResetFacts{}).rank(now, false); rank.class != soonestResetClassProbe {
		t.Fatalf("windowless without history class = %d, want probe", rank.class)
	}
	// Facts never carry between a subscription and a credential with nothing to learn.
	apiKey := soonestResetFactsFor(&Auth{ID: "a", Provider: "claude", Attributes: map[string]string{AttributeAPIKey: "sk-ant-api03"}})
	if rank := apiKey.carry(known).rank(now, false); rank.class != soonestResetClassOther {
		t.Fatalf("api key carried class = %d, want other", rank.class)
	}
}

// TestSoonestResetWindowlessSnapshotKeepsKnownReset follows Claude Code's fast-mode fallback:
// a fast request is refused with a windowless snapshot while the credential's probe is still
// pending, and the standard retry must stay on the soonest reset.
func TestSoonestResetWindowlessSnapshotKeepsKnownReset(t *testing.T) {
	t.Parallel()

	now := time.Now()
	setup := func(prefix string) (soonest, later, learned, refused *Auth) {
		soonest = claudeSubscription(prefix+"a", time.Time{}, nil)
		later = claudeSubscription(prefix+"b", now.Add(-time.Minute), claudeWeeklySignals(now, now.Add(72*time.Hour)))
		learned = claudeSubscription(soonest.ID, now, claudeWeeklySignals(now, now.Add(24*time.Hour)))
		learned.Generation = 1
		refused = claudeSubscription(soonest.ID, now, claudeFastModeRefusalSignals(now))
		refused.Generation = 2
		return soonest, later, learned, refused
	}

	t.Run("selector", func(t *testing.T) {
		t.Parallel()
		soonest, later, learned, refused := setup("sr-windowless-selector-")
		selector := &SoonestResetSelector{}
		pick := func(ctx context.Context, auths ...*Auth) string {
			t.Helper()
			got, errPick := selector.Pick(ctx, "claude", "", cliproxyexecutor.Options{}, auths)
			if errPick != nil {
				t.Fatalf("Pick() error = %v", errPick)
			}
			return got.ID
		}
		if got := pick(context.Background(), soonest, later); got != soonest.ID {
			t.Fatalf("probe pick = %s, want %s", got, soonest.ID)
		}
		// The selector ranks from the facts it is handed, which the Manager's scheduler carried
		// across the windowless snapshot.
		recorded := withSoonestResetFacts(context.Background(), map[string]soonestResetFacts{
			soonest.ID: soonestResetFactsFor(refused).carry(soonestResetFactsFor(learned)),
		})
		if got := pick(recorded, refused, later); got != soonest.ID {
			t.Fatalf("pick with recorded facts = %s, want %s", got, soonest.ID)
		}
		// On its own snapshot the refused credential is an unknown subscription with a pending probe.
		if got := pick(context.Background(), refused, later); got != later.ID {
			t.Fatalf("pick without recorded facts = %s, want %s", got, later.ID)
		}
	})

	t.Run("scheduler", func(t *testing.T) {
		t.Parallel()
		soonest, later, learned, refused := setup("sr-windowless-scheduler-")
		scheduler := newSchedulerForTest(&SoonestResetSelector{}, soonest, later)
		pick := func(step string) {
			t.Helper()
			got, errPick := scheduler.pickSingle(context.Background(), "claude", "", cliproxyexecutor.Options{}, nil)
			if errPick != nil || got.ID != soonest.ID {
				t.Fatalf("%s pick = %v, %v; want %s", step, got, errPick, soonest.ID)
			}
		}
		pick("probe")
		scheduler.upsertAuthResult(learned, nil, false)
		pick("learned")
		scheduler.upsertAuthResult(refused, nil, false)
		pick("refused")

		// A rebuild keeps the carried facts too.
		scheduler.rebuild([]*Auth{refused, later})
		scheduler.mu.Lock()
		facts := scheduler.providers["claude"].auths[soonest.ID].soonestReset
		scheduler.mu.Unlock()
		if wantReset := now.Add(24 * time.Hour); facts.weeklyReset.Unix() != wantReset.Unix() {
			t.Fatalf("weekly reset after rebuild = %v, want %v", facts.weeklyReset, wantReset)
		}
	})
}

// soonestResetFastModeExecutor refuses fast-mode requests like Claude does for accounts
// without usage credits: a request-scoped 429 whose quota headers report no usage window.
type soonestResetFastModeExecutor struct {
	*soonestResetQuotaExecutor
}

func (e soonestResetFastModeExecutor) Execute(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if string(req.Payload) != `{"speed":"fast"}` {
		return e.soonestResetQuotaExecutor.Execute(ctx, auth, req, opts)
	}
	e.mu.Lock()
	e.picked = append(e.picked, "fast:"+auth.ID)
	e.mu.Unlock()
	headers := http.Header{}
	for key, value := range claudeFastModeRefusalSignals(time.Now()) {
		headers.Set(key, value)
	}
	internallogging.SetResponseHeaders(ctx, headers)
	return cliproxyexecutor.Response{}, NewRequestScopedError("Usage credits are required for fast mode.", http.StatusTooManyRequests)
}

func TestManagerSoonestResetFastModeRefusalKeepsSoonestReset(t *testing.T) {
	selectors := map[string]func() Selector{
		"scheduler fast path": func() Selector { return &SoonestResetSelector{} },
		"session affinity fallback": func() Selector {
			return NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{Fallback: &SoonestResetSelector{}, TTL: time.Hour})
		},
	}
	for name, newSelector := range selectors {
		t.Run(name, func(t *testing.T) {
			selector := newSelector()
			if stoppable, ok := selector.(StoppableSelector); ok {
				t.Cleanup(stoppable.Stop)
			}
			now := time.Now()
			prefix := "sr-fast-" + strconv.Itoa(len(name)) + "-"
			executor := &soonestResetQuotaExecutor{resets: map[string]time.Time{
				prefix + "a": now.Add(24 * time.Hour),
				prefix + "b": now.Add(3 * 24 * time.Hour),
			}}
			manager := NewManager(nil, selector, nil)
			manager.RegisterExecutor(soonestResetFastModeExecutor{executor})
			model := prefix + "model"
			ids := []string{prefix + "a", prefix + "b"}
			for _, id := range ids {
				if _, errRegister := manager.Register(context.Background(), &Auth{
					ID:       id,
					Provider: "claude",
					Status:   StatusActive,
					Metadata: map[string]any{"access_token": "token-" + id},
				}); errRegister != nil {
					t.Fatalf("Register(%s) error = %v", id, errRegister)
				}
			}
			registerSoonestResetModels(t, "claude", ids, model)

			execute := func(payload string) error {
				_, errExecute := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model, Payload: []byte(payload)}, cliproxyexecutor.Options{})
				return errExecute
			}
			// Probe a, then b; a's probe is still pending when its fast requests are refused.
			for index := 0; index < 2; index++ {
				if errExecute := execute(`{}`); errExecute != nil {
					t.Fatalf("probe %d error = %v", index, errExecute)
				}
			}
			for index := 0; index < 2; index++ {
				if errExecute := execute(`{"speed":"fast"}`); errExecute == nil {
					t.Fatalf("fast request %d succeeded, want the refusal", index)
				}
				if errExecute := execute(`{}`); errExecute != nil {
					t.Fatalf("standard retry %d error = %v", index, errExecute)
				}
			}

			a, b := prefix+"a", prefix+"b"
			want := []string{a, b, "fast:" + a, a, "fast:" + a, a}
			got := executor.pickedIDs()
			if len(got) != len(want) {
				t.Fatalf("picked = %v, want %v", got, want)
			}
			for index := range want {
				if got[index] != want[index] {
					t.Fatalf("picked = %v, want %v", got, want)
				}
			}
		})
	}
}

func TestSoonestResetWindowlessRejectionKeepsCredentialUsedUp(t *testing.T) {
	t.Parallel()

	now := soonestResetTestNow
	reset := now.Add(24 * time.Hour)
	known := soonestResetFactsFor(claudeSubscription("a", now.Add(-time.Minute), claudeWeeklySignals(now, reset)))
	rejection := func(claim string, until time.Time) soonestResetFacts {
		signals := map[string]string{
			"Anthropic-Ratelimit-Unified-Status":               "rejected",
			"Anthropic-Ratelimit-Unified-Representative-Claim": claim,
		}
		if !until.IsZero() {
			signals["Anthropic-Ratelimit-Unified-Reset"] = epochSignal(until)
		}
		return soonestResetFactsFor(claudeSubscription("a", now, signals))
	}

	fiveHour := rejection("five_hour", now.Add(4*time.Hour))
	if rank := fiveHour.rank(now, false); !rank.exhausted || rank.class != soonestResetClassProbe {
		t.Fatalf("rejection without history rank = %+v, want an exhausted probe", rank)
	}
	carried := fiveHour.carry(known)
	if rank := carried.rank(now, false); !rank.exhausted || rank.class != soonestResetClassKnown || !rank.weeklyReset.Equal(reset) {
		t.Fatalf("carried rejection rank = %+v, want exhausted with known reset %v", rank, reset)
	}
	if rank := carried.rank(now.Add(5*time.Hour), false); rank.exhausted || rank.class != soonestResetClassKnown {
		t.Fatalf("rank after the rejection ends = %+v, want usable with the known reset", rank)
	}
	if got := rejection("seven_day", time.Time{}).exhaustedUntil; !got.Equal(now.Add(quotaWindowWeek)) {
		t.Fatalf("seven_day rejection without reset holds until %v, want %v", got, now.Add(quotaWindowWeek))
	}
	if rank := rejection("overage", now.Add(4*time.Hour)).carry(known).rank(now, false); rank.exhausted || rank.class != soonestResetClassKnown {
		t.Fatalf("overage rejection rank = %+v, want the usable known facts", rank)
	}

	// Ranking from the carried facts moves on to the next soonest reset while the rejection
	// holds, and back once it ends.
	soonest := claudeSubscription("a-soonest", now.Add(-time.Minute), claudeWeeklySignals(now, reset))
	later := claudeSubscription("b-later", now.Add(-time.Minute), claudeWeeklySignals(now, now.Add(72*time.Hour)))
	rejected := claudeSubscription(soonest.ID, now, map[string]string{
		"Anthropic-Ratelimit-Unified-Status":               "rejected",
		"Anthropic-Ratelimit-Unified-Reset":                epochSignal(now.Add(4 * time.Hour)),
		"Anthropic-Ratelimit-Unified-Representative-Claim": "five_hour",
	})
	recorded := map[string]soonestResetFacts{soonest.ID: soonestResetFactsFor(rejected).carry(soonestResetFactsFor(soonest))}
	if got := pickSoonestResetAuth([]*Auth{rejected, later}, now, recorded, nil, true); got.ID != later.ID {
		t.Fatalf("pick during the rejection = %s, want %s", got.ID, later.ID)
	}
	if got := pickSoonestResetAuth([]*Auth{rejected, later}, now.Add(5*time.Hour), recorded, nil, true); got.ID != soonest.ID {
		t.Fatalf("pick after the rejection = %s, want %s with its carried reset", got.ID, soonest.ID)
	}
}

// TestManagerSoonestResetSessionAffinityRanksFromRecordedResults covers session affinity,
// where most requests reuse a binding without a pick: a credential's windows must still be
// known when a later new session is picked while its snapshot is windowless again.
func TestManagerSoonestResetSessionAffinityRanksFromRecordedResults(t *testing.T) {
	affinity := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{Fallback: &SoonestResetSelector{}, TTL: time.Hour})
	t.Cleanup(affinity.Stop)
	now := time.Now()
	prefix := "sr-affinity-"
	a, b := prefix+"a", prefix+"b"
	executor := &soonestResetQuotaExecutor{resets: map[string]time.Time{
		a: now.Add(6 * 24 * time.Hour),
		b: now.Add(24 * time.Hour),
	}}
	manager := NewManager(nil, affinity, nil)
	manager.RegisterExecutor(soonestResetFastModeExecutor{executor})
	model := prefix + "model"
	for _, id := range []string{a, b} {
		if _, errRegister := manager.Register(context.Background(), &Auth{
			ID:       id,
			Provider: "claude",
			Status:   StatusActive,
			Metadata: map[string]any{"access_token": "token-" + id},
		}); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", id, errRegister)
		}
	}
	registerSoonestResetModels(t, "claude", []string{a, b}, model)

	execute := func(session, payload string) {
		t.Helper()
		opts := cliproxyexecutor.Options{Headers: http.Header{"X-Claude-Code-Session-Id": []string{session}}}
		_, errExecute := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model, Payload: []byte(payload)}, opts)
		if (errExecute != nil) != (payload != `{}`) {
			t.Fatalf("Execute(%s, %s) error = %v", session, payload, errExecute)
		}
	}
	execute("s1", `{"speed":"fast"}`) // probes a, which stays windowless
	execute("s2", `{}`)               // probes b while a's probe is pending
	execute("s1", `{}`)               // a learns its reset through the binding, without a pick
	execute("s1", `{"speed":"fast"}`) // a is windowless again
	// Once the probe hold passes, a new session must rank a by its recorded reset.
	fallback := affinity.fallback.(*SoonestResetSelector)
	fallback.mu.Lock()
	for id := range fallback.probes {
		fallback.probes[id] = now.Add(-2 * soonestResetProbeHold)
	}
	fallback.mu.Unlock()
	execute("s3", `{}`)

	want := []string{"fast:" + a, b, a, "fast:" + a, b}
	got := executor.pickedIDs()
	if len(got) != len(want) {
		t.Fatalf("picked = %v, want %v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("picked = %v, want %v", got, want)
		}
	}
}

// TestManagerSoonestResetSelectAuthRanksFromRecordedResults covers the legacy path with the
// bare selector, which SelectAuth callers take: a credential whose snapshot turned windowless
// must keep its recorded reset rather than jump ahead as a probe.
func TestManagerSoonestResetSelectAuthRanksFromRecordedResults(t *testing.T) {
	now := time.Now()
	prefix := "sr-select-recorded-"
	a, b := prefix+"a", prefix+"b"
	executor := &soonestResetQuotaExecutor{resets: map[string]time.Time{
		a: now.Add(3 * 24 * time.Hour),
		b: now.Add(24 * time.Hour),
	}}
	manager := NewManager(nil, &SoonestResetSelector{}, nil)
	manager.RegisterExecutor(soonestResetFastModeExecutor{executor})
	model := prefix + "model"
	for _, id := range []string{a, b} {
		if _, errRegister := manager.Register(context.Background(), &Auth{
			ID:       id,
			Provider: "claude",
			Status:   StatusActive,
			Metadata: map[string]any{"access_token": "token-" + id},
		}); errRegister != nil {
			t.Fatalf("Register(%s) error = %v", id, errRegister)
		}
	}
	registerSoonestResetModels(t, "claude", []string{a, b}, model)
	execute := func(payload string, opts cliproxyexecutor.Options) {
		t.Helper()
		_, errExecute := manager.Execute(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: model, Payload: []byte(payload)}, opts)
		if (errExecute != nil) != (payload != `{}`) {
			t.Fatalf("Execute(%s) error = %v", payload, errExecute)
		}
	}
	execute(`{}`, cliproxyexecutor.Options{}) // probes a
	execute(`{}`, cliproxyexecutor.Options{}) // probes b
	execute(`{"speed":"fast"}`, cliproxyexecutor.Options{Metadata: map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: a}})

	selected, errSelect := manager.SelectAuth(context.Background(), "claude", model, cliproxyexecutor.Options{})
	if errSelect != nil || selected.ID != b {
		t.Fatalf("SelectAuth() = %v, %v; want the soonest recorded reset %s", selected, errSelect, b)
	}
	if want := []string{a, b, "fast:" + a}; len(executor.pickedIDs()) != len(want) {
		t.Fatalf("picked = %v, want %v", executor.pickedIDs(), want)
	}
}
