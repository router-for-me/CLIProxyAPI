package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usage"
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

func TestGetUsageStatisticsReturnsSnapshot(t *testing.T) {
	gin.SetMode(gin.TestMode)

	stats := usage.NewRequestStatistics()
	stats.RestoreSnapshot(usage.StatisticsSnapshot{
		TotalRequests: 3,
		SuccessCount:  2,
		FailureCount:  1,
		TotalTokens:   42,
	})

	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage", nil)

	h := &Handler{usageStats: stats}
	h.GetUsageStatistics(ginCtx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload struct {
		Usage          usage.StatisticsSnapshot `json:"usage"`
		FailedRequests int64                    `json:"failed_requests"`
	}
	if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &payload); errUnmarshal != nil {
		t.Fatalf("unmarshal response: %v", errUnmarshal)
	}
	if payload.Usage.TotalRequests != 3 || payload.Usage.SuccessCount != 2 || payload.Usage.FailureCount != 1 {
		t.Fatalf("usage snapshot = %+v, want totals 3/2/1", payload.Usage)
	}
	if payload.FailedRequests != 1 {
		t.Fatalf("failed_requests = %d, want 1", payload.FailedRequests)
	}
}

func TestExportUsageStatisticsReturnsVersionedSnapshot(t *testing.T) {
	gin.SetMode(gin.TestMode)

	stats := usage.NewRequestStatistics()
	stats.RestoreSnapshot(usage.StatisticsSnapshot{
		TotalRequests: 5,
		SuccessCount:  4,
		FailureCount:  1,
		TotalTokens:   99,
	})

	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage/export", nil)

	h := &Handler{usageStats: stats}
	h.ExportUsageStatistics(ginCtx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload usageExportPayload
	if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &payload); errUnmarshal != nil {
		t.Fatalf("unmarshal response: %v", errUnmarshal)
	}
	if payload.Version != 1 {
		t.Fatalf("version = %d, want 1", payload.Version)
	}
	if payload.Usage.TotalRequests != 5 || payload.Usage.FailureCount != 1 {
		t.Fatalf("usage snapshot = %+v, want totals 5/1", payload.Usage)
	}
}

func TestImportUsageStatisticsRestoresSnapshot(t *testing.T) {
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(
		http.MethodPost,
		"/v0/management/usage/import",
		strings.NewReader(`{"version":1,"usage":{"total_requests":4,"success_count":3,"failure_count":1,"total_tokens":77}}`),
	)

	stats := usage.NewRequestStatistics()
	h := &Handler{usageStats: stats}
	h.ImportUsageStatistics(ginCtx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload struct {
		Added         int64 `json:"added"`
		Skipped       int64 `json:"skipped"`
		TotalRequests int64 `json:"total_requests"`
		FailedRequest int64 `json:"failed_requests"`
	}
	if errUnmarshal := json.Unmarshal(rec.Body.Bytes(), &payload); errUnmarshal != nil {
		t.Fatalf("unmarshal response: %v", errUnmarshal)
	}
	if payload.Added != 4 || payload.Skipped != 0 || payload.TotalRequests != 4 || payload.FailedRequest != 1 {
		t.Fatalf("import response = %+v, want added=4 skipped=0 total=4 failed=1", payload)
	}

	snapshot := stats.Snapshot()
	if snapshot.TotalRequests != 4 || snapshot.SuccessCount != 3 || snapshot.FailureCount != 1 || snapshot.TotalTokens != 77 {
		t.Fatalf("restored snapshot = %+v, want 4/3/1/77", snapshot)
	}
}

func TestImportUsageStatisticsRejectsOversizedBody(t *testing.T) {
	gin.SetMode(gin.TestMode)

	prevMaxBytes := usageImportMaxBytes
	usageImportMaxBytes = 8
	t.Cleanup(func() {
		usageImportMaxBytes = prevMaxBytes
	})

	rec := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(rec)
	ginCtx.Request = httptest.NewRequest(
		http.MethodPost,
		"/v0/management/usage/import",
		strings.NewReader(`{"version":1,"usage":{"total_requests":1}}`),
	)

	h := &Handler{usageStats: usage.NewRequestStatistics()}
	h.ImportUsageStatistics(ginCtx)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusRequestEntityTooLarge, rec.Body.String())
	}
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
