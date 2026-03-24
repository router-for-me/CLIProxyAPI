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
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

func TestGetUsageStatisticsResponseCompatibility(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := usage.NewMemoryStatisticsStore(usage.NewRequestStatistics())
	h := &Handler{usageStore: store}

	err := store.Record(context.Background(), coreusage.Record{
		APIKey:      "api-1",
		Model:       "gpt-5",
		RequestedAt: time.Date(2026, 3, 24, 13, 0, 0, 0, time.UTC),
		Detail: coreusage.Detail{
			InputTokens:  10,
			OutputTokens: 20,
			TotalTokens:  30,
		},
	})
	if err != nil {
		t.Fatalf("record failed: %v", err)
	}

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage", nil)

	h.GetUsageStatistics(c)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body=%s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Usage          usage.StatisticsSnapshot `json:"usage"`
		FailedRequests int64                    `json:"failed_requests"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}
	if resp.Usage.TotalRequests != 1 {
		t.Fatalf("total requests = %d, want 1", resp.Usage.TotalRequests)
	}
	if resp.FailedRequests != resp.Usage.FailureCount {
		t.Fatalf("failed_requests mismatch: got %d want %d", resp.FailedRequests, resp.Usage.FailureCount)
	}
	if _, ok := resp.Usage.APIs["api-1"]; !ok {
		t.Fatalf("missing api entry in usage snapshot: %+v", resp.Usage.APIs)
	}
}

func TestUsageExportImportCompatibilityAndIdempotency(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sourceStore := usage.NewMemoryStatisticsStore(usage.NewRequestStatistics())
	source := &Handler{usageStore: sourceStore}

	err := sourceStore.Record(context.Background(), coreusage.Record{
		APIKey:      "api-1",
		Model:       "gpt-5",
		Source:      "tester",
		AuthIndex:   "1",
		RequestedAt: time.Date(2026, 3, 24, 14, 30, 0, 0, time.UTC),
		Detail: coreusage.Detail{
			InputTokens:  10,
			OutputTokens: 20,
			TotalTokens:  30,
		},
	})
	if err != nil {
		t.Fatalf("record failed: %v", err)
	}

	exportRec := httptest.NewRecorder()
	exportCtx, _ := gin.CreateTestContext(exportRec)
	exportCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage/export", nil)
	source.ExportUsageStatistics(exportCtx)

	if exportRec.Code != http.StatusOK {
		t.Fatalf("export status = %d, want 200, body=%s", exportRec.Code, exportRec.Body.String())
	}

	var exportPayload usageExportPayload
	if err := json.Unmarshal(exportRec.Body.Bytes(), &exportPayload); err != nil {
		t.Fatalf("decode export failed: %v", err)
	}
	if exportPayload.Version != 1 {
		t.Fatalf("export version = %d, want 1", exportPayload.Version)
	}

	targetStore := usage.NewMemoryStatisticsStore(usage.NewRequestStatistics())
	target := &Handler{usageStore: targetStore}

	importBody, err := json.Marshal(usageImportPayload{Version: 1, Usage: exportPayload.Usage})
	if err != nil {
		t.Fatalf("marshal import payload failed: %v", err)
	}

	importOnce := func() map[string]any {
		rec := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(rec)
		ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/usage/import", bytes.NewReader(importBody))
		target.ImportUsageStatistics(ctx)
		if rec.Code != http.StatusOK {
			t.Fatalf("import status = %d, want 200, body=%s", rec.Code, rec.Body.String())
		}
		out := map[string]any{}
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode import response failed: %v", err)
		}
		return out
	}

	first := importOnce()
	if first["added"].(float64) != 1 {
		t.Fatalf("first import added = %v, want 1", first["added"])
	}
	if first["skipped"].(float64) != 0 {
		t.Fatalf("first import skipped = %v, want 0", first["skipped"])
	}

	second := importOnce()
	if second["added"].(float64) != 0 {
		t.Fatalf("second import added = %v, want 0", second["added"])
	}
	if second["skipped"].(float64) != 1 {
		t.Fatalf("second import skipped = %v, want 1", second["skipped"])
	}

	usageRec := httptest.NewRecorder()
	usageCtx, _ := gin.CreateTestContext(usageRec)
	usageCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage", nil)
	target.GetUsageStatistics(usageCtx)

	if usageRec.Code != http.StatusOK {
		t.Fatalf("usage status = %d, want 200, body=%s", usageRec.Code, usageRec.Body.String())
	}
	var usageResp struct {
		Usage usage.StatisticsSnapshot `json:"usage"`
	}
	if err := json.Unmarshal(usageRec.Body.Bytes(), &usageResp); err != nil {
		t.Fatalf("decode usage response failed: %v", err)
	}
	if usageResp.Usage.TotalRequests != 1 {
		t.Fatalf("total requests after idempotent import = %d, want 1", usageResp.Usage.TotalRequests)
	}
}
