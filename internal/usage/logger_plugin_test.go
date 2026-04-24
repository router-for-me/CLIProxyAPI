package usage

import (
	"context"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

func TestRequestStatisticsRecordIncludesLatency(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "test-key",
		Model:       "gpt-5.4",
		RequestedAt: time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC),
		Latency:     1500 * time.Millisecond,
		Detail: coreusage.Detail{
			InputTokens:  10,
			OutputTokens: 20,
			TotalTokens:  30,
		},
	})

	snapshot := stats.Snapshot()
	details := snapshot.APIs["test-key"].Models["gpt-5.4"].Details
	if len(details) != 1 {
		t.Fatalf("details len = %d, want 1", len(details))
	}
	if details[0].LatencyMs != 1500 {
		t.Fatalf("latency_ms = %d, want 1500", details[0].LatencyMs)
	}

	model := snapshot.APIs["test-key"].Models["gpt-5.4"]
	if model.TokenBreakdown.InputTokens != 10 || model.TokenBreakdown.OutputTokens != 20 || model.TokenBreakdown.TotalTokens != 30 {
		t.Fatalf("token breakdown = %+v, want input=10 output=20 total=30", model.TokenBreakdown)
	}
	if model.Latency.Count != 1 || model.Latency.TotalMs != 1500 || model.Latency.MinMs != 1500 || model.Latency.MaxMs != 1500 {
		t.Fatalf("latency summary = %+v, want count=1 total=min=max=1500", model.Latency)
	}
}

func TestRequestStatisticsMergeSnapshotDedupIgnoresLatency(t *testing.T) {
	stats := NewRequestStatistics()
	timestamp := time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC)
	first := StatisticsSnapshot{
		APIs: map[string]APISnapshot{
			"test-key": {
				Models: map[string]ModelSnapshot{
					"gpt-5.4": {
						Details: []RequestDetail{{
							Timestamp: timestamp,
							LatencyMs: 0,
							Source:    "user@example.com",
							AuthIndex: "0",
							Tokens: TokenStats{
								InputTokens:  10,
								OutputTokens: 20,
								TotalTokens:  30,
							},
						}},
					},
				},
			},
		},
	}
	second := StatisticsSnapshot{
		APIs: map[string]APISnapshot{
			"test-key": {
				Models: map[string]ModelSnapshot{
					"gpt-5.4": {
						Details: []RequestDetail{{
							Timestamp: timestamp,
							LatencyMs: 2500,
							Source:    "user@example.com",
							AuthIndex: "0",
							Tokens: TokenStats{
								InputTokens:  10,
								OutputTokens: 20,
								TotalTokens:  30,
							},
						}},
					},
				},
			},
		},
	}

	result := stats.MergeSnapshot(first)
	if result.Added != 1 || result.Skipped != 0 {
		t.Fatalf("first merge = %+v, want added=1 skipped=0", result)
	}

	result = stats.MergeSnapshot(second)
	if result.Added != 0 || result.Skipped != 1 {
		t.Fatalf("second merge = %+v, want added=0 skipped=1", result)
	}

	snapshot := stats.Snapshot()
	details := snapshot.APIs["test-key"].Models["gpt-5.4"].Details
	if len(details) != 1 {
		t.Fatalf("details len = %d, want 1", len(details))
	}
}

func TestRequestStatisticsRetainsAllDetails(t *testing.T) {
	stats := NewRequestStatistics()
	start := time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC)

	const count = 517
	for i := 0; i < count; i++ {
		stats.Record(context.Background(), coreusage.Record{
			APIKey:      "test-key",
			Model:       "gpt-5.4",
			RequestedAt: start.Add(time.Duration(i) * time.Second),
			Detail: coreusage.Detail{
				InputTokens:  1,
				OutputTokens: 1,
				TotalTokens:  2,
			},
		})
	}

	snapshot := stats.Snapshot()
	model := snapshot.APIs["test-key"].Models["gpt-5.4"]
	if model.TotalRequests != int64(count) {
		t.Fatalf("total requests = %d, want %d", model.TotalRequests, count)
	}
	if len(model.Details) != count {
		t.Fatalf("details len = %d, want %d", len(model.Details), count)
	}

	wantFirst := start
	if !model.Details[0].Timestamp.Equal(wantFirst) {
		t.Fatalf("first retained timestamp = %s, want %s", model.Details[0].Timestamp, wantFirst)
	}
	wantLast := start.Add(time.Duration(count-1) * time.Second)
	if !model.Details[len(model.Details)-1].Timestamp.Equal(wantLast) {
		t.Fatalf("last retained timestamp = %s, want %s", model.Details[len(model.Details)-1].Timestamp, wantLast)
	}
}

