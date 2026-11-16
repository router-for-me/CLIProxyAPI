package auth

import (
	"testing"
	"time"
)

var quotaHistoryNow = time.Date(2026, 10, 26, 10, 0, 0, 0, time.UTC)

const quotaHistoryWeek = 7 * 24 * time.Hour

func weeklyQuotaWindow(observedAt time.Time, used float64, resetAt time.Time) QuotaWindow {
	return QuotaWindow{
		Kind:       QuotaWindowWeekly,
		Used:       used,
		ResetAt:    resetAt,
		Period:     quotaHistoryWeek,
		Source:     QuotaSourceUsage,
		ObservedAt: observedAt,
	}
}

// observeWeekly records one weekly window for authID and returns the number of points added.
func observeWeekly(h *QuotaHistory, authID string, observedAt time.Time, used float64, resetAt time.Time) int {
	return h.Observe(authID, []QuotaWindow{weeklyQuotaWindow(observedAt, used, resetAt)}, observedAt)
}

func weeklyPoints(t *testing.T, h *QuotaHistory, authID string) []QuotaHistoryPoint {
	t.Helper()
	series := h.Series(authID, time.Time{})
	if len(series) != 1 {
		t.Fatalf("got %d series for %s, want 1: %+v", len(series), authID, series)
	}
	return series[0].Points
}

func TestQuotaObservationsDoesNotRollPassedReset(t *testing.T) {
	passed := earliestResetNow.Add(-time.Hour)
	auth := claudeAuthWithHeaders("rolled", 0.2, 0.5, 0.1, passed, passed)

	var observed QuotaWindow
	for _, w := range QuotaObservations(auth) {
		if w.Kind == QuotaWindowWeekly {
			observed = w
		}
	}
	if observed.Used != 0.5 || !observed.ResetAt.Equal(passed) {
		t.Fatalf("observation = %+v, want used 0.5 resetting at %s", observed, passed)
	}

	var current QuotaWindow
	for _, w := range QuotaWindows(auth, earliestResetNow) {
		if w.Kind == QuotaWindowWeekly {
			current = w
		}
	}
	if current.Used != 0 || !current.ResetAt.Equal(passed.Add(quotaHistoryWeek)) {
		t.Fatalf("current window = %+v, want rolled to used 0 resetting at %s", current, passed.Add(quotaHistoryWeek))
	}
}

func TestQuotaHistoryRecordsFirstReading(t *testing.T) {
	h := NewQuotaHistory()
	observedAt := quotaHistoryNow.Add(-time.Minute + 750*time.Millisecond)
	reset := quotaHistoryNow.Add(3 * 24 * time.Hour).In(time.FixedZone("UTC+2", 2*60*60))
	if added := observeWeekly(h, "a", observedAt, 0.412345, reset); added != 1 {
		t.Fatalf("added %d points, want 1", added)
	}
	series := h.Series("a", time.Time{})
	if len(series) != 1 || series[0].Kind != QuotaWindowWeekly || series[0].Scope != "" || series[0].Period != quotaHistoryWeek {
		t.Fatalf("series = %+v", series)
	}
	point := series[0].Points[0]
	if !point.At.Equal(quotaHistoryNow.Add(-time.Minute)) || point.At.Location() != time.UTC {
		t.Fatalf("point time = %s, want %s in UTC", point.At, quotaHistoryNow.Add(-time.Minute))
	}
	if point.Used != 0.4123 {
		t.Fatalf("used = %v, want 0.4123", point.Used)
	}
	if !point.ResetAt.Equal(reset) || point.ResetAt.Location() != time.UTC {
		t.Fatalf("reset = %s, want %s in UTC", point.ResetAt, reset)
	}
}

func TestQuotaHistorySkipsUnchangedObservedAt(t *testing.T) {
	h := NewQuotaHistory()
	reset := quotaHistoryNow.Add(time.Hour)
	observeWeekly(h, "a", quotaHistoryNow, 0.9, reset)

	if added := observeWeekly(h, "a", quotaHistoryNow, 0.9, reset); added != 0 {
		t.Fatalf("duplicate observation added %d points", added)
	}
	// After the reset passes, QuotaWindows reports the same observation as an
	// unused window; it is not a new reading.
	rolled := weeklyQuotaWindow(quotaHistoryNow, 0.9, reset).current(reset.Add(time.Minute))
	if added := h.Observe("a", []QuotaWindow{rolled}, reset.Add(time.Minute)); added != 0 {
		t.Fatalf("rolled-over window added %d points", added)
	}
	if added := observeWeekly(h, "a", quotaHistoryNow.Add(-time.Hour), 0.1, reset); added != 0 {
		t.Fatalf("older observation added %d points", added)
	}
	if points := weeklyPoints(t, h, "a"); len(points) != 1 {
		t.Fatalf("got %d points, want 1", len(points))
	}
}

