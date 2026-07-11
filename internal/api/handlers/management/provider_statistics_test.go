package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/redisqueue"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/usagestats"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

func TestGetProviderStatisticsReturnsAggregatedReport(t *testing.T) {
	previousEnabled := redisqueue.UsageStatisticsEnabled()
	redisqueue.SetUsageStatisticsEnabled(true)
	t.Cleanup(func() { redisqueue.SetUsageStatisticsEnabled(previousEnabled) })

	store := usagestats.NewStore(10, 24*time.Hour)
	store.HandleUsage(context.Background(), coreusage.Record{
		Provider:    "codex",
		Model:       "gpt-5",
		RequestedAt: time.Now().Add(-time.Minute),
		Detail:      coreusage.Detail{InputTokens: 10, OutputTokens: 5, TotalTokens: 15},
	})
	handler := &Handler{usageStats: store}

	recorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(recorder)
	ginContext.Request = httptest.NewRequest(http.MethodGet, "/v0/management/provider-statistics?days=1&provider=codex&model_limit=5&recent_limit=5", nil)
	handler.GetProviderStatistics(ginContext)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	var report usagestats.Report
	if errUnmarshal := json.Unmarshal(recorder.Body.Bytes(), &report); errUnmarshal != nil {
		t.Fatalf("unmarshal response: %v", errUnmarshal)
	}
	if report.Summary.Requests != 1 || report.Summary.Tokens.TotalTokens != 15 {
		t.Fatalf("summary = %#v, want one request and 15 tokens", report.Summary)
	}
	if report.Range.Provider != "codex" || report.Range.Days != 1 {
		t.Fatalf("range = %#v, want one day filtered to codex", report.Range)
	}
}

func TestGetProviderStatisticsRejectsInvalidLimits(t *testing.T) {
	tests := []string{
		"days=0",
		"days=91",
		"days=invalid",
		"model_limit=0",
		"model_limit=101",
		"recent_limit=0",
		"recent_limit=201",
	}
	for _, query := range tests {
		t.Run(query, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ginContext, _ := gin.CreateTestContext(recorder)
			ginContext.Request = httptest.NewRequest(http.MethodGet, "/v0/management/provider-statistics?"+query, nil)

			(&Handler{usageStats: usagestats.NewStore(10, time.Hour)}).GetProviderStatistics(ginContext)

			if recorder.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d body=%s", recorder.Code, http.StatusBadRequest, recorder.Body.String())
			}
		})
	}
}
