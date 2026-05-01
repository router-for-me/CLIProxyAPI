package management

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

func newUsageResetTestHandler(t *testing.T) *Handler {
	t.Helper()
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("port: 8080\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	h := NewHandler(&config.Config{}, configPath, nil)
	h.SetUsageStatistics(usage.NewRequestStatistics())
	return h
}

func seedUsageStats(t *testing.T, stats *usage.RequestStatistics) {
	t.Helper()
	stats.Record(nil, coreusage.Record{Provider: "provider-a", Model: "model-a", APIKey: "key-a", Detail: coreusage.Detail{TotalTokens: 10}})
	stats.Record(nil, coreusage.Record{Provider: "provider-b", Model: "model-b", APIKey: "key-b", Detail: coreusage.Detail{TotalTokens: 20}})
}

func TestResetUsageStatistics_All(t *testing.T) {
	t.Parallel()
	h := newUsageResetTestHandler(t)
	seedUsageStats(t, h.usageStats)

	body := []byte(`{"scope":"all"}`)
	req := httptest.NewRequest(http.MethodPost, "/v0/management/usage/reset", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	h.ResetUsageStatistics(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if got := h.usageStats.Snapshot().TotalRequests; got != 0 {
		t.Fatalf("expected reset all to clear usage, got %d requests", got)
	}
}

func TestResetUsageStatistics_Provider(t *testing.T) {
	t.Parallel()
	h := newUsageResetTestHandler(t)
	seedUsageStats(t, h.usageStats)

	body := []byte(`{"scope":"provider","value":"provider-a"}`)
	req := httptest.NewRequest(http.MethodPost, "/v0/management/usage/reset", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	h.ResetUsageStatistics(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	snapshot := h.usageStats.Snapshot()
	if _, ok := snapshot.Providers["provider-a"]; ok {
		t.Fatalf("expected provider-a to be removed")
	}
	if snapshot.TotalRequests != 1 {
		t.Fatalf("expected one remaining request, got %d", snapshot.TotalRequests)
	}
}

func TestResetUsageStatistics_Model(t *testing.T) {
	t.Parallel()
	h := newUsageResetTestHandler(t)
	seedUsageStats(t, h.usageStats)

	body := []byte(`{"scope":"model","value":"model-a"}`)
	req := httptest.NewRequest(http.MethodPost, "/v0/management/usage/reset", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	h.ResetUsageStatistics(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	snapshot := h.usageStats.Snapshot()
	if _, ok := snapshot.Models["model-a"]; ok {
		t.Fatalf("expected model-a to be removed")
	}
	if snapshot.TotalRequests != 1 {
		t.Fatalf("expected one remaining request, got %d", snapshot.TotalRequests)
	}
}

func TestResetUsageStatistics_APIKey(t *testing.T) {
	t.Parallel()
	h := newUsageResetTestHandler(t)
	seedUsageStats(t, h.usageStats)

	body := []byte(`{"scope":"api_key","value":"key-a"}`)
	req := httptest.NewRequest(http.MethodPost, "/v0/management/usage/reset", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	h.ResetUsageStatistics(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	snapshot := h.usageStats.Snapshot()
	if _, ok := snapshot.APIs["key-a"]; ok {
		t.Fatalf("expected key-a api bucket to be removed")
	}
	if snapshot.TotalRequests != 1 {
		t.Fatalf("expected one remaining request, got %d", snapshot.TotalRequests)
	}
}

func TestResetUsageStatistics_PersistentSavesSnapshot(t *testing.T) {
	t.Parallel()
	h := newUsageResetTestHandler(t)
	store := usage.NewSnapshotStore(filepath.Join(t.TempDir(), "usage-statistics.json"))
	h.SetUsageSnapshotStore(store)
	h.SetUsageStatisticsMode("persistent")
	seedUsageStats(t, h.usageStats)

	body := []byte(`{"scope":"provider","value":"provider-a"}`)
	req := httptest.NewRequest(http.MethodPost, "/v0/management/usage/reset", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	h.ResetUsageStatistics(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("load saved snapshot: %v", err)
	}
	restored := usage.NewRequestStatistics()
	restored.MergeSnapshot(loaded)
	snapshot := restored.Snapshot()
	if _, ok := snapshot.Providers["provider-a"]; ok {
		t.Fatalf("expected provider-a to remain cleared after reload")
	}
	if snapshot.TotalRequests != 1 {
		t.Fatalf("expected one remaining request after reload, got %d", snapshot.TotalRequests)
	}
}

func TestResetUsageStatistics_InvalidScope(t *testing.T) {
	t.Parallel()
	h := newUsageResetTestHandler(t)

	body := []byte(`{"scope":"weird"}`)
	req := httptest.NewRequest(http.MethodPost, "/v0/management/usage/reset", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	h.ResetUsageStatistics(c)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}

	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp["error"] == "" {
		t.Fatalf("expected error response, got %s", w.Body.String())
	}
}
