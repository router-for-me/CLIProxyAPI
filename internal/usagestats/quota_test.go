package usagestats

import (
	"context"
	"errors"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func configureTestRecorder(t *testing.T) *Store {
	t.Helper()
	if errConfigure := Configure(true, filepath.Join(t.TempDir(), DefaultFileName)); errConfigure != nil {
		t.Fatalf("configure: %v", errConfigure)
	}
	t.Cleanup(func() {
		if errShutdown := Shutdown(); errShutdown != nil {
			t.Errorf("shutdown: %v", errShutdown)
		}
	})
	return Current()
}

func mustQuotaSamples(t *testing.T, store *Store, q Query) QuotaHistory {
	t.Helper()
	history, errHistory := store.QuotaSamples(context.Background(), q)
	if errHistory != nil {
		t.Fatalf("quota samples: %v", errHistory)
	}
	return history
}

func mustRecordQuota(t *testing.T, store *Store, samples ...QuotaSample) {
	t.Helper()
	for _, sample := range samples {
		if errRecord := store.RecordQuotaSample(context.Background(), sample); errRecord != nil {
			t.Fatalf("record quota sample: %v", errRecord)
		}
	}
}

func seriesByWindow(history QuotaHistory) map[string]QuotaSeries {
	out := make(map[string]QuotaSeries, len(history.Series))
	for _, series := range history.Series {
		out[series.Window] = series
	}
	return out
}

func samplePoint(key QuotaSeriesKey, at time.Time, usedPercent float64, resetsAt time.Time) QuotaSample {
	point := QuotaPoint{TimestampMs: at.UnixMilli(), UsedPercent: usedPercent}
	if !resetsAt.IsZero() {
		point.ResetsAtMs = resetsAt.UnixMilli()
	}
	return QuotaSample{QuotaSeriesKey: key, QuotaPoint: point}
}

func headerOf(pairs ...string) http.Header {
	headers := make(http.Header, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		headers.Set(pairs[i], pairs[i+1])
	}
	return headers
}

func TestRecorderSamplesClaudeQuotaHeadersOfEveryAttempt(t *testing.T) {
	store := configureTestRecorder(t)
	// The 429 Anthropic sends when the Fable weekly allowance is used up
	// (upstream issue #7197): no 5h headers, 7d allowed, 7d_oi exhausted.
	rejected := coreusage.Record{
		Provider:    "claude",
		Model:       "claude-fable-5-1",
		AuthIndex:   "auth-1",
		AuthType:    "oauth",
		Source:      "me@example.com",
		RequestedAt: testDay,
		Latency:     800 * time.Millisecond,
		Failed:      true,
		Fail:        coreusage.Failure{StatusCode: http.StatusTooManyRequests},
		ResponseHeaders: headerOf(
			"Anthropic-Ratelimit-Unified-Status", "rejected",
			"Anthropic-Ratelimit-Unified-7d-Status", "allowed",
			"Anthropic-Ratelimit-Unified-7d-Utilization", "0.62",
			"Anthropic-Ratelimit-Unified-7d-Reset", "1794150000",
			"Anthropic-Ratelimit-Unified-7d_oi-Status", "rejected",
			"Anthropic-Ratelimit-Unified-7d_oi-Utilization", "1.0",
			"Anthropic-Ratelimit-Unified-7d_oi-Surpassed-Threshold", "1.0",
			"Anthropic-Ratelimit-Unified-7d_oi-Reset", "1794150000",
			"Anthropic-Ratelimit-Unified-Fallback-Percentage", "0.5",
			"Anthropic-Ratelimit-Unified-Reset", "1794150000",
		),
	}
	recorder{}.HandleUsage(context.Background(), rejected)

	streamed := rejected
	streamed.Model = "claude-opus-5-5"
	streamed.Failed = false
	streamed.Fail = coreusage.Failure{}
	streamed.Stream = true
	streamed.RequestedAt = testDay.Add(time.Minute)
	streamed.TTFT = 400 * time.Millisecond
	streamed.Latency = 9 * time.Second
	streamed.ResponseHeaders = headerOf(
		"Anthropic-Ratelimit-Unified-5h-Utilization", "0.07",
		"Anthropic-Ratelimit-Unified-5h-Reset", "1794000000",
		"Anthropic-Ratelimit-Unified-7d-Utilization", "0.63",
		"Anthropic-Ratelimit-Unified-7d-Reset", "1794150000",
	)
	recorder{}.HandleUsage(context.Background(), streamed)

	withoutAuthIndex := streamed
	withoutAuthIndex.AuthIndex = ""
	withoutAuthIndex.RequestedAt = testDay.Add(2 * time.Minute)
	withoutAuthIndex.ResponseHeaders = headerOf("Anthropic-Ratelimit-Unified-5h-Utilization", "0.50")
	recorder{}.HandleUsage(context.Background(), withoutAuthIndex)

	otherProvider := streamed
	otherProvider.Provider = "gemini"
	otherProvider.RequestedAt = testDay.Add(3 * time.Minute)
	otherProvider.ResponseHeaders = headerOf("Anthropic-Ratelimit-Unified-5h-Utilization", "0.50")
	recorder{}.HandleUsage(context.Background(), otherProvider)

	history := mustQuotaSamples(t, store, Query{From: testDay, To: testDay.Add(time.Hour)})
	byWindow := seriesByWindow(history)
	if len(history.Series) != 3 {
		t.Fatalf("series = %+v, want 5h, 7d and 7d_oi only", history.Series)
	}
	weekReset := time.Unix(1794150000, 0).UnixMilli()
	rejectedAt := testDay.UnixMilli()
	streamedAt := testDay.Add(time.Minute).UnixMilli()
	want := map[string][]QuotaPoint{
		"5h":    {{TimestampMs: streamedAt, UsedPercent: 7, ResetsAtMs: time.Unix(1794000000, 0).UnixMilli()}},
		"7d":    {{TimestampMs: rejectedAt, UsedPercent: 62, ResetsAtMs: weekReset}, {TimestampMs: streamedAt, UsedPercent: 63, ResetsAtMs: weekReset}},
		"7d_oi": {{TimestampMs: rejectedAt, UsedPercent: 100, ResetsAtMs: weekReset}},
	}
	for window, points := range want {
		series := byWindow[window]
		if series.Provider != "claude" || series.AuthIndex != "auth-1" || series.Account != "me@example.com" {
			t.Errorf("%s key = %+v", window, series.QuotaSeriesKey)
		}
		if len(series.Points) != len(points) {
			t.Fatalf("%s points = %+v, want %+v", window, series.Points, points)
		}
		for i := range points {
			if series.Points[i] != points[i] {
				t.Errorf("%s point %d = %+v, want %+v", window, i, series.Points[i], points[i])
			}
		}
	}
}

func TestRecorderDropsTheStartStateOfAStreamThatFinishesLate(t *testing.T) {
	store := configureTestRecorder(t)
	stream := func(requestedAt time.Time, latency time.Duration, utilization string) coreusage.Record {
		return coreusage.Record{
			Provider:        "claude",
			AuthIndex:       "auth-1",
			AuthType:        "oauth",
			Source:          "me@example.com",
			Stream:          true,
			RequestedAt:     requestedAt,
			Latency:         latency,
			ResponseHeaders: headerOf("Anthropic-Ratelimit-Unified-7d-Utilization", utilization),
		}
	}
	// The long stream started first, so its headers describe the window before
	// the two shorter requests reported it.
	long := stream(testDay, 10*time.Minute, "0.10")
	recorder{}.HandleUsage(context.Background(), stream(testDay.Add(time.Minute), time.Second, "0.10"))
	recorder{}.HandleUsage(context.Background(), stream(testDay.Add(2*time.Minute), time.Second, "0.19"))
	recorder{}.HandleUsage(context.Background(), long)

	history := mustQuotaSamples(t, store, Query{From: testDay, To: testDay.Add(time.Hour)})
	points := history.Series[0].Points
	if len(points) != 2 || points[0].UsedPercent != 10 || points[1].UsedPercent != 19 {
		t.Fatalf("points = %+v, want 10%% then 19%% with no stale 10%% after them", points)
	}
}

func TestRecorderMasksKeyAccountsInQuotaSeriesLikeRequests(t *testing.T) {
	store := configureTestRecorder(t)
	recorder{}.HandleUsage(context.Background(), coreusage.Record{
		Provider:        "claude",
		AuthIndex:       "auth-key",
		AuthType:        "apikey",
		Source:          "sk-ant-upstream-secret-key",
		RequestedAt:     testDay,
		ResponseHeaders: headerOf("Anthropic-Ratelimit-Unified-5h-Utilization", "0.10"),
	})
	history := mustQuotaSamples(t, store, Query{From: testDay, To: testDay.Add(time.Hour)})
	aggregate, errAggregate := store.Aggregate(context.Background(), Query{From: testDay, To: testDay.Add(time.Hour), GroupBy: []string{"account"}})
	if errAggregate != nil {
		t.Fatalf("aggregate: %v", errAggregate)
	}
	if len(history.Series) != 1 || len(aggregate.Groups) != 1 {
		t.Fatalf("series = %+v groups = %+v", history.Series, aggregate.Groups)
	}
	if got, want := history.Series[0].Account, aggregate.Groups[0].Dimensions["account"]; got != want || got != "sk-a...-key" {
		t.Fatalf("quota account = %q, request account = %q, want both masked", got, want)
	}
}

func TestQuotaSamplesFromRowReadsCodexWindows(t *testing.T) {
	row := Row{Provider: "codex", AuthIndex: "auth-2", Account: "dev@example.com", Timestamp: testDay, Stream: true, TTFTMs: 2000, LatencyMs: 9000}
	observedAt := testDay
	samples := quotaSamplesFromRow(row, headerOf(
		"X-Codex-Primary-Used-Percent", "48",
		"X-Codex-Primary-Window-Minutes", "300",
		"X-Codex-Primary-Reset-At", "1787231961",
		"X-Codex-Primary-Reset-After-Seconds", "99999",
		"X-Codex-Secondary-Used-Percent", "17.5",
		"X-Codex-Secondary-Window-Minutes", "10080",
		"X-Codex-Secondary-Reset-After-Seconds", "600",
		"X-Codex-Bengalfox-Secondary-Used-Percent", "35",
	))
	want := []QuotaPoint{
		{TimestampMs: observedAt.UnixMilli(), UsedPercent: 48, ResetsAtMs: time.Unix(1787231961, 0).UnixMilli()},
		{TimestampMs: observedAt.UnixMilli(), UsedPercent: 17.5, ResetsAtMs: observedAt.Add(10 * time.Minute).UnixMilli()},
	}
	if len(samples) != 2 || samples[0].Window != "5h" || samples[1].Window != "7d" {
		t.Fatalf("samples = %+v, want the 5h primary and 7d secondary windows only", samples)
	}
	for i := range want {
		if samples[i].QuotaPoint != want[i] || samples[i].Account != "dev@example.com" {
			t.Errorf("sample %d = %+v, want %+v", i, samples[i], want[i])
		}
	}

	for minutes, window := range map[string]string{"60": "1h", "1440": "1d", "90": "90m"} {
		got := quotaSamplesFromRow(row, headerOf("X-Codex-Primary-Used-Percent", "1", "X-Codex-Primary-Window-Minutes", minutes))
		if len(got) != 1 || got[0].Window != window || got[0].ResetsAtMs != 0 {
			t.Errorf("window minutes %q: samples = %+v, want window %q with unknown reset", minutes, got, window)
		}
	}
}

func TestQuotaSamplesFromRowSkipsACodexSlotWithNoLength(t *testing.T) {
	row := Row{Provider: "codex", AuthIndex: "auth-1", Timestamp: testDay}
	// What an account with only a weekly limit sends.
	got := quotaSamplesFromRow(row, headerOf(
		"X-Codex-Primary-Used-Percent", "38",
		"X-Codex-Primary-Window-Minutes", "10080",
		"X-Codex-Primary-Reset-At", "1795290015",
		"X-Codex-Secondary-Used-Percent", "0",
		"X-Codex-Secondary-Reset-After-Seconds", "0",
	))
	if len(got) != 1 || got[0].Window != "7d" || got[0].UsedPercent != 38 {
		t.Fatalf("samples = %+v, want the 7d window only", got)
	}
	for _, minutes := range []string{"", "0", "-5", "soon"} {
		got := quotaSamplesFromRow(row, headerOf("X-Codex-Primary-Used-Percent", "1", "X-Codex-Primary-Window-Minutes", minutes))
		if len(got) != 0 {
			t.Errorf("window minutes %q: samples = %+v, want none", minutes, got)
		}
	}
}

func TestQuotaSamplesFromRowSkipsUnusableValues(t *testing.T) {
	claude := Row{Provider: "claude", AuthIndex: "auth-1", Timestamp: testDay}
	for name, headers := range map[string]http.Header{
		"not a number":        headerOf("Anthropic-Ratelimit-Unified-5h-Utilization", "high"),
		"negative":            headerOf("Anthropic-Ratelimit-Unified-5h-Utilization", "-0.1"),
		"not finite":          headerOf("Anthropic-Ratelimit-Unified-5h-Utilization", "NaN"),
		"window id":           headerOf("Anthropic-Ratelimit-Unified-5 h-Utilization", "0.1"),
		"unwindowed":          headerOf("Anthropic-Ratelimit-Unified-Utilization", "0.1"),
		"status only":         headerOf("Anthropic-Ratelimit-Unified-5h-Status", "allowed"),
		"codex on claude":     headerOf("X-Codex-Primary-Used-Percent", "10"),
		"surpassed threshold": headerOf("Anthropic-Ratelimit-Unified-7d_oi-Surpassed-Threshold", "1.0"),
	} {
		if samples := quotaSamplesFromRow(claude, headers); len(samples) != 0 {
			t.Errorf("%s: samples = %+v, want none", name, samples)
		}
	}
	reset := quotaSamplesFromRow(claude, headerOf("Anthropic-Ratelimit-Unified-5h-Utilization", "0.25", "Anthropic-Ratelimit-Unified-5h-Reset", "soon"))
	if len(reset) != 1 || reset[0].UsedPercent != 25 || reset[0].ResetsAtMs != 0 {
		t.Errorf("unparseable reset: samples = %+v, want 25%% with unknown reset", reset)
	}
}

func TestRecordQuotaSampleStoresOnlyNewerChangedStates(t *testing.T) {
	store := openTestStore(t)
	key := QuotaSeriesKey{Provider: "codex", AuthIndex: "auth-2", Account: "dev@example.com", Window: "7d"}
	reset := testDay.Add(7 * 24 * time.Hour)
	nextReset := reset.Add(7 * 24 * time.Hour)
	mustRecordQuota(t, store,
		samplePoint(key, testDay, 10, reset),
		samplePoint(key, testDay.Add(1*time.Minute), 10, reset),
		samplePoint(key, testDay.Add(2*time.Minute), 10, reset.Add(20*time.Second)),
		samplePoint(key, testDay.Add(3*time.Minute), 11, reset),
		samplePoint(key, testDay.Add(150*time.Second), 12, reset),
		samplePoint(key, testDay.Add(4*time.Minute), 11, nextReset),
		samplePoint(key, testDay.Add(5*time.Minute), 0, nextReset),
		samplePoint(key, testDay.Add(6*time.Minute), 0, time.Time{}),
	)
	history := mustQuotaSamples(t, store, Query{From: testDay, To: testDay.Add(time.Hour)})
	if len(history.Series) != 1 {
		t.Fatalf("series = %+v", history.Series)
	}
	want := []QuotaPoint{
		samplePoint(key, testDay, 10, reset).QuotaPoint,
		samplePoint(key, testDay.Add(3*time.Minute), 11, reset).QuotaPoint,
		samplePoint(key, testDay.Add(4*time.Minute), 11, nextReset).QuotaPoint,
		samplePoint(key, testDay.Add(5*time.Minute), 0, nextReset).QuotaPoint,
	}
	got := history.Series[0].Points
	if len(got) != len(want) {
		t.Fatalf("points = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("point %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestRecordQuotaSampleResumesFromTheDatabaseAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), DefaultFileName)
	key := QuotaSeriesKey{Provider: "claude", AuthIndex: "auth-1", Account: "me@example.com", Window: "5h"}
	reset := testDay.Add(5 * time.Hour)

	first, errOpen := Open(path)
	if errOpen != nil {
		t.Fatalf("open: %v", errOpen)
	}
	mustRecordQuota(t, first, samplePoint(key, testDay, 10, reset), samplePoint(key, testDay.Add(time.Minute), 11, reset))
	if errClose := first.Close(); errClose != nil {
		t.Fatalf("close: %v", errClose)
	}

	second := openTestStoreAt(t, path)
	mustRecordQuota(t, second,
		samplePoint(key, testDay, 10, reset),
		samplePoint(key, testDay.Add(time.Minute), 11, reset),
		samplePoint(key, testDay.Add(2*time.Minute), 11, reset),
		samplePoint(key, testDay.Add(3*time.Minute), 12, reset),
	)
	points := mustQuotaSamples(t, second, Query{From: testDay, To: testDay.Add(time.Hour)}).Series[0].Points
	if len(points) != 3 || points[2].UsedPercent != 12 {
		t.Fatalf("points = %+v, want 10, 11, 12 with no duplicate after the restart", points)
	}
}

func openTestStoreAt(t *testing.T, path string) *Store {
	t.Helper()
	store, errOpen := Open(path)
	if errOpen != nil {
		t.Fatalf("open: %v", errOpen)
	}
	t.Cleanup(func() {
		if errClose := store.Close(); errClose != nil {
			t.Errorf("close store: %v", errClose)
		}
	})
	return store
}

func TestQuotaSamplesCarriesInTheNewestEarlierSample(t *testing.T) {
	store := openTestStore(t)
	from, to := testDay, testDay.Add(24*time.Hour)
	active := QuotaSeriesKey{Provider: "claude", AuthIndex: "auth-1", Account: "me@example.com", Window: "5h"}
	idle := QuotaSeriesKey{Provider: "claude", AuthIndex: "auth-1", Account: "me@example.com", Window: "7d"}
	future := QuotaSeriesKey{Provider: "codex", AuthIndex: "auth-2", Account: "dev@example.com", Window: "5h"}
	mustRecordQuota(t, store,
		samplePoint(active, from.Add(-2*time.Hour), 5, time.Time{}),
		samplePoint(active, from.Add(-time.Hour), 6, time.Time{}),
		samplePoint(active, from.Add(time.Hour), 7, time.Time{}),
		samplePoint(idle, from.Add(-3*time.Hour), 40, time.Time{}),
		samplePoint(future, to, 1, time.Time{}),
	)

	byWindow := seriesByWindow(mustQuotaSamples(t, store, Query{From: from, To: to, Filters: map[string]string{"provider": "claude"}}))
	if len(byWindow) != 2 {
		t.Fatalf("series = %+v, want the active and idle claude windows", byWindow)
	}
	if carry := byWindow["5h"].CarryIn; carry == nil || carry.UsedPercent != 6 || carry.TimestampMs != from.Add(-time.Hour).UnixMilli() {
		t.Errorf("5h carry-in = %+v, want the 6%% sample an hour before the range", carry)
	}
	if points := byWindow["5h"].Points; len(points) != 1 || points[0].UsedPercent != 7 {
		t.Errorf("5h points = %+v, want only the sample in the range", points)
	}
	if idleSeries := byWindow["7d"]; idleSeries.CarryIn == nil || idleSeries.CarryIn.UsedPercent != 40 || len(idleSeries.Points) != 0 {
		t.Errorf("7d series = %+v, want a carry-in and no points", idleSeries)
	}

	filtered := mustQuotaSamples(t, store, Query{From: from, To: to, Filters: map[string]string{"window": "7d", "account": "me@example.com", "auth_index": "auth-1"}})
	if len(filtered.Series) != 1 || filtered.Series[0].Window != "7d" {
		t.Errorf("filtered series = %+v, want the 7d window only", filtered.Series)
	}
	for name, q := range map[string]Query{
		"unknown filter": {From: from, To: to, Filters: map[string]string{"model": "x"}},
		"empty range":    {From: to, To: from},
	} {
		if _, errQuery := store.QuotaSamples(context.Background(), q); !errors.Is(errQuery, ErrInvalidQuery) {
			t.Errorf("%s: error = %v, want ErrInvalidQuery", name, errQuery)
		}
	}
}

func TestQuotaSamplesThinsLongSeriesAndKeepsEveryPeak(t *testing.T) {
	store := openTestStore(t)
	from := testDay
	to := from.Add(10 * 24 * time.Hour)
	sawtooth := QuotaSeriesKey{Provider: "codex", AuthIndex: "auth-2", Account: "dev@example.com", Window: "5h"}
	short := QuotaSeriesKey{Provider: "codex", AuthIndex: "auth-2", Account: "dev@example.com", Window: "7d"}
	const total, period = 3000, 100
	step := (to.Sub(from) - time.Minute) / total
	stored := make(map[QuotaPoint]bool, total)
	for i := range total {
		at := from.Add(time.Duration(i) * step)
		reset := from.Add(time.Duration(i/period+1) * period * step)
		sample := samplePoint(sawtooth, at, float64(i%period), reset)
		stored[sample.QuotaPoint] = true
		mustRecordQuota(t, store, sample)
	}
	mustRecordQuota(t, store, samplePoint(short, from, 3, time.Time{}), samplePoint(short, from.Add(time.Hour), 4, time.Time{}))

	history := mustQuotaSamples(t, store, Query{From: from, To: to})
	byWindow := seriesByWindow(history)
	thinned := byWindow["5h"]
	span := to.UnixMilli() - from.UnixMilli()
	if !thinned.Thinned || history.BucketMs != (span+maxQuotaPoints/2-1)/(maxQuotaPoints/2) {
		t.Fatalf("thinned = %t bucket_ms = %d, want thinning into %d buckets", thinned.Thinned, history.BucketMs, maxQuotaPoints/2)
	}
	if len(thinned.Points) > maxQuotaPoints || len(thinned.Points) < maxQuotaPoints/2 {
		t.Fatalf("thinned to %d points, want between %d and %d", len(thinned.Points), maxQuotaPoints/2, maxQuotaPoints)
	}
	peaks := 0
	for i, point := range thinned.Points {
		if !stored[point] {
			t.Fatalf("point %+v was not recorded", point)
		}
		if i > 0 && point.TimestampMs <= thinned.Points[i-1].TimestampMs {
			t.Fatalf("points out of order at %d", i)
		}
		if point.UsedPercent == period-1 {
			peaks++
		}
	}
	if peaks != total/period {
		t.Errorf("kept %d peaks, want all %d", peaks, total/period)
	}
	lastAt := from.Add(time.Duration(total-1) * step).UnixMilli()
	if last := thinned.Points[len(thinned.Points)-1]; last.TimestampMs != lastAt {
		t.Errorf("last point at %d, want the newest sample at %d", last.TimestampMs, lastAt)
	}
	if byWindow["7d"].Thinned || len(byWindow["7d"].Points) != 2 {
		t.Errorf("short series = %+v, want it untouched", byWindow["7d"])
	}
}