func TestQuotaHistoryUsedDeltaThreshold(t *testing.T) {
	h := NewQuotaHistory()
	reset := quotaHistoryNow.Add(24 * time.Hour)
	observeWeekly(h, "a", quotaHistoryNow, 0.4, reset)
	if added := observeWeekly(h, "a", quotaHistoryNow.Add(time.Minute), 0.404, reset); added != 0 {
		t.Fatalf("a 0.004 change added %d points", added)
	}
	if added := observeWeekly(h, "a", quotaHistoryNow.Add(2*time.Minute), 0.405, reset); added != 1 {
		t.Fatalf("a 0.005 change added %d points, want 1", added)
	}
	if added := observeWeekly(h, "a", quotaHistoryNow.Add(3*time.Minute), 0.4, reset); added != 1 {
		t.Fatalf("a 0.005 decrease added %d points, want 1", added)
	}
}

func TestQuotaHistoryResetChangeVsJitter(t *testing.T) {
	h := NewQuotaHistory()
	reset := quotaHistoryNow.Add(24 * time.Hour)
	observeWeekly(h, "a", quotaHistoryNow, 0.4, reset)
	if added := observeWeekly(h, "a", quotaHistoryNow.Add(10*time.Minute), 0.4, reset.Add(90*time.Second)); added != 0 {
		t.Fatalf("90s of reset jitter added %d points", added)
	}
	if added := observeWeekly(h, "a", quotaHistoryNow.Add(20*time.Minute), 0.4, reset.Add(3*time.Minute)); added != 1 {
		t.Fatalf("a 3m reset change added %d points, want 1", added)
	}
	if added := observeWeekly(h, "a", quotaHistoryNow.Add(30*time.Minute), 0.4, time.Time{}); added != 1 {
		t.Fatalf("a reset becoming unknown added %d points, want 1", added)
	}
}

func TestQuotaHistoryResetOnlyChangeMinSpacing(t *testing.T) {
	h := NewQuotaHistory()
	reset := quotaHistoryNow.Add(7 * 24 * time.Hour)
	for tick := 0; tick <= 30; tick++ {
		shift := time.Duration(tick) * time.Minute
		observeWeekly(h, "a", quotaHistoryNow.Add(shift), 0, reset.Add(3*shift))
	}
	points := weeklyPoints(t, h, "a")
	if len(points) != 7 {
		t.Fatalf("got %d points over 30 minutes, want 7", len(points))
	}
	for i := 1; i < len(points); i++ {
		if gap := points[i].At.Sub(points[i-1].At); gap < quotaHistoryResetMinGap {
			t.Fatalf("points %d and %d are %s apart, want at least %s", i-1, i, gap, quotaHistoryResetMinGap)
		}
	}
}

func TestQuotaHistoryKeepAliveAfterOneHour(t *testing.T) {
	h := NewQuotaHistory()
	reset := quotaHistoryNow.Add(24 * time.Hour)
	observeWeekly(h, "a", quotaHistoryNow, 0.4, reset)
	if added := observeWeekly(h, "a", quotaHistoryNow.Add(59*time.Minute), 0.4, reset); added != 0 {
		t.Fatalf("an unchanged reading after 59m added %d points", added)
	}
	if added := observeWeekly(h, "a", quotaHistoryNow.Add(time.Hour), 0.4, reset); added != 1 {
		t.Fatalf("an unchanged reading after 1h added %d points, want 1", added)
	}
}

func TestQuotaHistoryClampsFutureObservedAt(t *testing.T) {
	h := NewQuotaHistory()
	reset := quotaHistoryNow.Add(24 * time.Hour)
	h.Observe("near", []QuotaWindow{weeklyQuotaWindow(quotaHistoryNow.Add(time.Minute), 0.4, reset)}, quotaHistoryNow)
	h.Observe("far", []QuotaWindow{weeklyQuotaWindow(quotaHistoryNow.Add(10*time.Minute), 0.4, reset)}, quotaHistoryNow)
	if at := weeklyPoints(t, h, "near")[0].At; !at.Equal(quotaHistoryNow.Add(time.Minute)) {
		t.Fatalf("near-future reading stored at %s, want it unchanged", at)
	}
	if at := weeklyPoints(t, h, "far")[0].At; !at.Equal(quotaHistoryNow) {
		t.Fatalf("far-future reading stored at %s, want now %s", at, quotaHistoryNow)
	}
}

