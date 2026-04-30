package usage

import (
	"context"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

func TestRequestStatisticsRecordIncludesLatency(t *testing.T) {
	stats := NewRequestStatistics()
	thinkingDetail := &coreusage.Thinking{
		Intensity: "high",
		Mode:      "level",
		Level:     "high",
	}
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
		Thinking: thinkingDetail,
	})
	thinkingDetail.Intensity = "mutated"

	snapshot := stats.Snapshot()
	details := snapshot.APIs["test-key"].Models["gpt-5.4"].Details
	if len(details) != 1 {
		t.Fatalf("details len = %d, want 1", len(details))
	}
	if details[0].LatencyMs != 1500 {
		t.Fatalf("latency_ms = %d, want 1500", details[0].LatencyMs)
	}
	if details[0].Thinking == nil {
		t.Fatal("thinking metadata is nil, want populated")
	}
	if details[0].Thinking.Intensity != "high" {
		t.Fatalf("thinking intensity = %q, want high", details[0].Thinking.Intensity)
	}
}

func TestRequestStatisticsSnapshotDeepCopiesThinking(t *testing.T) {
	stats := NewRequestStatistics()
	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "test-key",
		Model:       "gpt-5.4",
		RequestedAt: time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC),
		Thinking: &coreusage.Thinking{
			Intensity: "medium",
			Mode:      "level",
			Level:     "medium",
		},
		Detail: coreusage.Detail{TotalTokens: 1},
	})

	snapshot := stats.Snapshot()
	snapshot.APIs["test-key"].Models["gpt-5.4"].Details[0].Thinking.Intensity = "mutated"

	nextSnapshot := stats.Snapshot()
	got := nextSnapshot.APIs["test-key"].Models["gpt-5.4"].Details[0].Thinking.Intensity
	if got != "medium" {
		t.Fatalf("thinking intensity = %q after snapshot mutation, want medium", got)
	}
}

func TestRequestStatisticsMergeSnapshotDeepCopiesThinking(t *testing.T) {
	stats := NewRequestStatistics()
	thinkingDetail := &coreusage.Thinking{
		Intensity: "medium",
		Mode:      "level",
		Level:     "medium",
	}
	snapshot := StatisticsSnapshot{
		APIs: map[string]APISnapshot{
			"test-key": {
				Models: map[string]ModelSnapshot{
					"gpt-5.4": {
						Details: []RequestDetail{{
							Timestamp: time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC),
							Thinking:  thinkingDetail,
							Tokens:    TokenStats{TotalTokens: 1},
						}},
					},
				},
			},
		},
	}

	result := stats.MergeSnapshot(snapshot)
	if result.Added != 1 || result.Skipped != 0 {
		t.Fatalf("merge = %+v, want added=1 skipped=0", result)
	}
	thinkingDetail.Intensity = "mutated"

	nextSnapshot := stats.Snapshot()
	got := nextSnapshot.APIs["test-key"].Models["gpt-5.4"].Details[0].Thinking.Intensity
	if got != "medium" {
		t.Fatalf("thinking intensity = %q after source snapshot mutation, want medium", got)
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

func TestRequestStatisticsMergeSnapshotDedupIgnoresThinking(t *testing.T) {
	stats := NewRequestStatistics()
	timestamp := time.Date(2026, 3, 20, 12, 0, 0, 0, time.UTC)
	baseDetail := RequestDetail{
		Timestamp: timestamp,
		Source:    "user@example.com",
		AuthIndex: "0",
		Tokens: TokenStats{
			InputTokens:  10,
			OutputTokens: 20,
			TotalTokens:  30,
		},
	}
	first := StatisticsSnapshot{
		APIs: map[string]APISnapshot{
			"test-key": {
				Models: map[string]ModelSnapshot{
					"gpt-5.4": {
						Details: []RequestDetail{baseDetail},
					},
				},
			},
		},
	}
	withThinking := baseDetail
	withThinking.Thinking = &coreusage.Thinking{
		Intensity: "high",
		Mode:      "level",
		Level:     "high",
	}
	second := StatisticsSnapshot{
		APIs: map[string]APISnapshot{
			"test-key": {
				Models: map[string]ModelSnapshot{
					"gpt-5.4": {
						Details: []RequestDetail{withThinking},
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
