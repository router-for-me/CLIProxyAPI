package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

func TestPutRoutingStrategyAcceptsSequentialFillAlias(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("routing:\n  strategy: round-robin\n"), 0o600); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	cfg := &config.Config{
		Routing: config.RoutingConfig{Strategy: "round-robin"},
	}
	h := NewHandler(cfg, configPath, coreauth.NewManager(nil, nil, nil))

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/routing/strategy", strings.NewReader(`{"value":"sf"}`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.PutRoutingStrategy(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("PutRoutingStrategy status = %d, want %d with body %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := h.cfg.Routing.Strategy; got != "sequential-fill" {
		t.Fatalf("handler config strategy = %q, want %q", got, "sequential-fill")
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read saved config: %v", err)
	}
	if !strings.Contains(string(data), "sequential-fill") {
		t.Fatalf("saved config = %q, want it to contain %q", string(data), "sequential-fill")
	}
}

func TestPutUsageRetentionDaysPersistsAndUpdatesPlugin(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)
	usage.CloseDatabasePlugin()
	t.Cleanup(usage.CloseDatabasePlugin)

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("usage-retention-days: 30\n"), 0o600); err != nil {
		t.Fatalf("failed to write config file: %v", err)
	}

	cfg := &config.Config{
		AuthDir:                 tmpDir,
		UsagePersistenceEnabled: true,
		UsageRetentionDays:      30,
	}
	if err := usage.InitDatabasePlugin(context.Background(), "", "", tmpDir, cfg.UsageRetentionDays); err != nil {
		t.Fatalf("InitDatabasePlugin failed: %v", err)
	}
	defer usage.CloseDatabasePlugin()

	h := NewHandler(cfg, configPath, coreauth.NewManager(nil, nil, nil))
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/usage-retention-days", strings.NewReader(`{"value":45}`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.PutUsageRetentionDays(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("PutUsageRetentionDays status = %d, want %d with body %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := h.cfg.UsageRetentionDays; got != 45 {
		t.Fatalf("handler config retention = %d, want 45", got)
	}
	if got := usage.GetDatabasePlugin(); got == nil {
		t.Fatalf("expected database plugin to remain available")
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("failed to read saved config: %v", err)
	}
	if !strings.Contains(string(data), "usage-retention-days: 45") {
		t.Fatalf("saved config = %q, want it to contain usage-retention-days: 45", string(data))
	}
}

func TestCleanupMonitorLogsDeletesExpiredRecords(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	gin.SetMode(gin.TestMode)
	usage.CloseDatabasePlugin()
	t.Cleanup(usage.CloseDatabasePlugin)

	tmpDir := t.TempDir()
	if err := usage.InitDatabasePlugin(context.Background(), "", "", tmpDir, 30); err != nil {
		t.Fatalf("InitDatabasePlugin failed: %v", err)
	}
	defer usage.CloseDatabasePlugin()
	plugin := usage.GetDatabasePlugin()
	if plugin == nil {
		t.Fatalf("expected database plugin to be initialized")
	}

	oldTime := time.Now().Add(-45 * 24 * time.Hour)
	newTime := time.Now().Add(-5 * 24 * time.Hour)
	added, skipped, err := plugin.ImportRecords(usage.StatisticsSnapshot{
		APIs: map[string]usage.APISnapshot{
			"api-test": {
				Models: map[string]usage.ModelSnapshot{
					"model-old": {
						Details: []usage.RequestDetail{
							{Timestamp: oldTime, Source: "source-old", Failed: false, Tokens: usage.TokenStats{TotalTokens: 1}},
						},
					},
					"model-new": {
						Details: []usage.RequestDetail{
							{Timestamp: newTime, Source: "source-new", Failed: false, Tokens: usage.TokenStats{TotalTokens: 1}},
						},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("ImportRecords failed: %v", err)
	}
	if added != 2 || skipped != 0 {
		t.Fatalf("unexpected import result: added=%d skipped=%d", added, skipped)
	}

	h := NewHandler(&config.Config{UsagePersistenceEnabled: true, UsageRetentionDays: 30}, "", coreauth.NewManager(nil, nil, nil))
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/custom/monitor-cleanup", nil)

	h.CleanupMonitorLogs(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("CleanupMonitorLogs status = %d, want %d with body %s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var resp struct {
		Deleted int64 `json:"deleted"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response failed: %v", err)
	}
	if resp.Deleted < 1 {
		t.Fatalf("expected at least one deleted record, got %d", resp.Deleted)
	}
}
