package usage

import (
	"testing"
	"time"
)

func TestRequestStatisticsMonthlyTokensForAPIModel(t *testing.T) {
	stats := NewRequestStatistics()

	stats.MergeSnapshot(StatisticsSnapshot{
		APIs: map[string]APISnapshot{
			"quota-key": {
				Models: map[string]ModelSnapshot{
					"claude-sonnet-4-5": {
						Details: []RequestDetail{
							{Timestamp: time.Date(2026, 3, 5, 12, 0, 0, 0, time.UTC), Tokens: TokenStats{TotalTokens: 300}},
							{Timestamp: time.Date(2026, 3, 7, 12, 0, 0, 0, time.UTC), Tokens: TokenStats{InputTokens: 100, OutputTokens: 100}},
							{Timestamp: time.Date(2026, 2, 28, 23, 59, 59, 0, time.UTC), Tokens: TokenStats{TotalTokens: 900}},
						},
					},
				},
			},
		},
	})

	now := time.Date(2026, 3, 11, 0, 0, 0, 0, time.UTC)
	got := stats.MonthlyTokensForAPIModel("quota-key", "claude-sonnet-4-5", now)
	if got != 500 {
		t.Fatalf("monthly tokens = %d, want 500", got)
	}
}
