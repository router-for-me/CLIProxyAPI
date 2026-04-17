package helps_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/runtime/executor/helps"
)

func TestHeaderFromContext_NilSafe(t *testing.T) {
	// No gin context in ctx — must return nil, not panic.
	got := helps.HeaderFromContext(context.Background())
	if got != nil {
		t.Fatalf("expected nil header for bare context, got %v", got)
	}
}

func TestHeaderFromContext_PicksGinContextHeader(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("X-Custom", "hello")
	ginCtx := &gin.Context{Request: req}
	ctx := context.WithValue(context.Background(), "gin", ginCtx)

	got := helps.HeaderFromContext(ctx)
	if got == nil {
		t.Fatal("expected non-nil header")
	}
	if got.Get("X-Custom") != "hello" {
		t.Fatalf("expected X-Custom=hello, got %q", got.Get("X-Custom"))
	}
}
