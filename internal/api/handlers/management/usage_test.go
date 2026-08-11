package management

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
	internalusage "github.com/router-for-me/CLIProxyAPI/v7/internal/usage"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestGetUsageQueuePopsRequestedRecords(t *testing.T) {
	withManagementUsageQueue(t, func() {
		redisqueue.Enqueue([]byte(`{"id":1}`))
		redisqueue.Enqueue([]byte(`{"id":2}`))
		redisqueue.Enqueue([]byte(`{"id":3}`))

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage-queue?count=2", nil)

		h := &Handler{}
		h.GetUsageQueue(ginCtx)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
		}

		var payload []json.RawMessage
		if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &payload); errUnmarshal != nil {
			t.Fatalf("unmarshal response: %v", errUnmarshal)
		}
		if len(payload) != 2 {
			t.Fatalf("response records = %d, want 2", len(payload))
		}
		requireRecordID(t, payload[0], 1)
		requireRecordID(t, payload[1], 2)

		remaining := redisqueue.PopOldest(10)
		if len(remaining) != 1 || string(remaining[0]) != `{"id":3}` {
			t.Fatalf("remaining queue = %q, want third item only", remaining)
		}
	})
}

func TestGetUsageQueueInvalidCountDoesNotPop(t *testing.T) {
	withManagementUsageQueue(t, func() {
		redisqueue.Enqueue([]byte(`{"id":1}`))

		rec := httptest.NewRecorder()
		ginCtx, _ := gin.CreateTestContext(rec)
		ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage-queue?count=0", nil)

		h := &Handler{}
		h.GetUsageQueue(ginCtx)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
		}

		remaining := redisqueue.PopOldest(10)
		if len(remaining) != 1 || string(remaining[0]) != `{"id":1}` {
			t.Fatalf("remaining queue = %q, want original item", remaining)
		}
	})
}

func withManagementUsageQueue(t *testing.T, fn func()) {
	t.Helper()

	prevQueueEnabled := redisqueue.Enabled()
	redisqueue.SetEnabled(false)
	redisqueue.SetEnabled(true)

	defer func() {
		redisqueue.SetEnabled(false)
		redisqueue.SetEnabled(prevQueueEnabled)
	}()

	fn()
}

func requireRecordID(t *testing.T, raw json.RawMessage, want int) {
	t.Helper()

	var payload struct {
		ID int `json:"id"`
	}
	if errUnmarshal := json.Unmarshal(raw, &payload); errUnmarshal != nil {
		t.Fatalf("unmarshal record: %v", errUnmarshal)
	}
	if payload.ID != want {
		t.Fatalf("record id = %d, want %d", payload.ID, want)
	}
}

func TestGetUsageStatisticsFiltersByClientKey(t *testing.T) {
	now := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	collector := newManagementUsageCollector(now, "USD", "rates")
	collector.HandleUsage(context.Background(), managementUsageRecord(now, "key-a", "Team A", "gpt-5", "secret-key-a"))
	collector.HandleUsage(context.Background(), managementUsageRecord(now, "key-b", "Team B", "gpt-4", "secret-key-b"))
	h := &Handler{}
	h.SetUsageCollector(collector)

	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodGet, "/usage?key_id=key-b&from=2026-08-05&to=2026-08-05", nil)
	h.GetUsageStatistics(ginCtx)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
	var report internalusage.Report
	if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &report); errUnmarshal != nil {
		t.Fatalf("json.Unmarshal() error = %v", errUnmarshal)
	}
	if report.Summary.Attempts != 1 || len(report.Keys) != 1 || report.Keys[0].KeyID != "key-b" {
		t.Fatalf("filtered report = %#v", report)
	}
}

func TestUsageStatisticsRejectsInvalidDateFilter(t *testing.T) {
	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodGet, "/usage?from=not-a-date", nil)
	(&Handler{}).GetUsageStatistics(ginCtx)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 body=%s", rec.Code, rec.Body.String())
	}
}

func TestResetClientKeyUsageClearsAllClientAggregates(t *testing.T) {
	now := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	collector := newManagementUsageCollector(now, "USD", "rates")
	collector.HandleUsage(context.Background(), managementUsageRecord(now, "key-a", "Team A", "gpt-5", "secret-key-a"))
	collector.HandleUsage(context.Background(), managementUsageRecord(now, "key-b", "Team B", "gpt-4", "secret-key-b"))
	h := &Handler{}
	h.SetUsageCollector(collector)

	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/client-key-usage/reset", nil)
	h.ResetClientKeyUsage(ginCtx)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		ResetCount int `json:"reset_count"`
	}
	if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &payload); errUnmarshal != nil {
		t.Fatalf("json.Unmarshal() error = %v", errUnmarshal)
	}
	if payload.ResetCount != 2 {
		t.Fatalf("reset count = %d, want 2", payload.ResetCount)
	}
	if report := collector.Report(internalusage.Filter{}); report.Summary.Attempts != 0 || len(report.Keys) != 0 {
		t.Fatalf("report after reset = %#v", report)
	}
}

