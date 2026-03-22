package management

import (
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

func TestGetUsageStatistics_IncludesFailovers(t *testing.T) {
	gin.SetMode(gin.TestMode)

	previous := usage.StatisticsEnabled()
	usage.SetStatisticsEnabled(true)
	t.Cleanup(func() {
		usage.SetStatisticsEnabled(previous)
	})

	stats := usage.NewRequestStatistics()
	stats.Record(context.Background(), coreusage.Record{
		Provider:       "gemini",
		Model:          "gemini-2.5-pro",
		RequestedModel: "gemini-2.5-pro",
		ActualModel:    "gemini-2.0-flash",
		APIKey:         "test-key",
		RequestedAt:    time.Date(2026, time.March, 22, 8, 0, 0, 0, time.UTC),
		Detail: coreusage.Detail{
			InputTokens:  3,
			OutputTokens: 4,
			TotalTokens:  7,
		},
	})

	handler := &Handler{usageStats: stats}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage", nil)

	handler.GetUsageStatistics(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}

	var payload map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	usageMap, ok := payload["usage"].(map[string]any)
	if !ok {
		t.Fatalf("usage payload has unexpected type: %T", payload["usage"])
	}
	if got := usageMap["total_failovers"]; got != float64(1) {
		t.Fatalf("total_failovers = %v, want 1", got)
	}
}
