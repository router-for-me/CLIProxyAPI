package auth

import (
	"context"
	"net/http"
	"testing"
)

type homeDispatchTestGinContext struct {
	values map[string]any
	query  map[string]string
}

func (c homeDispatchTestGinContext) Get(key string) (any, bool) {
	v, ok := c.values[key]
	return v, ok
}

func (c homeDispatchTestGinContext) Query(key string) string {
	if c.query == nil {
		return ""
	}
	return c.query[key]
}

func TestHomeDispatchHeadersAddsQueryKeyCredential(t *testing.T) {
	ginCtx := homeDispatchTestGinContext{query: map[string]string{"key": "12345"}}
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	headers := http.Header{"User-Agent": {"client"}}

	got := homeDispatchHeaders(ctx, headers)

	if got.Get("X-Goog-Api-Key") != "12345" {
		t.Fatalf("X-Goog-Api-Key = %q, want %q", got.Get("X-Goog-Api-Key"), "12345")
	}
	if headers.Get("X-Goog-Api-Key") != "" {
		t.Fatalf("original headers were mutated: %v", headers)
	}
}

func TestHomeDispatchHeadersAddsQueryCredentialFromAccessMetadata(t *testing.T) {
	ginCtx := homeDispatchTestGinContext{values: map[string]any{
		"accessMetadata": map[string]string{"source": "query-key"},
		"userApiKey":     "12345",
	}}
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	headers := http.Header{"User-Agent": {"client"}}

	got := homeDispatchHeaders(ctx, headers)

	if got.Get("X-Goog-Api-Key") != "12345" {
		t.Fatalf("X-Goog-Api-Key = %q, want %q", got.Get("X-Goog-Api-Key"), "12345")
	}
	if headers.Get("X-Goog-Api-Key") != "" {
		t.Fatalf("original headers were mutated: %v", headers)
	}
}

func TestHomeDispatchHeadersKeepsExistingCredentialHeader(t *testing.T) {
	ginCtx := homeDispatchTestGinContext{query: map[string]string{"key": "query-key"}}
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	headers := http.Header{"X-Goog-Api-Key": {"header-key"}}

	got := homeDispatchHeaders(ctx, headers)

	if got.Get("X-Goog-Api-Key") != "header-key" {
		t.Fatalf("X-Goog-Api-Key = %q, want %q", got.Get("X-Goog-Api-Key"), "header-key")
	}
}

func TestHomeDispatchHeadersIgnoresHeaderCredentialSource(t *testing.T) {
	ginCtx := homeDispatchTestGinContext{values: map[string]any{
		"accessMetadata": map[string]string{"source": "authorization"},
		"userApiKey":     "12345",
	}}
	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	headers := http.Header{"Authorization": {"Bearer 12345"}}

	got := homeDispatchHeaders(ctx, headers)

	if got.Get("X-Goog-Api-Key") != "" {
		t.Fatalf("X-Goog-Api-Key = %q, want empty", got.Get("X-Goog-Api-Key"))
	}
	if got.Get("Authorization") != "Bearer 12345" {
		t.Fatalf("Authorization = %q, want %q", got.Get("Authorization"), "Bearer 12345")
	}
}
