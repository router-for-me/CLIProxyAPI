package handlers

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestRequestHeadersFromContext(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(w)
	req := httptest.NewRequest("POST", "http://example.test/v1/responses", nil)
	req.Header.Set("session_id", "s123")
	req.Header.Set("Authorization", "Bearer k1")
	req.Header.Set("User-Agent", "ua-test")
	ginCtx.Request = req

	ctx := context.WithValue(context.Background(), "gin", ginCtx)
	headers := requestHeaders(ctx)
	if headers == nil {
		t.Fatal("expected non-nil headers")
	}
	if got := headers.Get("session_id"); got != "s123" {
		t.Fatalf("expected session_id header, got %q", got)
	}
	if got := headers.Get("authorization"); got == "" {
		t.Fatalf("expected authorization header, got %q", got)
	}
	if got := headers.Get("user-agent"); got != "ua-test" {
		t.Fatalf("expected user-agent header, got %q", got)
	}
}
