package test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func newAPIKeyQuotaTestHandler(t *testing.T) (*management.Handler, string) {
	t.Helper()
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")

	cfg := &config.Config{
		SDKConfig: config.SDKConfig{
			APIKeyQuotas: config.APIKeyQuotaConfig{
				Enabled:              false,
				ExcludeModelPatterns: []string{"*haiku*"},
				MonthlyTokenLimits: []config.APIKeyMonthlyModelTokenLimit{
					{APIKey: "team-a", Model: "claude-*", Limit: 1000},
				},
			},
		},
	}
	if err := os.WriteFile(configPath, []byte("port: 8080\n"), 0o644); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}
	return management.NewHandler(cfg, configPath, nil), configPath
}

func setupAPIKeyQuotaRouter(h *management.Handler) *gin.Engine {
	r := gin.New()
	mgmt := r.Group("/v0/management")
	{
		mgmt.GET("/api-key-quotas", h.GetAPIKeyQuotas)
		mgmt.PUT("/api-key-quotas", h.PutAPIKeyQuotas)
		mgmt.PATCH("/api-key-quotas", h.PatchAPIKeyQuotas)
		mgmt.GET("/api-key-quotas/enabled", h.GetAPIKeyQuotasEnabled)
		mgmt.PUT("/api-key-quotas/enabled", h.PutAPIKeyQuotasEnabled)
		mgmt.GET("/api-key-quotas/exclude-model-patterns", h.GetAPIKeyQuotaExcludeModelPatterns)
		mgmt.PUT("/api-key-quotas/exclude-model-patterns", h.PutAPIKeyQuotaExcludeModelPatterns)
		mgmt.PATCH("/api-key-quotas/exclude-model-patterns", h.PatchAPIKeyQuotaExcludeModelPatterns)
		mgmt.DELETE("/api-key-quotas/exclude-model-patterns", h.DeleteAPIKeyQuotaExcludeModelPatterns)
		mgmt.GET("/api-key-quotas/monthly-token-limits", h.GetAPIKeyQuotaMonthlyTokenLimits)
		mgmt.PUT("/api-key-quotas/monthly-token-limits", h.PutAPIKeyQuotaMonthlyTokenLimits)
		mgmt.PATCH("/api-key-quotas/monthly-token-limits", h.PatchAPIKeyQuotaMonthlyTokenLimits)
		mgmt.DELETE("/api-key-quotas/monthly-token-limits", h.DeleteAPIKeyQuotaMonthlyTokenLimits)
	}
	return r
}

func TestGetAPIKeyQuotas(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, _ := newAPIKeyQuotaTestHandler(t)
	r := setupAPIKeyQuotaRouter(h)

	req := httptest.NewRequest(http.MethodGet, "/v0/management/api-key-quotas", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, w.Code)
	}
	var resp map[string]config.APIKeyQuotaConfig
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if _, ok := resp["api-key-quotas"]; !ok {
		t.Fatalf("missing api-key-quotas in response")
	}
}

func TestPutAndPatchAPIKeyQuotaLimits(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, configPath := newAPIKeyQuotaTestHandler(t)
	r := setupAPIKeyQuotaRouter(h)

	putBody := `{"items":[{"api-key":"team-a","model":"claude-*","limit":2000}]}`
	req := httptest.NewRequest(http.MethodPut, "/v0/management/api-key-quotas/monthly-token-limits", bytes.NewBufferString(putBody))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, w.Code, w.Body.String())
	}

	patchBody := `{"match":{"api-key":"team-a","model":"claude-*"},"value":{"api-key":"team-a","model":"claude-*","limit":3000}}`
	req = httptest.NewRequest(http.MethodPatch, "/v0/management/api-key-quotas/monthly-token-limits", bytes.NewBufferString(patchBody))
	req.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, w.Code, w.Body.String())
	}

	loaded, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}
	if len(loaded.APIKeyQuotas.MonthlyTokenLimits) != 1 {
		t.Fatalf("expected 1 quota limit, got %d", len(loaded.APIKeyQuotas.MonthlyTokenLimits))
	}
	if loaded.APIKeyQuotas.MonthlyTokenLimits[0].Limit != 3000 {
		t.Fatalf("expected patched limit 3000, got %d", loaded.APIKeyQuotas.MonthlyTokenLimits[0].Limit)
	}
}

func TestPutAPIKeyQuotasEnabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, configPath := newAPIKeyQuotaTestHandler(t)
	r := setupAPIKeyQuotaRouter(h)

	body := `{"value": true}`
	req := httptest.NewRequest(http.MethodPut, "/v0/management/api-key-quotas/enabled", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d: %s", http.StatusOK, w.Code, w.Body.String())
	}

	loaded, err := config.LoadConfig(configPath)
	if err != nil {
		t.Fatalf("failed to load config: %v", err)
	}
	if !loaded.APIKeyQuotas.Enabled {
		t.Fatalf("expected api-key-quotas.enabled true")
	}
}
