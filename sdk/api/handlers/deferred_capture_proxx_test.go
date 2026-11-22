package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

// Without the request-logging middleware (embedded SDK routers, skipped paths) the
// execution context must still carry a request-scoped capture, so executor stream
// goroutines that outlive the handler never fall back to the pooled gin context.
func TestGetContextWithCancelAttachesDeferredCaptureWithoutMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	handler := &BaseAPIHandler{Cfg: &config.SDKConfig{}}
	ctx, cancel := handler.GetContextWithCancel(nil, ginCtx, context.Background())
	defer cancel()

	capture := logging.DeferredAPICaptureFromContext(ctx)
	if capture == nil {
		t.Fatal("execution context has no request-scoped DeferredAPICapture")
	}
	value, exists := ginCtx.Get(logging.DeferredAPIResponseContextKey)
	if !exists || value != capture {
		t.Fatalf("gin context capture = %v (exists=%v), want the context capture so the request-log finalizer sees it", value, exists)
	}
}

func TestGetContextWithCancelReusesMiddlewareDeferredCapture(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	existing := logging.NewDeferredAPICapture()
	ginCtx.Set(logging.DeferredAPIResponseContextKey, existing)

	handler := &BaseAPIHandler{Cfg: &config.SDKConfig{}}
	ctx, cancel := handler.GetContextWithCancel(nil, ginCtx, context.Background())
	defer cancel()

	if got := logging.DeferredAPICaptureFromContext(ctx); got != existing {
		t.Fatalf("context capture = %p, want the existing gin capture %p", got, existing)
	}
}

func TestGetContextWithCancelKeepsDeferredCaptureDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	handler := &BaseAPIHandler{Cfg: &config.SDKConfig{}}
	ctx, cancel := handler.GetContextWithCancel(nil, ginCtx, logging.WithDeferredAPIRequestCaptureDisabled(context.Background()))
	defer cancel()

	if capture := logging.DeferredAPICaptureFromContext(ctx); capture != nil {
		t.Fatal("capture attached although deferred capture is disabled")
	}
	if _, exists := ginCtx.Get(logging.DeferredAPIResponseContextKey); exists {
		t.Fatal("capture stored on gin context although deferred capture is disabled")
	}
}
