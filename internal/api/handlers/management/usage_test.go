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

type usagePluginFunc func(context.Context, coreusage.Record)

func (f usagePluginFunc) HandleUsage(ctx context.Context, record coreusage.Record) {
	f(ctx, record)
}

func TestExportUsageStatisticsFlushesPendingRecords(t *testing.T) {
	gin.SetMode(gin.TestMode)

	stats := usage.GetRequestStatistics()
	before := stats.Snapshot().TotalRequests

	started := make(chan struct{})
	release := make(chan struct{})
	blocked := true
	coreusage.DefaultManager().Register(usagePluginFunc(func(_ context.Context, _ coreusage.Record) {
		if blocked {
			blocked = false
			close(started)
			<-release
		}
	}))

	coreusage.PublishRecord(context.Background(), coreusage.Record{
		APIKey: "export-test",
		Model:  "gpt-5.4",
		Detail: coreusage.Detail{InputTokens: 1, OutputTokens: 2, TotalTokens: 3},
	})
	<-started
	coreusage.PublishRecord(context.Background(), coreusage.Record{
		APIKey: "export-test",
		Model:  "gpt-5.4",
		Detail: coreusage.Detail{InputTokens: 4, OutputTokens: 5, TotalTokens: 9},
	})

	handler := &Handler{usageStats: stats}
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/usage/export", nil)

	done := make(chan struct{})
	go func() {
		handler.ExportUsageStatistics(ctx)
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("export returned before pending usage records were flushed")
	case <-time.After(30 * time.Millisecond):
	}

	close(release)

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("export did not complete after releasing pending usage records")
	}

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload struct {
		Usage usage.StatisticsSnapshot `json:"usage"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if got, want := payload.Usage.TotalRequests, before+2; got != want {
		t.Fatalf("total_requests = %d, want %d", got, want)
	}
}
