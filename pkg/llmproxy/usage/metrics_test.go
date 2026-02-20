package usage

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

func TestGetProviderMetrics_Empty(t *testing.T) {
	got := GetProviderMetrics()
	if got == nil {
		t.Fatal("expected non-nil map")
	}
	if len(got) != 0 {
		t.Errorf("expected empty map with no usage, got %d providers", len(got))
	}
}

func TestGetProviderMetrics_JSONRoundtrip(t *testing.T) {
	got := GetProviderMetrics()
	// Ensure result is JSON-serializable (used by GET /v1/metrics/providers)
	_, err := json.Marshal(got)
	if err != nil {
		t.Errorf("GetProviderMetrics result must be JSON-serializable: %v", err)
	}
}

func TestKnownProviders(t *testing.T) {
	for p := range knownProviders {
		if p == "" {
			t.Error("empty known provider")
		}
	}
}

func TestFallbackCost(t *testing.T) {
	for p, cost := range fallbackCostPer1k {
		if cost <= 0 {
			t.Errorf("invalid cost for %s: %f", p, cost)
		}
	}
}

func TestGetProviderMetrics_WithUsage(t *testing.T) {
	stats := GetRequestStatistics()
	ctx := context.Background()
	
	// Use a known provider like 'claude'
	record := coreusage.Record{
		Provider: "claude",
		APIKey:   "claude", 
		Model:    "claude-3-sonnet",
		Detail: coreusage.Detail{
			TotalTokens: 1000,
		},
		Failed: false,
	}
	stats.Record(ctx, record)

	// Add a failure
	failRecord := coreusage.Record{
		Provider: "claude",
		APIKey:   "claude", 
		Model:    "claude-3-sonnet",
		Failed: true,
	}
	stats.Record(ctx, failRecord)
	
	metrics := GetProviderMetrics()
	m, ok := metrics["claude"]
	if !ok {
		t.Errorf("claude metrics not found")
		return
	}
	
	if m.RequestCount < 2 {
		t.Errorf("expected at least 2 requests, got %d", m.RequestCount)
	}
	if m.FailureCount < 1 {
		t.Errorf("expected at least 1 failure, got %d", m.FailureCount)
	}
	if m.SuccessCount < 1 {
		t.Errorf("expected at least 1 success, got %d", m.SuccessCount)
	}
}

func TestLoggerPlugin(t *testing.T) {
	plugin := NewLoggerPlugin()
	if plugin == nil {
		t.Fatal("NewLoggerPlugin returned nil")
	}
	
	ctx := context.Background()
	record := coreusage.Record{Model: "test"}
	
	SetStatisticsEnabled(false)
	if StatisticsEnabled() {
		t.Error("expected statistics disabled")
	}
	plugin.HandleUsage(ctx, record)
	
	SetStatisticsEnabled(true)
	if !StatisticsEnabled() {
		t.Error("expected statistics enabled")
	}
	plugin.HandleUsage(ctx, record)
}

func TestRequestStatistics_MergeSnapshot(t *testing.T) {
	s := NewRequestStatistics()
	
	snap := StatisticsSnapshot{
		APIs: map[string]APISnapshot{
			"api1": {
				Models: map[string]ModelSnapshot{
					"m1": {
						Details: []RequestDetail{
							{
								Timestamp: time.Now(),
								Tokens: TokenStats{InputTokens: 10, OutputTokens: 5},
								Failed: false,
							},
						},
					},
				},
			},
		},
	}
	
	res := s.MergeSnapshot(snap)
	if res.Added != 1 {
		t.Errorf("expected 1 added, got %d", res.Added)
	}
	
	// Test deduplication
	res2 := s.MergeSnapshot(snap)
	if res2.Skipped != 1 {
		t.Errorf("expected 1 skipped, got %d", res2.Skipped)
	}
}

func TestRequestStatistics_Snapshot(t *testing.T) {
	s := NewRequestStatistics()
	s.Record(context.Background(), coreusage.Record{
		APIKey: "api1",
		Model: "m1",
		Detail: coreusage.Detail{InputTokens: 10},
	})
	
	snap := s.Snapshot()
	if snap.TotalRequests != 1 {
		t.Errorf("expected 1 total request, got %d", snap.TotalRequests)
	}
	if _, ok := snap.APIs["api1"]; !ok {
		t.Error("api1 not found in snapshot")
	}
}
