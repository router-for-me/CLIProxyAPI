package executor

import (
	"context"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
)

func TestAppendAPIResponseChunk_TruncatesWhenRequestLogDisabled(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(nil)
	ctx := context.WithValue(context.Background(), "gin", ginCtx)

	cfg := &config.Config{SDKConfig: config.SDKConfig{RequestLog: false}}

	recordAPIRequest(ctx, cfg, upstreamRequestLog{
		URL:      "https://example.com/api",
		Method:   "POST",
		Provider: "test-provider",
		AuthID:   "test-auth",
	})

	largeChunk := strings.Repeat("x", maxErrorLogResponseBodySize+1000)
	appendAPIResponseChunk(ctx, cfg, []byte(largeChunk))

	attempts := getAttempts(ginCtx)
	if len(attempts) == 0 {
		t.Fatal("expected at least one attempt")
	}

	attempt := attempts[0]
	if !attempt.bodyTruncated {
		t.Error("expected bodyTruncated to be true")
	}

	responseText := attempt.response.String()
	if !strings.Contains(responseText, "<truncated:") {
		t.Error("expected truncation marker in response")
	}

	if attempt.bodyBytesWritten > maxErrorLogResponseBodySize {
		t.Errorf("expected bodyBytesWritten <= %d, got %d",
			maxErrorLogResponseBodySize, attempt.bodyBytesWritten)
	}
}

func TestAppendAPIResponseChunk_NoTruncationWhenRequestLogEnabled(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(nil)
	ctx := context.WithValue(context.Background(), "gin", ginCtx)

	cfg := &config.Config{SDKConfig: config.SDKConfig{RequestLog: true}}

	recordAPIRequest(ctx, cfg, upstreamRequestLog{
		URL:    "https://example.com/api",
		Method: "POST",
	})

	largeChunk := strings.Repeat("y", maxErrorLogResponseBodySize+5000)
	appendAPIResponseChunk(ctx, cfg, []byte(largeChunk))

	attempts := getAttempts(ginCtx)
	if len(attempts) == 0 {
		t.Fatal("expected at least one attempt")
	}

	attempt := attempts[0]
	if attempt.bodyTruncated {
		t.Error("expected bodyTruncated to be false when request-log is enabled")
	}

	responseText := attempt.response.String()
	if strings.Contains(responseText, "<truncated:") {
		t.Error("expected no truncation marker when request-log is enabled")
	}

	if attempt.bodyBytesWritten != len(largeChunk) {
		t.Errorf("expected full body written, got %d, want %d",
			attempt.bodyBytesWritten, len(largeChunk))
	}
}

func TestAppendAPIResponseChunk_MultipleChunksRespectLimit(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(nil)
	ctx := context.WithValue(context.Background(), "gin", ginCtx)

	cfg := &config.Config{SDKConfig: config.SDKConfig{RequestLog: false}}

	recordAPIRequest(ctx, cfg, upstreamRequestLog{
		URL:    "https://example.com/api",
		Method: "POST",
	})

	chunkSize := 8 * 1024
	numChunks := 10
	for i := 0; i < numChunks; i++ {
		chunk := strings.Repeat("z", chunkSize)
		appendAPIResponseChunk(ctx, cfg, []byte(chunk))
	}

	attempts := getAttempts(ginCtx)
	if len(attempts) == 0 {
		t.Fatal("expected at least one attempt")
	}

	attempt := attempts[0]
	if !attempt.bodyTruncated {
		t.Error("expected bodyTruncated after exceeding limit with multiple chunks")
	}

	if attempt.bodyBytesWritten > maxErrorLogResponseBodySize {
		t.Errorf("bodyBytesWritten %d exceeded limit %d",
			attempt.bodyBytesWritten, maxErrorLogResponseBodySize)
	}
}

func TestRecordAPIRequest_OmitsBodyWhenRequestLogDisabled(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(nil)
	ctx := context.WithValue(context.Background(), "gin", ginCtx)

	cfg := &config.Config{SDKConfig: config.SDKConfig{RequestLog: false}}

	recordAPIRequest(ctx, cfg, upstreamRequestLog{
		URL:      "https://example.com/api",
		Method:   "POST",
		Body:     []byte(`{"secret": "password123"}`),
		Provider: "test-provider",
		AuthID:   "auth-123",
	})

	attempts := getAttempts(ginCtx)
	if len(attempts) == 0 {
		t.Fatal("expected at least one attempt")
	}

	requestText := attempts[0].request
	if strings.Contains(requestText, "password123") {
		t.Error("request body should be omitted when request-log is disabled")
	}
	if !strings.Contains(requestText, "<omitted for error log>") {
		t.Error("expected omission marker in request")
	}
	if !strings.Contains(requestText, "provider=test-provider") {
		t.Error("expected provider info to be present")
	}
	if !strings.Contains(requestText, "auth_id=auth-123") {
		t.Error("expected auth_id to be present")
	}
}

func TestRecordAPIRequest_IncludesBodyWhenRequestLogEnabled(t *testing.T) {
	t.Parallel()

	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(nil)
	ctx := context.WithValue(context.Background(), "gin", ginCtx)

	cfg := &config.Config{SDKConfig: config.SDKConfig{RequestLog: true}}

	recordAPIRequest(ctx, cfg, upstreamRequestLog{
		URL:    "https://example.com/api",
		Method: "POST",
		Body:   []byte(`{"data": "test-data"}`),
	})

	attempts := getAttempts(ginCtx)
	if len(attempts) == 0 {
		t.Fatal("expected at least one attempt")
	}

	requestText := attempts[0].request
	if !strings.Contains(requestText, "test-data") {
		t.Error("expected request body to be present when request-log is enabled")
	}
	if strings.Contains(requestText, "<omitted for error log>") {
		t.Error("should not have omission marker when request-log is enabled")
	}
}
