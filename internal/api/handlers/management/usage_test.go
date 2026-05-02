package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
)

func newUsageTestStore(t *testing.T) usage.Store {
	t.Helper()

	store, err := usage.NewSQLiteStore(filepath.Join(t.TempDir(), "usage.db"))
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func newUsageTestRouter(h *Handler) *gin.Engine {
	router := gin.New()
	mgmt := router.Group("/v0/management")
	mgmt.GET("/usage", h.GetUsageStatistics)
	mgmt.DELETE("/usage", h.DeleteUsageRecords)
	return router
}

func assertUsageRecordAbsent(t *testing.T, store usage.Store, id string) {
	t.Helper()

	remaining, err := store.Query(context.Background(), usage.QueryRange{})
	if err != nil {
		t.Fatalf("query remaining usage records: %v", err)
	}
	for _, byModel := range remaining {
		for _, details := range byModel {
			for _, detail := range details {
				if detail.ID == id {
					t.Fatalf("record %q still present after delete", id)
				}
			}
		}
	}
}

func TestGetUsageStatisticsReturnsSimplifiedShape(t *testing.T) {
	gin.SetMode(gin.TestMode)

	store := newUsageTestStore(t)
	timestamp := time.Date(2026, 5, 2, 10, 30, 0, 0, time.UTC)
	if err := store.Insert(context.Background(), usage.Record{
		ID:                 "record-1",
		Timestamp:          timestamp,
		APIKey:             "api-key-1",
		Model:              "claude-sonnet-4-5",
		Source:             "claude-code",
		AuthIndex:          "auth-1",
		LatencyMs:          1250,
		FirstByteLatencyMs: 200,
		GenerationMs:       1050,
		ThinkingEffort:     "high",
		Tokens: usage.TokenStats{
			InputTokens:     10,
			OutputTokens:    20,
			ReasoningTokens: 3,
			CachedTokens:    4,
			TotalTokens:     33,
		},
	}); err != nil {
		t.Fatalf("insert usage record: %v", err)
	}

	h := &Handler{}
	h.SetUsageStore(store)
	router := newUsageTestRouter(h)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v0/management/usage", nil)
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	body := rec.Body.String()
	for _, oldWrapper := range []string{"\"usage\"", "\"models\"", "\"details\""} {
		if strings.Contains(body, oldWrapper) {
			t.Fatalf("response contains old wrapper %s: %s", oldWrapper, body)
		}
	}

	var payload usage.APIUsage
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v body=%s", err, body)
	}
	details := payload["api-key-1"]["claude-sonnet-4-5"]
	if len(details) != 1 {
		t.Fatalf("details len = %d, want 1 payload=%v", len(details), payload)
	}
	detail := details[0]
	if detail.ID != "record-1" || !detail.Timestamp.Equal(timestamp) || detail.LatencyMs != 1250 || detail.FirstByteLatencyMs != 200 || detail.GenerationMs != 1050 || detail.ThinkingEffort != "high" {
		t.Fatalf("detail = %+v", detail)
	}
	if detail.Tokens.InputTokens != 10 || detail.Tokens.OutputTokens != 20 || detail.Tokens.ReasoningTokens != 3 || detail.Tokens.CachedTokens != 4 || detail.Tokens.TotalTokens != 33 {
		t.Fatalf("tokens = %+v", detail.Tokens)
	}
}

func TestGetUsageStatisticsRejectsInvalidRange(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name        string
		path        string
		wantError   string
		attachStore bool
	}{
		{
			name:        "invalid start",
			path:        "/v0/management/usage?start=bad",
			wantError:   "invalid start",
			attachStore: true,
		},
		{
			name:        "invalid end",
			path:        "/v0/management/usage?end=bad",
			wantError:   "invalid end",
			attachStore: true,
		},
		{
			name:      "invalid start before nil store fallback",
			path:      "/v0/management/usage?start=bad",
			wantError: "invalid start",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &Handler{}
			if tt.attachStore {
				h.SetUsageStore(newUsageTestStore(t))
			}
			router := newUsageTestRouter(h)

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "\"error\":\""+tt.wantError+"\"") {
				t.Fatalf("body = %s, want %s error", rec.Body.String(), tt.wantError)
			}
		})
	}
}

