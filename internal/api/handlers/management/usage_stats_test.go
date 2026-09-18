package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usagestats"
)

func TestGetUsageStats_ReturnsAggregatedRows(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	logsDir := t.TempDir()
	statsDir := usagestats.Dir(logsDir)
	if err := os.MkdirAll(statsDir, 0o755); err != nil {
		t.Fatalf("mkdir stats dir: %v", err)
	}
	line := `{"timestamp":"2026-09-18T14:15:00Z","provider":"antigravity","model":"gemini-3.8-flash-high","account":"kevin","failed":true,"status_code":429,"token_breakdown":{"total_tokens":0}}` + "\n"
	if err := os.WriteFile(filepath.Join(statsDir, "usage-2026-09-18.jsonl"), []byte(line), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	h.logDir = logsDir

	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage-stats?from=2026-09-18&to=2026-09-18", nil)
	h.GetUsageStats(ginCtx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload struct {
		From  string                       `json:"from"`
		To    string                       `json:"to"`
		Stats []usagestats.DailyModelStats `json:"stats"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(payload.Stats) != 1 {
		t.Fatalf("stats len = %d, want 1: %+v", len(payload.Stats), payload.Stats)
	}
	row := payload.Stats[0]
	if row.Model != "gemini-3.8-flash-high" || row.Account != "kevin" || row.Records != 1 || row.Failures != 1 {
		t.Fatalf("unexpected row: %+v", row)
	}
}

func TestGetUsageStats_InvalidFromReturns400(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	h.logDir = t.TempDir()

	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage-stats?from=not-a-date", nil)
	h.GetUsageStats(ginCtx)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
}

func TestGetUsageStats_NoDataReturnsEmptyStats(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	h.logDir = t.TempDir()

	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage-stats?from=2000-01-01&to=2000-01-02", nil)
	h.GetUsageStats(ginCtx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	var payload struct {
		Stats []usagestats.DailyModelStats `json:"stats"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(payload.Stats) != 0 {
		t.Fatalf("stats len = %d, want 0", len(payload.Stats))
	}
}

func TestGetUsageTimeseriesReturnsZeroFilledBuckets(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	logsDir := t.TempDir()
	statsDir := usagestats.Dir(logsDir)
	line := `{"timestamp":"2026-09-18T10:15:00Z","provider":"antigravity","model":"gemini","account":"kevin","failed":false,"status_code":200,"token_breakdown":{"total_tokens":12}}` + "\n"
	if err := os.MkdirAll(statsDir, 0o755); err != nil {
		t.Fatalf("mkdir stats dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(statsDir, "usage-2026-09-18.jsonl"), []byte(line), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	h.logDir = logsDir
	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage-timeseries?step=hour&from=2026-09-18T10:00:00Z&to=2026-09-18T12:00:00Z", nil)
	h.GetUsageTimeseries(ginCtx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Buckets []usagestats.TimeseriesBucket `json:"buckets"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(payload.Buckets) != 3 || payload.Buckets[0].Records != 1 || payload.Buckets[1].Records != 0 {
		t.Fatalf("unexpected buckets: %+v", payload.Buckets)
	}
}

func TestGetUsageRecordsReturnsNewestFirstAndFilters(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	logsDir := t.TempDir()
	statsDir := usagestats.Dir(logsDir)
	writeFixture := `{"timestamp":"2026-09-18T10:00:00Z","provider":"antigravity","model":"gemini","account":"kevin","failed":false,"status_code":200,"token_breakdown":{"total_tokens":10}}
{"timestamp":"2026-09-18T11:00:00Z","provider":"antigravity","model":"gemini","account":"kevin","failed":true,"status_code":429,"error_message":"quota","token_breakdown":{"total_tokens":0}}
`
	if err := os.MkdirAll(statsDir, 0o755); err != nil {
		t.Fatalf("mkdir stats dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(statsDir, "usage-2026-09-18.jsonl"), []byte(writeFixture), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	h.logDir = logsDir
	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage-records?date=2026-09-18&failed=true", nil)
	h.GetUsageRecords(ginCtx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 body=%s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Records []usagestats.UsageRecord `json:"records"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(payload.Records) != 1 || payload.Records[0].ErrorMessage != "quota" {
		t.Fatalf("unexpected records: %+v", payload.Records)
	}
}

func TestGetUsageRecordsRejectsPathLikeDate(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	h.logDir = t.TempDir()
	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage-records?date=../../etc/passwd", nil)
	h.GetUsageRecords(ginCtx)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestGetUsageRecordsRejectsNegativePagination(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, nil)
	h.logDir = t.TempDir()

	for _, query := range []string{"limit=-1", "offset=-1"} {
		t.Run(query, func(t *testing.T) {
			rec := httptest.NewRecorder()
			ginCtx, _ := gin.CreateTestContext(rec)
			ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage-records?"+query, nil)
			h.GetUsageRecords(ginCtx)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 body=%s", rec.Code, rec.Body.String())
			}
		})
	}
}