func TestQuotaHistoryIgnoresFiveHourAndScoped(t *testing.T) {
	h := NewQuotaHistory()
	reset := quotaHistoryNow.Add(time.Hour)
	windows := []QuotaWindow{
		{Kind: QuotaWindowFiveHour, Used: 0.5, ResetAt: reset, ObservedAt: quotaHistoryNow},
		{Kind: QuotaWindowWeeklyScoped, Scope: "fable", Used: 0.5, ResetAt: reset, ObservedAt: quotaHistoryNow},
		{Kind: QuotaWindowWeekly, Scope: "fable", Used: 0.5, ResetAt: reset, ObservedAt: quotaHistoryNow},
	}
	if added := h.Observe("a", windows, quotaHistoryNow); added != 0 {
		t.Fatalf("added %d points, want 0", added)
	}
	if series := h.Series("a", time.Time{}); len(series) != 0 {
		t.Fatalf("series = %+v, want none", series)
	}
}

func TestQuotaHistoryCapDropsOldest(t *testing.T) {
	h := NewQuotaHistory()
	reset := quotaHistoryNow.Add(24 * time.Hour)
	for i := range quotaHistoryMaxPoints + 5 {
		observeWeekly(h, "a", quotaHistoryNow.Add(time.Duration(i)*time.Hour), 0.4, reset)
	}
	points := weeklyPoints(t, h, "a")
	if len(points) != quotaHistoryMaxPoints {
		t.Fatalf("got %d points, want %d", len(points), quotaHistoryMaxPoints)
	}
	if !points[0].At.Equal(quotaHistoryNow.Add(5 * time.Hour)) {
		t.Fatalf("oldest point at %s, want %s", points[0].At, quotaHistoryNow.Add(5*time.Hour))
	}
}

func TestQuotaHistorySeriesCarryInAndCopies(t *testing.T) {
	h := NewQuotaHistory()
	reset := quotaHistoryNow.Add(24 * time.Hour)
	for i, used := range []float64{0.1, 0.2, 0.3} {
		observeWeekly(h, "a", quotaHistoryNow.Add(time.Duration(i)*time.Hour), used, reset)
	}

	series := h.Series("a", quotaHistoryNow.Add(90*time.Minute))
	if len(series) != 1 || len(series[0].Points) != 2 {
		t.Fatalf("series = %+v, want the carry-in point and one point after since", series)
	}
	if got := series[0].Points; got[0].Used != 0.2 || got[1].Used != 0.3 {
		t.Fatalf("points = %+v, want used 0.2 then 0.3", got)
	}
	series[0].Points[0].Used = 1
	if again := h.Series("a", quotaHistoryNow.Add(90*time.Minute)); again[0].Points[0].Used != 0.2 {
		t.Fatal("Series returned a slice that aliases the store")
	}

	late := h.Series("a", quotaHistoryNow.Add(24*time.Hour))
	if len(late) != 1 || len(late[0].Points) != 1 || late[0].Points[0].Used != 0.3 {
		t.Fatalf("since after every point = %+v, want only the newest point", late)
	}
	if unknown := h.Series("b", time.Time{}); unknown != nil {
		t.Fatalf("unknown credential = %+v, want nil", unknown)
	}
}

func TestQuotaHistoryCompactDropsExpired(t *testing.T) {
	h := NewQuotaHistory()
	reset := quotaHistoryNow.Add(24 * time.Hour)
	observeWeekly(h, "a", quotaHistoryNow.Add(-31*24*time.Hour), 0.1, reset)
	observeWeekly(h, "a", quotaHistoryNow.Add(-29*24*time.Hour), 0.2, reset)
	observeWeekly(h, "b", quotaHistoryNow.Add(-31*24*time.Hour), 0.1, reset)

	if errCompact := h.Compact(quotaHistoryNow); errCompact != nil {
		t.Fatalf("Compact: %v", errCompact)
	}
	if points := weeklyPoints(t, h, "a"); len(points) != 1 || points[0].Used != 0.2 {
		t.Fatalf("points = %+v, want only the reading inside retention", points)
	}
	if series := h.Series("b", time.Time{}); series != nil {
		t.Fatalf("expired series = %+v, want it dropped", series)
	}
	if len(h.series) != 1 {
		t.Fatalf("store holds %d series, want 1", len(h.series))
	}
}