func TestGetUsageStatisticsFiltersByValidRange(t *testing.T) {
	gin.SetMode(gin.TestMode)

	store := newUsageTestStore(t)
	start := time.Date(2026, 5, 2, 10, 0, 0, 0, time.UTC)
	end := time.Date(2026, 5, 2, 11, 0, 0, 0, time.UTC)
	records := []usage.Record{
		{ID: "before-range", Timestamp: start.Add(-time.Minute), APIKey: "api-key-1", Model: "claude-sonnet-4-5"},
		{ID: "in-range", Timestamp: start.Add(30 * time.Minute), APIKey: "api-key-1", Model: "claude-sonnet-4-5"},
		{ID: "after-range", Timestamp: end.Add(time.Minute), APIKey: "api-key-1", Model: "claude-sonnet-4-5"},
	}
	for _, record := range records {
		if err := store.Insert(context.Background(), record); err != nil {
			t.Fatalf("insert usage record %s: %v", record.ID, err)
		}
	}

	h := &Handler{}
	h.SetUsageStore(store)
	router := newUsageTestRouter(h)

	rec := httptest.NewRecorder()
	path := "/v0/management/usage?start=" + start.Format(time.RFC3339) + "&end=" + end.Format(time.RFC3339)
	req := httptest.NewRequest(http.MethodGet, path, nil)
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload usage.APIUsage
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode payload: %v body=%s", err, rec.Body.String())
	}
	details := payload["api-key-1"]["claude-sonnet-4-5"]
	if len(details) != 1 {
		t.Fatalf("details len = %d, want 1 payload=%v", len(details), payload)
	}
	if details[0].ID != "in-range" {
		t.Fatalf("detail ID = %q, want in-range payload=%v", details[0].ID, payload)
	}
}

func TestGetUsageStatisticsSetUsageStoreAllowsNilReceiver(t *testing.T) {
	var h *Handler
	h.SetUsageStore(newUsageTestStore(t))
}

func TestDeleteUsageRecords(t *testing.T) {
	gin.SetMode(gin.TestMode)

	store := newUsageTestStore(t)
	if err := store.Insert(context.Background(), usage.Record{
		ID:        "record-1",
		Timestamp: time.Date(2026, 5, 2, 11, 0, 0, 0, time.UTC),
		APIKey:    "api-key-1",
		Model:     "claude-sonnet-4-5",
	}); err != nil {
		t.Fatalf("insert usage record: %v", err)
	}

	h := &Handler{}
	h.SetUsageStore(store)
	router := newUsageTestRouter(h)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/v0/management/usage", strings.NewReader(`{"ids":["record-1","missing"]}`))
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var result usage.DeleteResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode delete result: %v body=%s", err, rec.Body.String())
	}
	if result.Deleted != 1 {
		t.Fatalf("deleted = %d, want 1", result.Deleted)
	}
	if len(result.Missing) != 1 || result.Missing[0] != "missing" {
		t.Fatalf("missing = %v, want [missing]", result.Missing)
	}
	assertUsageRecordAbsent(t, store, "record-1")
}

func TestDeleteUsageRecordsNormalizesAndDeduplicatesIDs(t *testing.T) {
	gin.SetMode(gin.TestMode)

	store := newUsageTestStore(t)
	if err := store.Insert(context.Background(), usage.Record{
		ID:        "record-1",
		Timestamp: time.Date(2026, 5, 2, 11, 0, 0, 0, time.UTC),
		APIKey:    "api-key-1",
		Model:     "claude-sonnet-4-5",
	}); err != nil {
		t.Fatalf("insert usage record: %v", err)
	}

	h := &Handler{}
	h.SetUsageStore(store)
	router := newUsageTestRouter(h)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodDelete, "/v0/management/usage", strings.NewReader(`{"ids":[" record-1 ","record-1","record-1"]}`))
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var result usage.DeleteResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode delete result: %v body=%s", err, rec.Body.String())
	}
	if result.Deleted != 1 {
		t.Fatalf("deleted = %d, want 1", result.Deleted)
	}
	if len(result.Missing) != 0 {
		t.Fatalf("missing = %v, want empty", result.Missing)
	}
	assertUsageRecordAbsent(t, store, "record-1")
}

func TestDeleteUsageRecordsRejectsEmptyIDs(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name string
		body string
	}{
		{name: "empty ids", body: `{"ids":[]}`},
		{name: "blank ids", body: `{"ids":["","  "]}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &Handler{}
			h.SetUsageStore(newUsageTestStore(t))
			router := newUsageTestRouter(h)

			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodDelete, "/v0/management/usage", strings.NewReader(tt.body))
			router.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), "\"error\":\"ids required\"") {
				t.Fatalf("body = %s, want ids required error", rec.Body.String())
			}
		})
	}
}