func TestRequestStatisticsMergeSnapshotSummaryOnly(t *testing.T) {
	stats := NewRequestStatistics()
	summary := StatisticsSnapshot{
		TotalRequests: 5,
		SuccessCount:  4,
		FailureCount:  1,
		TotalTokens:   500,
		APIs: map[string]APISnapshot{
			"summary-api": {
				TotalRequests: 5,
				TotalTokens:   500,
				Models: map[string]ModelSnapshot{
					"gpt-5.4": {
						TotalRequests:  5,
						TotalTokens:    500,
						TokenBreakdown: TokenStats{InputTokens: 200, OutputTokens: 250, ReasoningTokens: 50, TotalTokens: 500},
						Latency:        LatencyStats{Count: 4, TotalMs: 1600, MinMs: 250, MaxMs: 550},
					},
				},
			},
		},
		RequestsByDay: map[string]int64{
			"2026-04-10": 5,
		},
		RequestsByHour: map[string]int64{
			"13": 5,
		},
		TokensByDay: map[string]int64{
			"2026-04-10": 500,
		},
		TokensByHour: map[string]int64{
			"13": 500,
		},
	}

	result := stats.MergeSnapshot(summary)
	if result.Added != 5 || result.Skipped != 0 {
		t.Fatalf("merge result = %+v, want added=5 skipped=0", result)
	}

	snapshot := stats.Snapshot()
	if snapshot.TotalRequests != 5 {
		t.Fatalf("total requests = %d, want 5", snapshot.TotalRequests)
	}
	if snapshot.SuccessCount != 4 {
		t.Fatalf("success count = %d, want 4", snapshot.SuccessCount)
	}
	if snapshot.FailureCount != 1 {
		t.Fatalf("failure count = %d, want 1", snapshot.FailureCount)
	}
	if snapshot.TotalTokens != 500 {
		t.Fatalf("total tokens = %d, want 500", snapshot.TotalTokens)
	}

	api := snapshot.APIs["summary-api"]
	if api.TotalRequests != 5 || api.TotalTokens != 500 {
		t.Fatalf("api totals = %+v, want requests=5 tokens=500", api)
	}
	model := api.Models["gpt-5.4"]
	if model.TotalRequests != 5 || model.TotalTokens != 500 {
		t.Fatalf("model totals = %+v, want requests=5 tokens=500", model)
	}
	if model.TokenBreakdown.InputTokens != 200 || model.TokenBreakdown.OutputTokens != 250 || model.TokenBreakdown.ReasoningTokens != 50 || model.TokenBreakdown.TotalTokens != 500 {
		t.Fatalf("token breakdown = %+v, want input=200 output=250 reasoning=50 total=500", model.TokenBreakdown)
	}
	if model.Latency.Count != 4 || model.Latency.TotalMs != 1600 || model.Latency.MinMs != 250 || model.Latency.MaxMs != 550 {
		t.Fatalf("latency summary = %+v, want count=4 total=1600 min=250 max=550", model.Latency)
	}
	if len(model.Details) != 0 {
		t.Fatalf("details len = %d, want 0", len(model.Details))
	}

	if got := snapshot.RequestsByDay["2026-04-10"]; got != 5 {
		t.Fatalf("requests_by_day[2026-04-10] = %d, want 5", got)
	}
	if got := snapshot.RequestsByHour["13"]; got != 5 {
		t.Fatalf("requests_by_hour[13] = %d, want 5", got)
	}
	if got := snapshot.TokensByDay["2026-04-10"]; got != 500 {
		t.Fatalf("tokens_by_day[2026-04-10] = %d, want 500", got)
	}
	if got := snapshot.TokensByHour["13"]; got != 500 {
		t.Fatalf("tokens_by_hour[13] = %d, want 500", got)
	}
}

func TestSnapshotSummaryOmitsDetailsButPreservesAggregates(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "summary-test",
		Model:       "gpt-5.4",
		RequestedAt: time.Date(2026, 4, 10, 13, 0, 0, 0, time.UTC),
		Latency:     1200 * time.Millisecond,
		Detail: coreusage.Detail{
			InputTokens:     10,
			OutputTokens:    20,
			ReasoningTokens: 5,
			CachedTokens:    2,
			TotalTokens:     35,
		},
	})
	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "summary-test",
		Model:       "gpt-5.4",
		RequestedAt: time.Date(2026, 4, 10, 13, 1, 0, 0, time.UTC),
		Latency:     800 * time.Millisecond,
		Detail: coreusage.Detail{
			InputTokens:  3,
			OutputTokens: 7,
			TotalTokens:  10,
		},
	})

	snapshot := stats.SnapshotSummary()
	model := snapshot.APIs["summary-test"].Models["gpt-5.4"]

	if len(model.Details) != 0 {
		t.Fatalf("details len = %d, want 0", len(model.Details))
	}
	if model.TokenBreakdown.InputTokens != 13 || model.TokenBreakdown.OutputTokens != 27 || model.TokenBreakdown.ReasoningTokens != 5 || model.TokenBreakdown.CachedTokens != 2 || model.TokenBreakdown.TotalTokens != 45 {
		t.Fatalf("token breakdown = %+v, want input=13 output=27 reasoning=5 cached=2 total=45", model.TokenBreakdown)
	}
	if model.Latency.Count != 2 || model.Latency.TotalMs != 2000 || model.Latency.MinMs != 800 || model.Latency.MaxMs != 1200 {
		t.Fatalf("latency summary = %+v, want count=2 total=2000 min=800 max=1200", model.Latency)
	}
}
