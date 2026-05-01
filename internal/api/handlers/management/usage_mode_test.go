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
)

func newUsageModeTestHandler(t *testing.T) *Handler {
	t.Helper()
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("port: 8080\n"), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	h := NewHandler(&config.Config{}, configPath, nil)
	return h
}

func TestGetUsageStatisticsMode(t *testing.T) {
	t.Parallel()
	h := newUsageModeTestHandler(t)
	h.cfg.UsageStatistics.Mode = "persistent"

	req := httptest.NewRequest(http.MethodGet, "/v0/management/usage-statistics-mode", nil)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	h.GetUsageStatisticsMode(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}

	var resp map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp["usage-statistics-mode"] != "persistent" {
		t.Fatalf("expected persistent, got %q", resp["usage-statistics-mode"])
	}
}

func TestPutUsageStatisticsModeRejectsInvalidValue(t *testing.T) {
	t.Parallel()
	h := newUsageModeTestHandler(t)

	body := []byte(`{"value":"strange"}`)
	req := httptest.NewRequest(http.MethodPut, "/v0/management/usage-statistics-mode", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	h.PutUsageStatisticsMode(c)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestGetUsageStatisticsEnabledUsesEffectiveMode(t *testing.T) {
	t.Parallel()
	h := newUsageModeTestHandler(t)
	h.cfg.UsageStatisticsEnabled = false
	h.cfg.UsageStatistics.Mode = "persistent"

	req := httptest.NewRequest(http.MethodGet, "/v0/management/usage-statistics-enabled", nil)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	h.GetUsageStatisticsEnabled(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	var resp map[string]bool
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if !resp["usage-statistics-enabled"] {
		t.Fatalf("expected effective mode persistent to report enabled")
	}
}

func TestPutUsageStatisticsEnabledOverridesExplicitMode(t *testing.T) {
	t.Parallel()
	h := newUsageModeTestHandler(t)
	h.cfg.UsageStatistics.Mode = "off"

	body := []byte(`{"value":true}`)
	req := httptest.NewRequest(http.MethodPut, "/v0/management/usage-statistics-enabled", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	h.PutUsageStatisticsEnabled(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if got := h.cfg.EffectiveUsageStatisticsMode(); got != "memory" {
		t.Fatalf("expected legacy enabled update to switch effective mode to memory, got %q", got)
	}
}

func TestPatchUsageStatisticsModeUpdatesConfig(t *testing.T) {
	t.Parallel()
	h := newUsageModeTestHandler(t)

	body := []byte(`{"value":"memory"}`)
	req := httptest.NewRequest(http.MethodPatch, "/v0/management/usage-statistics-mode", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	h.PutUsageStatisticsMode(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if got := h.cfg.UsageStatistics.Mode; got != "memory" {
		t.Fatalf("expected mode to update to memory, got %q", got)
	}
}

func TestImportUsageStatistics_PersistentSavesSnapshot(t *testing.T) {
	t.Parallel()
	h := newUsageModeTestHandler(t)
	store := usage.NewSnapshotStore(filepath.Join(t.TempDir(), "usage-statistics.json"))
	h.SetUsageStatistics(usage.NewRequestStatistics())
	h.SetUsageSnapshotStore(store)
	h.SetUsageStatisticsMode("persistent")

	body := []byte(`{"version":1,"usage":{"apis":{"key-a":{"total_requests":1,"total_tokens":30,"models":{"model-a":{"total_requests":1,"total_tokens":30,"details":[{"timestamp":"2026-04-28T00:00:00Z","provider":"provider-a","model":"model-a","api_key":"key-a","source":"source-a","tokens":{"total_tokens":30},"failed":false}]}}}}}}`)
	req := httptest.NewRequest(http.MethodPost, "/v0/management/usage/import", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = req

	h.ImportUsageStatistics(c)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatalf("load saved snapshot: %v", err)
	}
	if loaded.TotalRequests != 1 {
		t.Fatalf("expected saved snapshot to contain 1 request, got %d", loaded.TotalRequests)
	}
}
