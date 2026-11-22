package auth

import (
	"testing"
	"time"
)

func TestQuotaWindowsStructuredLookupFallback(t *testing.T) {
	percent := 42.0
	auth := &Auth{Provider: "claude", Quota: QuotaState{LookupWindows: []QuotaWindow{
		{Key: "scoped", Scoped: true},
		{Key: "long", Duration: weeklyWindow},
		{Key: "short-b", Duration: fiveHourWindow},
		{Key: "short-a", Duration: fiveHourWindow, UsedPercent: &percent},
	}}}
	var now time.Time
	windows := QuotaWindows(auth, now)
	want := []string{"short-a", "short-b", "long", "scoped"}
	if len(windows) != len(want) {
		t.Fatalf("windows = %+v", windows)
	}
	for i, key := range want {
		if windows[i].Key != key {
			t.Fatalf("window %d = %q, want %q", i, windows[i].Key, key)
		}
	}
	for _, copied := range []QuotaWindow{windows[0], auth.Clone().Quota.LookupWindows[3]} {
		if copied.UsedPercent == nil || *copied.UsedPercent != 42 {
			t.Fatalf("copied usage = %v", copied.UsedPercent)
		}
		*copied.UsedPercent = 0
	}
	cloned := auth.Clone()
	cloned.Quota.LookupWindows[0].Key = "changed"
	if auth.Quota.LookupWindows[0].Key != "scoped" || percent != 42 {
		t.Fatalf("source windows changed: %+v, usage = %v", auth.Quota.LookupWindows, percent)
	}
	auth.Quota.LookupSignals = map[string]string{"Anthropic-Ratelimit-Unified-7d-Utilization": "0.4"}
	if windows = QuotaWindows(auth, now); len(windows) != 1 || windows[0].Key != "seven_day" {
		t.Fatalf("signal-derived windows should take precedence: %+v", windows)
	}
}

func TestClaudeQuotaWindowsExactDecimalPercent(t *testing.T) {
	for _, tc := range []struct {
		fraction string
		percent  float64
	}{{"0.57", 57}, {"0.55", 55}, {"0.4237", 42.37}} {
		windows := claudeQuotaWindows(map[string]string{
			"Anthropic-Ratelimit-Unified-7d-Utilization": tc.fraction,
		})
		if len(windows) != 1 || windows[0].UsedPercent == nil {
			t.Fatalf("%s: missing utilization", tc.fraction)
		}
		if got := *windows[0].UsedPercent; got != tc.percent {
			t.Errorf("%s: UsedPercent = %.17g, want %.17g", tc.fraction, got, tc.percent)
		}
	}
}
