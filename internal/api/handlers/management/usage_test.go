package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

type usagePluginFunc func(context.Context, coreusage.Record)

func (f usagePluginFunc) HandleUsage(ctx context.Context, record coreusage.Record) {
	f(ctx, record)
}

func TestExportUsageStatisticsFlushesPendingRecords(t *testing.T) {
	gin.SetMode(gin.TestMode)

	stats := usage.GetRequestStatistics()
	before := stats.Snapshot().TotalRequests

	started := make(chan struct{})
	release := make(chan struct{})
	blocked := true
	coreusage.DefaultManager().Register(usagePluginFunc(func(_ context.Context, _ coreusage.Record) {
		if blocked {
			blocked = false
			close(started)
			<-release
		}
	}))

	coreusage.PublishRecord(context.Background(), coreusage.Record{
		APIKey: "export-test",
		Model:  "gpt-5.4",
		Detail: coreusage.Detail{InputTokens: 1, OutputTokens: 2, TotalTokens: 3},
	})
	<-started
	coreusage.PublishRecord(context.Background(), coreusage.Record{
		APIKey: "export-test",
		Model:  "gpt-5.4",
		Detail: coreusage.Detail{InputTokens: 4, OutputTokens: 5, TotalTokens: 9},
	})

	handler := &Handler{usageStats: stats}
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage/export", nil)

	done := make(chan struct{})
	go func() {
		handler.ExportUsageStatistics(ctx)
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("export returned before pending usage records were flushed")
	case <-time.After(30 * time.Millisecond):
	}

	close(release)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("export did not complete after releasing pending usage records")
	}

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload struct {
		Version    int                            `json:"version"`
		Usage      usage.StatisticsSnapshot       `json:"usage"`
		Aggregated *usage.AggregatedUsageSnapshot `json:"aggregated"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if payload.Version != 3 {
		t.Fatalf("version = %d, want 3", payload.Version)
	}
	if got, want := payload.Usage.TotalRequests, before+2; got != want {
		t.Fatalf("total_requests = %d, want %d", got, want)
	}
	if payload.Aggregated == nil {
		t.Fatal("aggregated payload is nil")
	}
}

func TestExportUsageStatisticsReturnsAggregatedPayload(t *testing.T) {
	gin.SetMode(gin.TestMode)

	stats := usage.NewRequestStatistics()
	now := time.Now().UTC()
	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "summary-test",
		Model:       "gpt-5.4",
		RequestedAt: now.Add(-30 * time.Minute),
		Detail: coreusage.Detail{
			InputTokens:  11,
			OutputTokens: 29,
			TotalTokens:  40,
		},
	})
	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "summary-test",
		Model:       "gpt-5.4",
		RequestedAt: now.Add(-3 * time.Hour),
		Detail: coreusage.Detail{
			InputTokens:  5,
			OutputTokens: 7,
			TotalTokens:  12,
		},
		Failed: true,
	})

	handler := &Handler{usageStats: stats}
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage/export", nil)

	handler.ExportUsageStatistics(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload struct {
		Version    int                            `json:"version"`
		Usage      usage.StatisticsSnapshot       `json:"usage"`
		Aggregated *usage.AggregatedUsageSnapshot `json:"aggregated"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if payload.Version != 3 {
		t.Fatalf("version = %d, want 3", payload.Version)
	}

	model := payload.Usage.APIs["summary-test"].Models["gpt-5.4"]
	if got := len(model.Details); got != 0 {
		t.Fatalf("details len = %d, want 0", got)
	}
	if payload.Aggregated == nil {
		t.Fatal("aggregated payload is nil")
	}
	if got := payload.Aggregated.Windows["1h"].TotalRequests; got != 1 {
		t.Fatalf("aggregated 1h total_requests = %d, want 1", got)
	}
	if got := payload.Aggregated.Windows["all"].TotalRequests; got != 2 {
		t.Fatalf("aggregated all total_requests = %d, want 2", got)
	}
	if got := payload.Aggregated.Windows["all"].FailureCount; got != 1 {
		t.Fatalf("aggregated all failure_count = %d, want 1", got)
	}
}

