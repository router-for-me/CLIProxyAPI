package usagestats

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestStoreReportAggregatesFiltersAndSanitizes(t *testing.T) {
	withUsageStatisticsEnabled(t, true)

	now := time.Now().Truncate(time.Second)
	store := NewStore(100, 90*24*time.Hour)
	store.HandleUsage(context.Background(), coreusage.Record{
		Provider:     "codex",
		ExecutorType: "CodexExecutor",
		Model:        "gpt-5",
		Alias:        "gpt-primary",
		APIKey:       "must-not-appear",
		AuthID:       "secret-auth-id",
		AuthType:     "oauth",
		RequestedAt:  now.Add(-time.Hour),
		Latency:      100 * time.Millisecond,
		Detail: coreusage.Detail{
			InputTokens:  100,
			OutputTokens: 50,
			TotalTokens:  150,
		},
	})
	store.HandleUsage(context.Background(), coreusage.Record{
		Provider:    "codex",
		Model:       "gpt-5",
		RequestedAt: now.Add(-30 * time.Minute),
		Latency:     300 * time.Millisecond,
		Failed:      true,
		Fail:        coreusage.Failure{StatusCode: 429},
		Detail:      coreusage.Detail{InputTokens: 20, TotalTokens: 20},
	})
	store.HandleUsage(context.Background(), coreusage.Record{
		Provider:    "claude",
		Model:       "claude-sonnet",
		RequestedAt: now.Add(-24 * time.Hour),
		Latency:     200 * time.Millisecond,
		Detail: coreusage.Detail{
			InputTokens:     200,
			OutputTokens:    80,
			ReasoningTokens: 20,
		},
	})

	report := store.Report(QueryOptions{Days: 2, ModelLimit: 10, RecentLimit: 10}, now)
	if report.Summary.Requests != 3 || report.Summary.Successful != 2 || report.Summary.Failed != 1 {
		t.Fatalf("summary requests = %d/%d/%d, want 3/2/1", report.Summary.Requests, report.Summary.Successful, report.Summary.Failed)
	}
	if report.Summary.SuccessRate != float64(2)*100/3 {
		t.Fatalf("success rate = %v, want %v", report.Summary.SuccessRate, float64(2)*100/3)
	}
	if report.Summary.ActiveProviders != 2 || report.Summary.ActiveModels != 2 {
		t.Fatalf("active providers/models = %d/%d, want 2/2", report.Summary.ActiveProviders, report.Summary.ActiveModels)
	}
	if report.Summary.AverageLatencyMs != 200 {
		t.Fatalf("average latency = %v, want 200", report.Summary.AverageLatencyMs)
	}
	if report.Summary.Tokens.TotalTokens != 470 {
		t.Fatalf("total tokens = %d, want 470", report.Summary.Tokens.TotalTokens)
	}
	if len(report.Providers) != 2 || report.Providers[0].Provider != "codex" || report.Providers[0].Requests != 2 {
		t.Fatalf("providers = %#v, want codex first with two requests", report.Providers)
	}
	if len(report.Models) != 2 || report.Models[0].Provider != "claude" || report.Models[0].Tokens.TotalTokens != 300 {
		t.Fatalf("models = %#v, want claude model first with 300 tokens", report.Models)
	}
	if len(report.Trend) != 2 || report.Trend[0].Requests != 1 || report.Trend[1].Requests != 2 {
		t.Fatalf("trend = %#v, want previous/current requests 1/2", report.Trend)
	}
	if len(report.Recent) != 3 || !report.Recent[0].Failed || report.Recent[0].StatusCode != 429 {
		t.Fatalf("recent = %#v, want failed 429 request first", report.Recent)
	}

	encoded, errMarshal := json.Marshal(report)
	if errMarshal != nil {
		t.Fatalf("marshal report: %v", errMarshal)
	}
	if strings.Contains(string(encoded), "must-not-appear") || strings.Contains(string(encoded), "secret-auth-id") {
		t.Fatalf("report leaked credential material: %s", encoded)
	}

	filtered := store.Report(QueryOptions{Days: 2, Provider: "CLAUDE", ModelLimit: 10, RecentLimit: 10}, now)
	if filtered.Summary.Requests != 1 || filtered.Summary.ActiveProviders != 1 || len(filtered.Providers) != 1 {
		t.Fatalf("filtered report = %#v, want one claude request", filtered)
	}
}

func TestStoreEnforcesAgeAndCapacity(t *testing.T) {
	withUsageStatisticsEnabled(t, true)

	now := time.Now().Truncate(time.Second)
	store := NewStore(2, time.Hour)
	for index, model := range []string{"expired", "first", "second", "third"} {
		requestedAt := now.Add(time.Duration(index-1) * time.Minute)
		if model == "expired" {
			requestedAt = now.Add(-2 * time.Hour)
		}
		store.HandleUsage(context.Background(), coreusage.Record{
			Provider:    "provider",
			Model:       model,
			RequestedAt: requestedAt,
		})
	}

	report := store.Report(QueryOptions{Days: 1, ModelLimit: 10, RecentLimit: 10}, now.Add(3*time.Minute))
	if report.Summary.Requests != 2 {
		t.Fatalf("requests = %d, want bounded history of 2", report.Summary.Requests)
	}
	if len(report.Recent) != 2 || report.Recent[0].Model != "third" || report.Recent[1].Model != "second" {
		t.Fatalf("recent = %#v, want newest two retained events", report.Recent)
	}
}

func TestStoreRespectsUsageStatisticsToggle(t *testing.T) {
	withUsageStatisticsEnabled(t, false)

	now := time.Now()
	store := NewStore(10, time.Hour)
	store.HandleUsage(context.Background(), coreusage.Record{Provider: "codex", Model: "gpt-5", RequestedAt: now})
	report := store.Report(QueryOptions{Days: 1}, now)
	if report.Summary.Requests != 0 {
		t.Fatalf("requests = %d, want disabled statistics to ignore usage", report.Summary.Requests)
	}
}

func withUsageStatisticsEnabled(t *testing.T, enabled bool) {
	t.Helper()
	previous := redisqueue.UsageStatisticsEnabled()
	redisqueue.SetUsageStatisticsEnabled(enabled)
	t.Cleanup(func() {
		redisqueue.SetUsageStatisticsEnabled(previous)
	})
}
