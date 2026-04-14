package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/logging"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	"golang.org/x/net/context"
)

func TestRequestExecutionMetadataUsesIdempotencyHeader(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ginCtx.Request.Header.Set("Idempotency-Key", "client-key")
	logging.SetGinRequestID(ginCtx, "req-ignored")

	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	meta := requestExecutionMetadata(ctx)

	if got := meta[idempotencyKeyMetadataKey]; got != "client-key" {
		t.Fatalf("idempotency key = %v, want client-key", got)
	}
}

func TestRequestExecutionMetadataFallsBackToRequestID(t *testing.T) {
	ctx := logging.WithRequestID(context.Background(), "req-1234")
	meta := requestExecutionMetadata(ctx)

	if got := meta[idempotencyKeyMetadataKey]; got != "req-1234" {
		t.Fatalf("idempotency key = %v, want req-1234", got)
	}
}

func TestRequestExecutionMetadataIncludesExecutionHints(t *testing.T) {
	base := logging.WithRequestID(context.Background(), "req-5678")
	base = WithPinnedAuthID(base, "auth-1")
	base = WithExecutionSessionID(base, "session-1")

	callbackCalled := false
	base = WithSelectedAuthIDCallback(base, func(authID string) {
		callbackCalled = authID != ""
	})

	meta := requestExecutionMetadata(base)
	if got := meta[idempotencyKeyMetadataKey]; got != "req-5678" {
		t.Fatalf("idempotency key = %v, want req-5678", got)
	}
	if got := meta[coreexecutor.PinnedAuthMetadataKey]; got != "auth-1" {
		t.Fatalf("pinned auth = %v, want auth-1", got)
	}
	if got := meta[coreexecutor.ExecutionSessionMetadataKey]; got != "session-1" {
		t.Fatalf("execution session = %v, want session-1", got)
	}
	callback, ok := meta[coreexecutor.SelectedAuthCallbackMetadataKey].(func(string))
	if !ok || callback == nil {
		t.Fatalf("selected auth callback missing")
	}
	callback("auth-1")
	if !callbackCalled {
		t.Fatalf("selected auth callback was not preserved")
	}
}