func TestGetUsageStatisticsReturnsSummaryPayload(t *testing.T) {
	gin.SetMode(gin.TestMode)

	stats := usage.NewRequestStatistics()
	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "summary-test",
		Model:       "gpt-5.4",
		RequestedAt: time.Date(2026, 4, 10, 13, 0, 0, 0, time.UTC),
		Latency:     1500 * time.Millisecond,
		Detail: coreusage.Detail{
			InputTokens:     11,
			OutputTokens:    29,
			ReasoningTokens: 3,
			CachedTokens:    2,
			TotalTokens:     43,
		},
	})

	handler := &Handler{usageStats: stats}
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage", nil)

	handler.GetUsageStatistics(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload struct {
		Usage usage.StatisticsSnapshot `json:"usage"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	model := payload.Usage.APIs["summary-test"].Models["gpt-5.4"]
	if got := len(model.Details); got != 0 {
		t.Fatalf("details len = %d, want 0", got)
	}
	if model.TokenBreakdown.InputTokens != 11 || model.TokenBreakdown.OutputTokens != 29 || model.TokenBreakdown.ReasoningTokens != 3 || model.TokenBreakdown.CachedTokens != 2 || model.TokenBreakdown.TotalTokens != 43 {
		t.Fatalf("token breakdown = %+v, want input=11 output=29 reasoning=3 cached=2 total=43", model.TokenBreakdown)
	}
	if model.Latency.Count != 1 || model.Latency.TotalMs != 1500 || model.Latency.MinMs != 1500 || model.Latency.MaxMs != 1500 {
		t.Fatalf("latency summary = %+v, want count=1 total=min=max=1500", model.Latency)
	}
}

func TestGetDetailedUsageStatisticsReturnsDetails(t *testing.T) {
	gin.SetMode(gin.TestMode)

	stats := usage.NewRequestStatistics()
	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "detail-test",
		Model:       "gpt-5.4",
		RequestedAt: time.Date(2026, 4, 10, 13, 0, 0, 0, time.UTC),
		Detail: coreusage.Detail{
			InputTokens:  11,
			OutputTokens: 29,
			TotalTokens:  40,
		},
	})

	handler := &Handler{usageStats: stats}
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage/details", nil)

	handler.GetDetailedUsageStatistics(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload struct {
		Usage usage.StatisticsSnapshot `json:"usage"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	model := payload.Usage.APIs["detail-test"].Models["gpt-5.4"]
	if got := len(model.Details); got != 1 {
		t.Fatalf("details len = %d, want 1", got)
	}
}

func TestGetAggregatedUsageStatisticsReturnsWindowedPayload(t *testing.T) {
	gin.SetMode(gin.TestMode)

	stats := usage.NewRequestStatistics()
	now := time.Now().UTC()
	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "aggregate-test",
		Model:       "gpt-5.4",
		RequestedAt: now.Add(-20 * time.Minute),
		Latency:     800 * time.Millisecond,
		Detail: coreusage.Detail{
			InputTokens:     10,
			OutputTokens:    20,
			ReasoningTokens: 5,
			CachedTokens:    2,
			TotalTokens:     37,
		},
	})
	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "aggregate-test",
		Model:       "gpt-5.4",
		RequestedAt: now.Add(-2 * time.Hour),
		Detail: coreusage.Detail{
			InputTokens:  3,
			OutputTokens: 7,
			TotalTokens:  10,
		},
		Failed: true,
	})

	handler := &Handler{usageStats: stats}
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage/aggregated", nil)

	handler.GetAggregatedUsageStatistics(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload struct {
		Usage usage.AggregatedUsageSnapshot `json:"usage"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	if got := payload.Usage.Windows["1h"].TotalRequests; got != 1 {
		t.Fatalf("1h total_requests = %d, want 1", got)
	}
	if got := payload.Usage.Windows["24h"].TotalRequests; got != 2 {
		t.Fatalf("24h total_requests = %d, want 2", got)
	}
	if got := payload.Usage.Windows["all"].FailureCount; got != 1 {
		t.Fatalf("all failure_count = %d, want 1", got)
	}
	if got := payload.Usage.Windows["1h"].Rate30m.RequestCount; got != 1 {
		t.Fatalf("1h rate_30m request_count = %d, want 1", got)
	}
	if got := len(payload.Usage.Windows["1h"].Sparklines.Timestamps); got != 60 {
		t.Fatalf("1h sparkline timestamps len = %d, want 60", got)
	}
}

func TestExportDetailedUsageStatisticsIncludesDetails(t *testing.T) {
	gin.SetMode(gin.TestMode)

	stats := usage.NewRequestStatistics()
	stats.Record(context.Background(), coreusage.Record{
		APIKey:      "detail-export-test",
		Model:       "gpt-5.4",
		RequestedAt: time.Date(2026, 4, 10, 13, 0, 0, 0, time.UTC),
		Detail: coreusage.Detail{
			InputTokens:  11,
			OutputTokens: 29,
			TotalTokens:  40,
		},
	})

	handler := &Handler{usageStats: stats}
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage/export/details", nil)

	handler.ExportDetailedUsageStatistics(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload struct {
		Version int                      `json:"version"`
		Usage   usage.StatisticsSnapshot `json:"usage"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if payload.Version != 3 {
		t.Fatalf("version = %d, want 3", payload.Version)
	}

	model := payload.Usage.APIs["detail-export-test"].Models["gpt-5.4"]
	if got := len(model.Details); got != 1 {
		t.Fatalf("details len = %d, want 1", got)
	}
}
