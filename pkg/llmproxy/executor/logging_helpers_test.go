package executor

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/kooshapari/cliproxyapi-plusplus/v6/internal/config"
)

func TestRecordAPIResponseMetadataRecordsTimestamp(t *testing.T) {
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	cfg := &config.Config{}
	cfg.RequestLog = true
	ctx := context.WithValue(context.Background(), ginContextKey, ginCtx)

	recordAPIRequest(ctx, cfg, upstreamRequestLog{URL: "http://example.local"})
	recordAPIResponseMetadata(ctx, cfg, http.StatusOK, http.Header{"Content-Type": {"application/json"}})

	tsRaw, exists := ginCtx.Get(apiResponseTimestampKey)
	if !exists {
		t.Fatal("API_RESPONSE_TIMESTAMP was not set")
	}
	ts, ok := tsRaw.(time.Time)
	if !ok || ts.IsZero() {
		t.Fatalf("API_RESPONSE_TIMESTAMP invalid type or zero: %#v", tsRaw)
	}
}

func TestRecordAPIResponseErrorKeepsInitialTimestamp(t *testing.T) {
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	cfg := &config.Config{}
	cfg.RequestLog = true
	ctx := context.WithValue(context.Background(), ginContextKey, ginCtx)

	recordAPIRequest(ctx, cfg, upstreamRequestLog{URL: "http://example.local"})
	recordAPIResponseMetadata(ctx, cfg, http.StatusOK, http.Header{"Content-Type": {"application/json"}})

	tsRaw, exists := ginCtx.Get(apiResponseTimestampKey)
	if !exists {
		t.Fatal("API_RESPONSE_TIMESTAMP was not set")
	}
	initial, ok := tsRaw.(time.Time)
	if !ok {
		t.Fatalf("API_RESPONSE_TIMESTAMP invalid type: %#v", tsRaw)
	}

	time.Sleep(5 * time.Millisecond)
	recordAPIResponseError(ctx, cfg, errors.New("upstream error"))

	afterRaw, exists := ginCtx.Get(apiResponseTimestampKey)
	if !exists {
		t.Fatal("API_RESPONSE_TIMESTAMP disappeared after error")
	}
	after, ok := afterRaw.(time.Time)
	if !ok || !after.Equal(initial) {
		t.Fatalf("API_RESPONSE_TIMESTAMP changed after error: initial=%v after=%v", initial, afterRaw)
	}
}

func TestAppendAPIResponseChunkSetsTimestamp(t *testing.T) {
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	cfg := &config.Config{}
	cfg.RequestLog = true
	ctx := context.WithValue(context.Background(), ginContextKey, ginCtx)

	recordAPIRequest(ctx, cfg, upstreamRequestLog{URL: "http://example.local"})
	appendAPIResponseChunk(ctx, cfg, []byte("chunk-1"))

	tsRaw, exists := ginCtx.Get(apiResponseTimestampKey)
	if !exists {
		t.Fatal("API_RESPONSE_TIMESTAMP was not set after chunk append")
	}
	ts, ok := tsRaw.(time.Time)
	if !ok || ts.IsZero() {
		t.Fatalf("API_RESPONSE_TIMESTAMP invalid after chunk append: %#v", tsRaw)
	}
}

func TestRecordAPIResponseTimestampStableAcrossChunkAndError(t *testing.T) {
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	cfg := &config.Config{}
	cfg.RequestLog = true
	ctx := context.WithValue(context.Background(), ginContextKey, ginCtx)

	recordAPIRequest(ctx, cfg, upstreamRequestLog{URL: "http://example.local"})
	appendAPIResponseChunk(ctx, cfg, []byte("chunk-1"))

	tsRaw, exists := ginCtx.Get(apiResponseTimestampKey)
	if !exists {
		t.Fatal("API_RESPONSE_TIMESTAMP was not set after chunk append")
	}
	initial, ok := tsRaw.(time.Time)
	if !ok || initial.IsZero() {
		t.Fatalf("API_RESPONSE_TIMESTAMP invalid: %#v", tsRaw)
	}

	time.Sleep(5 * time.Millisecond)
	recordAPIResponseError(ctx, cfg, errors.New("upstream error"))

	afterRaw, exists := ginCtx.Get(apiResponseTimestampKey)
	if !exists {
		t.Fatal("API_RESPONSE_TIMESTAMP disappeared after error")
	}
	after, ok := afterRaw.(time.Time)
	if !ok || !after.Equal(initial) {
		t.Fatalf("API_RESPONSE_TIMESTAMP changed after chunk->error: initial=%v after=%v", initial, afterRaw)
	}
}

func TestRecordAPIResponseMetadataDoesNotSetWhenRequestLoggingDisabled(t *testing.T) {
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	cfg := &config.Config{}
	cfg.RequestLog = false
	ctx := context.WithValue(context.Background(), ginContextKey, ginCtx)

	recordAPIRequest(ctx, cfg, upstreamRequestLog{URL: "http://example.local"})
	recordAPIResponseMetadata(ctx, cfg, http.StatusOK, http.Header{})

	if _, exists := ginCtx.Get(apiResponseTimestampKey); exists {
		t.Fatal("API_RESPONSE_TIMESTAMP should not be set when RequestLog is disabled")
	}
}

func TestExtractJSONErrorMessage_ModelNotFoundAddsGuidance(t *testing.T) {
	body := []byte(`{"error":{"code":"model_not_found","message":"model not found: foo"}}`)
	got := extractJSONErrorMessage(body)
	if !strings.Contains(got, "GET /v1/models") {
		t.Fatalf("expected /v1/models guidance, got %q", got)
	}
}

func TestExtractJSONErrorMessage_CodexModelAddsResponsesHint(t *testing.T) {
	body := []byte(`{"error":{"message":"model not found for gpt-5.3-codex"}}`)
	got := extractJSONErrorMessage(body)
	if !strings.Contains(got, "/v1/responses") {
		t.Fatalf("expected /v1/responses hint, got %q", got)
	}
}

func TestExtractJSONErrorMessage_NonModelErrorUnchanged(t *testing.T) {
	body := []byte(`{"error":{"message":"rate limit exceeded"}}`)
	got := extractJSONErrorMessage(body)
	if got != "rate limit exceeded" {
		t.Fatalf("expected unchanged message, got %q", got)
	}
}

func TestExtractJSONErrorMessage_ExistingGuidanceNotDuplicated(t *testing.T) {
	body := []byte(`{"error":{"message":"model not found; check /v1/models"}}`)
	got := extractJSONErrorMessage(body)
	if got != "model not found; check /v1/models" {
		t.Fatalf("expected existing guidance to remain unchanged, got %q", got)
	}
}
