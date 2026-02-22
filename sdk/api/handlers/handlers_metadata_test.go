package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
)

func requestContextWithHeader(t *testing.T, idempotencyKey string) context.Context {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}

	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginCtx.Request = req
	return context.WithValue(context.Background(), ginContextLookupKeyToken, ginCtx)
}

func TestRequestExecutionMetadata_GeneratesIdempotencyKey(t *testing.T) {
	meta1 := requestExecutionMetadata(context.Background())
	meta2 := requestExecutionMetadata(context.Background())

	key1, ok := meta1[idempotencyKeyMetadataKey].(string)
	if !ok || key1 == "" {
		t.Fatalf("generated idempotency key missing or empty: %#v", meta1[idempotencyKeyMetadataKey])
	}

	key2, ok := meta2[idempotencyKeyMetadataKey].(string)
	if !ok || key2 == "" {
		t.Fatalf("generated idempotency key missing or empty: %#v", meta2[idempotencyKeyMetadataKey])
	}
}

func TestRequestExecutionMetadata_PreservesHeaderAndContextMetadata(t *testing.T) {
	sessionID := "session-123"
	authID := "auth-456"
	callback := func(id string) {}

	ctx := requestContextWithHeader(t, "request-key-1")
	ctx = WithPinnedAuthID(ctx, authID)
	ctx = WithSelectedAuthIDCallback(ctx, callback)
	ctx = WithExecutionSessionID(ctx, sessionID)

	meta := requestExecutionMetadata(ctx)

	if got := meta[idempotencyKeyMetadataKey].(string); got != "request-key-1" {
		t.Fatalf("Idempotency-Key mismatch: got %q want %q", got, "request-key-1")
	}

	if got := meta[coreexecutor.PinnedAuthMetadataKey].(string); got != authID {
		t.Fatalf("pinned auth id mismatch: got %q want %q", got, authID)
	}

	if cb, ok := meta[coreexecutor.SelectedAuthCallbackMetadataKey].(func(string)); !ok || cb == nil {
		t.Fatalf("selected auth callback metadata missing: %#v", meta[coreexecutor.SelectedAuthCallbackMetadataKey])
	}

	if got := meta[coreexecutor.ExecutionSessionMetadataKey].(string); got != sessionID {
		t.Fatalf("execution session id mismatch: got %q want %q", got, sessionID)
	}
}

func TestRequestExecutionMetadata_UsesProvidedIdempotencyKeyForRetries(t *testing.T) {
	ctx := requestContextWithHeader(t, "retry-key-7")
	first := requestExecutionMetadata(ctx)
	second := requestExecutionMetadata(ctx)

	firstKey, ok := first[idempotencyKeyMetadataKey].(string)
	if !ok || firstKey != "retry-key-7" {
		t.Fatalf("first request metadata missing idempotency key: %#v", first[idempotencyKeyMetadataKey])
	}
	secondKey, ok := second[idempotencyKeyMetadataKey].(string)
	if !ok || secondKey != "retry-key-7" {
		t.Fatalf("second request metadata missing idempotency key: %#v", second[idempotencyKeyMetadataKey])
	}
	if firstKey != secondKey {
		t.Fatalf("idempotency key should be stable for retry requests: got %q and %q", firstKey, secondKey)
	}
}
