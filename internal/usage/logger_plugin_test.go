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

func TestRequestStatisticsSnapshotIncludesProviderModelSummariesAndIdentifiers(t *testing.T) {
	stats := NewRequestStatistics()
	timestamp := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)

	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "api-key-a",
		Provider:    "openai",
		Model:       "gpt-5.4",
		RequestedAt: timestamp,
		Detail: coreusage.Detail{
			InputTokens:  10,
			OutputTokens: 20,
			TotalTokens:  30,
		},
	})
	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "api-key-b",
		Provider:    "anthropic",
		Model:       "claude-sonnet",
		RequestedAt: timestamp.Add(time.Hour),
		Detail: coreusage.Detail{
			InputTokens:  7,
			OutputTokens: 8,
			TotalTokens:  15,
		},
	})

	snapshot := stats.Snapshot()
	if snapshot.Providers["openai"].TotalRequests != 1 || snapshot.Providers["openai"].TotalTokens != 30 {
		t.Fatalf("openai summary = %+v, want requests=1 tokens=30", snapshot.Providers["openai"])
	}
	if snapshot.Models["claude-sonnet"].TotalRequests != 1 || snapshot.Models["claude-sonnet"].TotalTokens != 15 {
		t.Fatalf("claude model summary = %+v, want requests=1 tokens=15", snapshot.Models["claude-sonnet"])
	}

	detail := snapshot.APIs["api-key-a"].Models["gpt-5.4"].Details[0]
	if detail.Provider != "openai" || detail.Model != "gpt-5.4" || detail.APIKey != "api-key-a" {
		t.Fatalf("detail identifiers = provider %q model %q api key %q", detail.Provider, detail.Model, detail.APIKey)
	}
}

func TestRequestStatisticsOnChangeFiresAfterRecordMergeAndReset(t *testing.T) {
	stats := NewRequestStatistics()
	var snapshots []StatisticsSnapshot
	stats.SetOnChange(func(snapshot StatisticsSnapshot) {
		snapshots = append(snapshots, snapshot)
	})

	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "api-key",
		Provider:    "openai",
		Model:       "gpt-5.4",
		RequestedAt: time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
		Detail:      coreusage.Detail{TotalTokens: 10},
	})
	stats.MergeSnapshot(StatisticsSnapshot{APIs: map[string]APISnapshot{
		"api-key": {
			Models: map[string]ModelSnapshot{
				"gpt-5.4": {Details: []RequestDetail{{
					Timestamp: time.Date(2026, 4, 1, 11, 0, 0, 0, time.UTC),
					Provider:  "openai",
					Model:     "gpt-5.4",
					APIKey:    "api-key",
					Tokens:    TokenStats{TotalTokens: 20},
				}}},
			},
		},
	}})
	stats.ResetByModel("gpt-5.4")

	if len(snapshots) != 3 {
		t.Fatalf("callback count = %d, want 3", len(snapshots))
	}
	if snapshots[0].TotalRequests != 1 || snapshots[1].TotalRequests != 2 || snapshots[2].TotalRequests != 0 {
		t.Fatalf("callback totals = %d, %d, %d; want 1, 2, 0", snapshots[0].TotalRequests, snapshots[1].TotalRequests, snapshots[2].TotalRequests)
	}
}

func TestRequestStatisticsScopedResetsRebuildTotalsFromRemainingDetails(t *testing.T) {
	stats := NewRequestStatistics()
	record := func(apiKey, provider, model string, tokens int64, failed bool) {
		stats.Record(context.Background(), coreusage.Record{
			APIKey:      apiKey,
			Provider:    provider,
			Model:       model,
			Failed:      failed,
			RequestedAt: time.Date(2026, 4, 1, 10+int(tokens%3), 0, 0, 0, time.UTC),
			Detail:      coreusage.Detail{TotalTokens: tokens},
		})
	}

	record("api-key-a", "openai", "gpt-5.4", 10, false)
	record("api-key-b", "openai", "gpt-4o", 20, true)
	record("api-key-c", "anthropic", "claude-sonnet", 30, false)
	record("api-key-d", "gemini", "gemini-pro", 40, false)

	if removed := stats.ResetByProvider("openai"); removed != 2 {
		t.Fatalf("ResetByProvider removed = %d, want 2", removed)
	}
	snapshot := stats.Snapshot()
	if snapshot.TotalRequests != 2 || snapshot.SuccessCount != 2 || snapshot.FailureCount != 0 || snapshot.TotalTokens != 70 {
		t.Fatalf("after provider reset totals = %+v, want requests=2 success=2 failure=0 tokens=70", snapshot)
	}
	if _, ok := snapshot.Providers["openai"]; ok {
		t.Fatal("openai provider summary still exists after provider reset")
	}

	if removed := stats.ResetByAPIKey("api-key-c"); removed != 1 {
		t.Fatalf("ResetByAPIKey removed = %d, want 1", removed)
	}
	snapshot = stats.Snapshot()
	if snapshot.TotalRequests != 1 || snapshot.TotalTokens != 40 {
		t.Fatalf("after api key reset totals = requests %d tokens %d, want requests=1 tokens=40", snapshot.TotalRequests, snapshot.TotalTokens)
	}
	if _, ok := snapshot.APIs["api-key-c"]; ok {
		t.Fatal("api-key-c summary still exists after api key reset")
	}

	if removed := stats.ResetByModel("gemini-pro"); removed != 1 {
		t.Fatalf("ResetByModel removed = %d, want 1", removed)
	}
	if snapshot = stats.Snapshot(); snapshot.TotalRequests != 0 || snapshot.TotalTokens != 0 || len(snapshot.APIs) != 0 {
		t.Fatalf("after model reset snapshot = %+v, want empty", snapshot)
	}
}

func TestRequestStatisticsResetAllClearsDataAndTriggersCallback(t *testing.T) {
	stats := NewRequestStatistics()
	var callbackSnapshot StatisticsSnapshot
	stats.SetOnChange(func(snapshot StatisticsSnapshot) {
		callbackSnapshot = snapshot
	})

	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "api-key",
		Provider:    "openai",
		Model:       "gpt-5.4",
		RequestedAt: time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
		Detail:      coreusage.Detail{TotalTokens: 10},
	})
	stats.ResetAll()

	snapshot := stats.Snapshot()
	if snapshot.TotalRequests != 0 || snapshot.TotalTokens != 0 || len(snapshot.APIs) != 0 {
		t.Fatalf("snapshot after ResetAll = %+v, want empty", snapshot)
	}
	if callbackSnapshot.TotalRequests != 0 || callbackSnapshot.TotalTokens != 0 || len(callbackSnapshot.APIs) != 0 {
		t.Fatalf("callback snapshot after ResetAll = %+v, want empty", callbackSnapshot)
	}
}
