package management

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
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

func TestExportUsageStatisticsIncludesStableSourceID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	handler := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	handler.SetUsageStatistics(usage.NewRequestStatistics())

	exportOnce := func() string {
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage/export", nil)
		handler.ExportUsageStatistics(ctx)

		if rec.Code != http.StatusOK {
			t.Fatalf("export status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
		}

		var payload struct {
			SourceID string `json:"source_id"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("unmarshal export response: %v", err)
		}
		if payload.SourceID == "" {
			t.Fatal("source_id is empty")
		}
		return payload.SourceID
	}

	first := exportOnce()
	second := exportOnce()
	if first != second {
		t.Fatalf("source_id changed across exports: %q != %q", first, second)
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

func TestImportDetailedUsageStatisticsRestoresTrimmedSummaryTotals(t *testing.T) {
	gin.SetMode(gin.TestMode)

	previousLimit := usage.DetailRetentionLimit()
	usage.SetDetailRetentionLimit(1)
	t.Cleanup(func() { usage.SetDetailRetentionLimit(previousLimit) })

	sourceStats := usage.NewRequestStatistics()
	now := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	sourceStats.Record(context.Background(), coreusage.Record{
		APIKey:      "detail-export-test",
		Model:       "gpt-5.4",
		RequestedAt: now.Add(-2 * time.Hour),
		Latency:     900 * time.Millisecond,
		Detail: coreusage.Detail{
			InputTokens:  3,
			OutputTokens: 4,
			TotalTokens:  7,
		},
	})
	sourceStats.Record(context.Background(), coreusage.Record{
		APIKey:      "detail-export-test",
		Model:       "gpt-5.4",
		RequestedAt: now.Add(-time.Hour),
		Latency:     1100 * time.Millisecond,
		Detail: coreusage.Detail{
			InputTokens:  5,
			OutputTokens: 6,
			TotalTokens:  11,
		},
	})

	exportHandler := &Handler{usageStats: sourceStats}
	exportRec := httptest.NewRecorder()
	exportCtx, _ := gin.CreateTestContext(exportRec)
	exportCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage/export/details", nil)
	exportHandler.ExportDetailedUsageStatistics(exportCtx)

	if exportRec.Code != http.StatusOK {
		t.Fatalf("export status = %d, want %d, body=%s", exportRec.Code, http.StatusOK, exportRec.Body.String())
	}

	var exportPayload usageExportPayload
	if err := json.Unmarshal(exportRec.Body.Bytes(), &exportPayload); err != nil {
		t.Fatalf("unmarshal export response: %v", err)
	}
	if got := len(exportPayload.Usage.APIs["detail-export-test"].Models["gpt-5.4"].Details); got != 1 {
		t.Fatalf("exported details len = %d, want 1 after retention trim", got)
	}
	if exportPayload.Usage.TotalRequests != 2 {
		t.Fatalf("exported total_requests = %d, want 2", exportPayload.Usage.TotalRequests)
	}

	importStats := usage.NewRequestStatistics()
	importHandler := &Handler{usageStats: importStats}
	importRec := httptest.NewRecorder()
	importCtx, _ := gin.CreateTestContext(importRec)
	importCtx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/usage/import", bytes.NewReader(exportRec.Body.Bytes()))
	importHandler.ImportUsageStatistics(importCtx)

	if importRec.Code != http.StatusOK {
		t.Fatalf("import status = %d, want %d, body=%s", importRec.Code, http.StatusOK, importRec.Body.String())
	}

	summary := importStats.SnapshotSummary()
	if summary.TotalRequests != 2 {
		t.Fatalf("imported total_requests = %d, want 2", summary.TotalRequests)
	}
	if summary.TotalTokens != 18 {
		t.Fatalf("imported total_tokens = %d, want 18", summary.TotalTokens)
	}

	aggregated := importStats.AggregatedUsageSnapshot(now)
	if got := aggregated.Windows["all"].TotalRequests; got != 2 {
		t.Fatalf("aggregated all total_requests = %d, want 2", got)
	}
	if got := aggregated.Windows["all"].TotalTokens; got != 18 {
		t.Fatalf("aggregated all total_tokens = %d, want 18", got)
	}
}

func TestImportDetailedUsageStatisticsUpsertsSameSourceIDUnderRetention(t *testing.T) {
	gin.SetMode(gin.TestMode)

	previousLimit := usage.DetailRetentionLimit()
	usage.SetDetailRetentionLimit(1)
	t.Cleanup(func() { usage.SetDetailRetentionLimit(previousLimit) })

	authDir := t.TempDir()
	exportHandler := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: authDir}, nil)

	exportBody := func(records ...coreusage.Record) []byte {
		stats := usage.NewRequestStatistics()
		for _, record := range records {
			stats.Record(context.Background(), record)
		}
		exportHandler.SetUsageStatistics(stats)

		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage/export/details", nil)
		exportHandler.ExportDetailedUsageStatistics(ctx)
		if rec.Code != http.StatusOK {
			t.Fatalf("export status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
		}
		return append([]byte(nil), rec.Body.Bytes()...)
	}

	t1 := time.Date(2026, 4, 26, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 4, 26, 11, 0, 0, 0, time.UTC)
	firstExport := exportBody(coreusage.Record{
		APIKey:      "detail-upsert-test",
		Model:       "gpt-5.4",
		RequestedAt: t1,
		Latency:     900 * time.Millisecond,
		Detail: coreusage.Detail{
			InputTokens:  3,
			OutputTokens: 4,
			TotalTokens:  7,
		},
	})
	secondExport := exportBody(
		coreusage.Record{
			APIKey:      "detail-upsert-test",
			Model:       "gpt-5.4",
			RequestedAt: t1,
			Latency:     900 * time.Millisecond,
			Detail: coreusage.Detail{
				InputTokens:  3,
				OutputTokens: 4,
				TotalTokens:  7,
			},
		},
		coreusage.Record{
			APIKey:      "detail-upsert-test",
			Model:       "gpt-5.4",
			RequestedAt: t2,
			Latency:     1100 * time.Millisecond,
			Detail: coreusage.Detail{
				InputTokens:  5,
				OutputTokens: 6,
				TotalTokens:  11,
			},
		},
	)

	importStats := usage.NewRequestStatistics()
	importHandler := &Handler{usageStats: importStats}

	importPayload := func(body []byte) map[string]int64 {
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/usage/import", bytes.NewReader(body))
		importHandler.ImportUsageStatistics(ctx)
		if rec.Code != http.StatusOK {
			t.Fatalf("import status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
		}
		var payload map[string]int64
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("unmarshal import response: %v", err)
		}
		return payload
	}

	firstImport := importPayload(firstExport)
	if firstImport["added"] != 1 || firstImport["replaced"] != 0 || firstImport["skipped"] != 0 {
		t.Fatalf("first import response = %#v, want added=1 replaced=0 skipped=0", firstImport)
	}

	secondImport := importPayload(secondExport)
	if secondImport["added"] != 2 || secondImport["replaced"] != 1 || secondImport["skipped"] != 0 {
		t.Fatalf("second import response = %#v, want added=2 replaced=1 skipped=0", secondImport)
	}

	thirdImport := importPayload(secondExport)
	if thirdImport["added"] != 0 || thirdImport["replaced"] != 0 || thirdImport["skipped"] != 2 {
		t.Fatalf("third import response = %#v, want added=0 replaced=0 skipped=2", thirdImport)
	}

	summary := importStats.SnapshotSummary()
	if summary.TotalRequests != 2 {
		t.Fatalf("summary total_requests = %d, want 2", summary.TotalRequests)
	}
	if summary.TotalTokens != 18 {
		t.Fatalf("summary total_tokens = %d, want 18", summary.TotalTokens)
	}

	detailed := importStats.Snapshot()
	model := detailed.APIs["detail-upsert-test"].Models["gpt-5.4"]
	if model.TotalRequests != 2 {
		t.Fatalf("detailed model total_requests = %d, want 2", model.TotalRequests)
	}
	if got := len(model.Details); got != 1 {
		t.Fatalf("detailed model details len = %d, want 1 after retention trim", got)
	}
	if got := model.Details[0].Timestamp; !got.Equal(t2) {
		t.Fatalf("retained detail timestamp = %s, want %s", got, t2)
	}

	aggregated := importStats.AggregatedUsageSnapshot(t2.Add(30 * time.Minute))
	if got := aggregated.Windows["all"].TotalRequests; got != 2 {
		t.Fatalf("aggregated all total_requests = %d, want 2", got)
	}
	if got := aggregated.Windows["all"].TotalTokens; got != 18 {
		t.Fatalf("aggregated all total_tokens = %d, want 18", got)
	}
}

func TestImportUsageStatisticsPreservesImportedAllWindowOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)

	sourceStats := usage.NewRequestStatistics()
	now := time.Now().UTC()
	sourceStats.Record(context.Background(), coreusage.Record{
		APIKey:      "import-aggregate-test",
		Model:       "gpt-5.4",
		RequestedAt: now.Add(-20 * time.Minute),
		Latency:     900 * time.Millisecond,
		Detail: coreusage.Detail{
			InputTokens:     11,
			OutputTokens:    29,
			ReasoningTokens: 3,
			CachedTokens:    2,
			TotalTokens:     45,
		},
	})

	exportHandler := &Handler{usageStats: sourceStats}
	exportRec := httptest.NewRecorder()
	exportCtx, _ := gin.CreateTestContext(exportRec)
	exportCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage/export", nil)
	exportHandler.ExportUsageStatistics(exportCtx)

	if exportRec.Code != http.StatusOK {
		t.Fatalf("export status = %d, want %d, body=%s", exportRec.Code, http.StatusOK, exportRec.Body.String())
	}

	importStats := usage.NewRequestStatistics()
	importHandler := &Handler{usageStats: importStats}
	importRec := httptest.NewRecorder()
	importCtx, _ := gin.CreateTestContext(importRec)
	importCtx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/usage/import", exportRec.Body)
	importHandler.ImportUsageStatistics(importCtx)

	if importRec.Code != http.StatusOK {
		t.Fatalf("import status = %d, want %d, body=%s", importRec.Code, http.StatusOK, importRec.Body.String())
	}

	aggregatedRec := httptest.NewRecorder()
	aggregatedCtx, _ := gin.CreateTestContext(aggregatedRec)
	aggregatedCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage/aggregated", nil)
	importHandler.GetAggregatedUsageStatistics(aggregatedCtx)

	if aggregatedRec.Code != http.StatusOK {
		t.Fatalf("aggregated status = %d, want %d, body=%s", aggregatedRec.Code, http.StatusOK, aggregatedRec.Body.String())
	}

	var payload struct {
		Usage usage.AggregatedUsageSnapshot `json:"usage"`
	}
	if err := json.Unmarshal(aggregatedRec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal aggregated response: %v", err)
	}

	if got := payload.Usage.Windows["1h"].TotalRequests; got != 0 {
		t.Fatalf("1h total_requests = %d, want 0", got)
	}
	if got := payload.Usage.Windows["all"].TotalRequests; got != 1 {
		t.Fatalf("all total_requests = %d, want 1", got)
	}
	if got := payload.Usage.Windows["all"].TotalTokens; got != 45 {
		t.Fatalf("all total_tokens = %d, want 45", got)
	}
	if got := payload.Usage.Windows["all"].Latency.Count; got != 1 {
		t.Fatalf("all latency count = %d, want 1", got)
	}
}

func TestImportUsageStatisticsDuplicateExportIsIdempotent(t *testing.T) {
	gin.SetMode(gin.TestMode)

	sourceStats := usage.NewRequestStatistics()
	now := time.Now().UTC()
	sourceStats.Record(context.Background(), coreusage.Record{
		APIKey:      "duplicate-import-test",
		Model:       "gpt-5.4",
		RequestedAt: now.Add(-20 * time.Minute),
		Latency:     800 * time.Millisecond,
		Detail: coreusage.Detail{
			InputTokens:     7,
			OutputTokens:    18,
			ReasoningTokens: 2,
			CachedTokens:    1,
			TotalTokens:     28,
		},
	})

	exportHandler := &Handler{usageStats: sourceStats}
	exportRec := httptest.NewRecorder()
	exportCtx, _ := gin.CreateTestContext(exportRec)
	exportCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage/export", nil)
	exportHandler.ExportUsageStatistics(exportCtx)

	if exportRec.Code != http.StatusOK {
		t.Fatalf("export status = %d, want %d, body=%s", exportRec.Code, http.StatusOK, exportRec.Body.String())
	}

	importStats := usage.NewRequestStatistics()
	importHandler := &Handler{usageStats: importStats}

	for i := 0; i < 2; i++ {
		importRec := httptest.NewRecorder()
		importCtx, _ := gin.CreateTestContext(importRec)
		importCtx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/usage/import", bytes.NewReader(exportRec.Body.Bytes()))
		importHandler.ImportUsageStatistics(importCtx)

		if importRec.Code != http.StatusOK {
			t.Fatalf("import %d status = %d, want %d, body=%s", i+1, importRec.Code, http.StatusOK, importRec.Body.String())
		}

		var payload struct {
			Added   int64 `json:"added"`
			Skipped int64 `json:"skipped"`
		}
		if err := json.Unmarshal(importRec.Body.Bytes(), &payload); err != nil {
			t.Fatalf("unmarshal import %d response: %v", i+1, err)
		}
		if i == 0 {
			if payload.Added != 1 || payload.Skipped != 0 {
				t.Fatalf("first import = %+v, want added=1 skipped=0", payload)
			}
			continue
		}
		if payload.Added != 0 || payload.Skipped != 1 {
			t.Fatalf("second import = %+v, want added=0 skipped=1", payload)
		}
	}

	summary := importStats.SnapshotSummary()
	if summary.TotalRequests != 1 {
		t.Fatalf("summary total_requests = %d, want 1", summary.TotalRequests)
	}
	if summary.TotalTokens != 28 {
		t.Fatalf("summary total_tokens = %d, want 28", summary.TotalTokens)
	}

	aggregated := importStats.AggregatedUsageSnapshot(now)
	if got := aggregated.Windows["all"].TotalRequests; got != 1 {
		t.Fatalf("aggregated all total_requests = %d, want 1", got)
	}
	if got := aggregated.Windows["all"].TotalTokens; got != 28 {
		t.Fatalf("aggregated all total_tokens = %d, want 28", got)
	}
}

func TestImportUsageStatisticsUpsertsSameSourceID(t *testing.T) {
	gin.SetMode(gin.TestMode)

	stats := usage.NewRequestStatistics()
	handler := &Handler{usageStats: stats}

	importPayload := func(totalRequests, totalTokens int64) map[string]int64 {
		payload := usageImportPayload{
			Version:  3,
			SourceID: "source-node-1",
			Usage: usage.StatisticsSnapshot{
				TotalRequests: totalRequests,
				SuccessCount:  totalRequests,
				TotalTokens:   totalTokens,
				APIs: map[string]usage.APISnapshot{
					"summary-api": {
						TotalRequests: totalRequests,
						TotalTokens:   totalTokens,
						Models: map[string]usage.ModelSnapshot{
							"gpt-5.4": {
								TotalRequests: totalRequests,
								TotalTokens:   totalTokens,
								TokenBreakdown: usage.TokenStats{
									InputTokens:  totalTokens / 2,
									OutputTokens: totalTokens - (totalTokens / 2),
									TotalTokens:  totalTokens,
								},
							},
						},
					},
				},
				RequestsByDay: map[string]int64{"2026-04-26": totalRequests},
				RequestsByHour: map[string]int64{
					"12": totalRequests,
				},
				TokensByDay: map[string]int64{"2026-04-26": totalTokens},
				TokensByHour: map[string]int64{
					"12": totalTokens,
				},
			},
			Aggregated: &usage.AggregatedUsageSnapshot{
				GeneratedAt: time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC),
				Windows: map[string]usage.AggregatedUsageWindow{
					"all": {
						TotalRequests: totalRequests,
						SuccessCount:  totalRequests,
						TotalTokens:   totalTokens,
						ModelNames:    []string{"gpt-5.4"},
					},
				},
			},
		}
		body, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal payload: %v", err)
		}

		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/usage/import", bytes.NewReader(body))
		handler.ImportUsageStatistics(ctx)
		if rec.Code != http.StatusOK {
			t.Fatalf("import status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
		}
		var response map[string]int64
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatalf("unmarshal import response: %v", err)
		}
		return response
	}

	first := importPayload(1, 20)
	if first["added"] != 1 || first["replaced"] != 0 || first["skipped"] != 0 {
		t.Fatalf("first import response = %#v, want added=1 replaced=0 skipped=0", first)
	}

	second := importPayload(2, 40)
	if second["added"] != 2 || second["replaced"] != 1 || second["skipped"] != 0 {
		t.Fatalf("second import response = %#v, want added=2 replaced=1 skipped=0", second)
	}

	summary := stats.SnapshotSummary()
	if summary.TotalRequests != 2 {
		t.Fatalf("summary total_requests = %d, want 2", summary.TotalRequests)
	}
	if summary.TotalTokens != 40 {
		t.Fatalf("summary total_tokens = %d, want 40", summary.TotalTokens)
	}

	aggregated := stats.AggregatedUsageSnapshot(time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC))
	if got := aggregated.Windows["all"].TotalRequests; got != 2 {
		t.Fatalf("aggregated all total_requests = %d, want 2", got)
	}
	if got := aggregated.Windows["all"].TotalTokens; got != 40 {
		t.Fatalf("aggregated all total_tokens = %d, want 40", got)
	}
}

func TestImportUsageStatisticsSkipsAggregatedWhenDetailsPresent(t *testing.T) {
	gin.SetMode(gin.TestMode)

	timestamp := time.Date(2026, 4, 26, 12, 0, 0, 0, time.UTC)
	payload := usageExportPayload{
		Version: 3,
		Usage: usage.StatisticsSnapshot{
			APIs: map[string]usage.APISnapshot{
				"detail-import-test": {
					Models: map[string]usage.ModelSnapshot{
						"gpt-5.4": {
							Details: []usage.RequestDetail{{
								Timestamp: timestamp,
								Tokens: usage.TokenStats{
									InputTokens:  2,
									OutputTokens: 3,
									TotalTokens:  5,
								},
							}},
						},
					},
				},
			},
		},
		Aggregated: &usage.AggregatedUsageSnapshot{
			GeneratedAt: timestamp,
			Windows: map[string]usage.AggregatedUsageWindow{
				"all": {
					TotalRequests: 1,
					TotalTokens:   5,
				},
			},
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}

	stats := usage.NewRequestStatistics()
	handler := &Handler{usageStats: stats}
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/usage/import", bytes.NewReader(body))
	handler.ImportUsageStatistics(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("import status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	snapshot := stats.AggregatedUsageSnapshot(timestamp)
	if got := snapshot.Windows["all"].TotalRequests; got != 1 {
		t.Fatalf("all total_requests = %d, want 1", got)
	}
	if got := snapshot.Windows["all"].TotalTokens; got != 5 {
		t.Fatalf("all total_tokens = %d, want 5", got)
	}
}