func TestExportUsageStatisticsJSONIsFilteredAndSecretSafe(t *testing.T) {
	now := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	collector := newManagementUsageCollector(now, "USD", "rates")
	collector.HandleUsage(context.Background(), managementUsageRecord(now, "key-a", "Team A", "gpt-5", "secret-key-a"))
	collector.HandleUsage(context.Background(), managementUsageRecord(now, "key-b", "Team B", "gpt-4", "secret-key-b"))
	h := &Handler{}
	h.SetUsageCollector(collector)

	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodGet, "/usage/export?key_id=key-a", nil)
	h.ExportUsageStatistics(ginCtx)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
	var snapshot internalusage.Snapshot
	if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &snapshot); errUnmarshal != nil {
		t.Fatalf("json.Unmarshal() error = %v", errUnmarshal)
	}
	if snapshot.Version != internalusage.SnapshotVersion || len(snapshot.Buckets) != 1 || snapshot.Buckets[0].ClientKeyID != "key-a" {
		t.Fatalf("filtered snapshot = %#v", snapshot)
	}
	if strings.Contains(rec.Body.String(), "secret-key-a") || strings.Contains(rec.Body.String(), "secret-key-b") {
		t.Fatalf("JSON export leaked raw key: %s", rec.Body.String())
	}
}

func TestExportUsageCSVPreventsFormulaInjection(t *testing.T) {
	now := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	collector := newManagementUsageCollector(now, "+USD", "=rates")
	collector.HandleUsage(context.Background(), managementUsageRecord(now, "key-a", "=SUM(1,1)", "gpt-5", "secret-key-a"))
	h := &Handler{}
	h.SetUsageCollector(collector)

	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodGet, "/usage/export.csv", nil)
	h.ExportUsageCSV(ginCtx)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
	rows, errRead := csv.NewReader(strings.NewReader(rec.Body.String())).ReadAll()
	if errRead != nil {
		t.Fatalf("csv.ReadAll() error = %v", errRead)
	}
	if len(rows) != 2 {
		t.Fatalf("CSV rows = %d, want 2: %q", len(rows), rows)
	}
	header := make(map[string]int, len(rows[0]))
	for index, name := range rows[0] {
		header[name] = index
	}
	for _, column := range []string{"alias", "pricing_versions", "estimated_cost_micros_by_currency"} {
		value := rows[1][header[column]]
		if !strings.HasPrefix(value, "'") {
			t.Fatalf("CSV %s cell = %q, want apostrophe prefix", column, value)
		}
	}
	if strings.Contains(rec.Body.String(), "secret-key-a") {
		t.Fatalf("CSV export leaked raw key: %s", rec.Body.String())
	}
}

func TestExportUsageCSVIncludesOverflowRow(t *testing.T) {
	now := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	cfg := &config.Config{
		UsageStatisticsEnabled:       true,
		UsageStatisticsRetentionDays: 90,
	}
	collector := internalusage.NewCollectorWithOptions(cfg, internalusage.CollectorOptions{
		MaxBuckets: 1, Now: func() time.Time { return now },
	})
	collector.HandleUsage(context.Background(), managementUsageRecord(now, "key-a", "", "gpt-5", "secret-key-a"))
	collector.HandleUsage(context.Background(), managementUsageRecord(now, "key-b", "", "gpt-4", "secret-key-b"))
	h := &Handler{}
	h.SetUsageCollector(collector)

	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodGet, "/usage/export.csv", nil)
	h.ExportUsageCSV(ginCtx)
	rows, errRead := csv.NewReader(strings.NewReader(rec.Body.String())).ReadAll()
	if errRead != nil {
		t.Fatalf("csv.ReadAll() error = %v", errRead)
	}
	if len(rows) != 3 {
		t.Fatalf("CSV rows = %d, want header + bucket + overflow: %q", len(rows), rows)
	}
	header := make(map[string]int, len(rows[0]))
	for index, name := range rows[0] {
		header[name] = index
	}
	if rows[2][header["is_overflow"]] != "true" || rows[2][header["overflow_attempts"]] != "1" || rows[2][header["attempts"]] != "1" {
		t.Fatalf("overflow CSV row = %q", rows[2])
	}
}

func newManagementUsageCollector(now time.Time, currency, version string) *internalusage.Collector {
	cfg := &config.Config{
		UsageStatisticsEnabled:       true,
		UsageStatisticsRetentionDays: 90,
		UsagePricing: config.UsagePricingConfig{
			Currency: currency,
			Version:  version,
			Rules: []config.UsagePricingRule{{
				Provider: "*", Model: "*", ServiceTier: "*",
				InputPerMillion: "1", OutputPerMillion: "1",
			}},
		},
	}
	return internalusage.NewCollectorWithOptions(cfg, internalusage.CollectorOptions{Now: func() time.Time { return now }})
}

func managementUsageRecord(at time.Time, keyID, alias, model, rawKey string) coreusage.Record {
	return coreusage.Record{
		Provider: "openai", Model: model, ClientKeyID: keyID, ClientKeyAlias: alias,
		APIKey: rawKey, RequestedAt: at,
		Detail: coreusage.Detail{InputTokens: 10, OutputTokens: 2, TotalTokens: 12},
	}
}
