package management

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	internalusage "github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
	coreusage "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/usage"
)

func newUsageRequestEventsTestHandler() *Handler {
	cfg := &config.Config{}
	h := NewHandler(cfg, "", nil)
	h.SetUsageStatistics(internalusage.NewRequestStatistics())
	h.SetRequestEventHub(internalusage.NewRequestEventHub(8))
	return h
}

func TestListUsageRequestEventsReturnsFlattenedSnapshot(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := newUsageRequestEventsTestHandler()
	h.usageStats.Record(context.Background(), coreusage.Record{
		Model:       "gpt-4.1",
		Source:      "openai-main",
		AuthIndex:   "auth-a",
		RequestID:   "req-1",
		RequestedAt: time.Now().UTC(),
		Detail: coreusage.Detail{
			InputTokens:  10,
			OutputTokens: 20,
			TotalTokens:  30,
		},
	})

	router := gin.New()
	router.GET("/v0/management/usage/request-events", h.ListUsageRequestEvents)

	req := httptest.NewRequest(http.MethodGet, "/v0/management/usage/request-events?time_range=24h&limit=10", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"model":"gpt-4.1"`) {
		t.Fatalf("response missing flattened model detail: %s", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"latest_event_id":0`) {
		t.Fatalf("response missing latest_event_id: %s", rec.Body.String())
	}
}

func TestStreamUsageRequestEventsEmitsRequestEvent(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := newUsageRequestEventsTestHandler()
	router := gin.New()
	router.GET("/v0/management/usage/request-events/stream", h.StreamUsageRequestEvents)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/v0/management/usage/request-events/stream?since_id=0", nil).WithContext(ctx)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		router.ServeHTTP(rec, req)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	h.requestEventHub.Publish(coreusage.Record{
		Model:       "claude-3.7-sonnet",
		Source:      "anthropic-main",
		AuthIndex:   "auth-b",
		RequestID:   "req-stream",
		RequestedAt: time.Now().UTC(),
	})
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	body := rec.Body.String()
	if !strings.Contains(body, "event: request-event") {
		t.Fatalf("stream body missing request-event frame: %s", body)
	}
	if !strings.Contains(body, `"model":"claude-3.7-sonnet"`) {
		t.Fatalf("stream body missing model payload: %s", body)
	}
}

func TestStreamUsageRequestEventsReturnsResetRequiredForExpiredCursor(t *testing.T) {
	gin.SetMode(gin.TestMode)

	h := newUsageRequestEventsTestHandler()
	h.SetRequestEventHub(internalusage.NewRequestEventHub(2))
	first := h.requestEventHub.Publish(coreusage.Record{Model: "n1", RequestedAt: time.Now().UTC()})
	h.requestEventHub.Publish(coreusage.Record{Model: "n2", RequestedAt: time.Now().UTC()})
	h.requestEventHub.Publish(coreusage.Record{Model: "n3", RequestedAt: time.Now().UTC()})

	router := gin.New()
	router.GET("/v0/management/usage/request-events/stream", h.StreamUsageRequestEvents)

	req := httptest.NewRequest(http.MethodGet, "/v0/management/usage/request-events/stream?since_id="+strconv.FormatUint(first.EventID, 10), nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "event: reset-required") {
		t.Fatalf("expected reset-required event, body=%s", rec.Body.String())
	}
}
