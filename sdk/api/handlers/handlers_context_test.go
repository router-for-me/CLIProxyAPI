package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/logging"
	"golang.org/x/net/context"
)

type testAPIHandler struct{}

func (testAPIHandler) HandlerType() string      { return "test" }
func (testAPIHandler) Models() []map[string]any { return nil }

func TestGetContextWithCancel_UsesRequestContextCancellationForBackgroundParent(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodGet, "/v1/test", nil)
	reqCtx, reqCancel := context.WithCancel(req.Context())
	defer reqCancel()
	ginCtx.Request = req.WithContext(reqCtx)

	handler := &BaseAPIHandler{}
	cliCtx, cliCancel := handler.GetContextWithCancel(testAPIHandler{}, ginCtx, context.Background())
	defer cliCancel()

	reqCancel()

	select {
	case <-cliCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("request cancellation did not propagate")
	}
}

func TestGetContextWithCancel_PreservesParentValuesWhileBridgingRequestCancellation(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodGet, "/v1/test", nil)
	reqCtx, reqCancel := context.WithCancel(req.Context())
	defer reqCancel()
	ginCtx.Request = req.WithContext(reqCtx)

	parent := WithPinnedAuthID(context.Background(), "auth-1")

	handler := &BaseAPIHandler{}
	cliCtx, cliCancel := handler.GetContextWithCancel(testAPIHandler{}, ginCtx, parent)
	defer cliCancel()

	if got := pinnedAuthIDFromContext(cliCtx); got != "auth-1" {
		t.Fatalf("pinned auth id = %q, want auth-1", got)
	}

	reqCancel()

	select {
	case <-cliCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("bridged request cancellation did not propagate")
	}
}

func TestGetContextWithCancel_PreservesRequestIDFromRequestContext(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodGet, "/v1/test", nil)
	reqCtx := logging.WithRequestID(req.Context(), "req-123")
	ginCtx.Request = req.WithContext(reqCtx)

	handler := &BaseAPIHandler{}
	cliCtx, cliCancel := handler.GetContextWithCancel(testAPIHandler{}, ginCtx, context.Background())
	defer cliCancel()

	if got := logging.GetRequestID(cliCtx); got != "req-123" {
		t.Fatalf("request id = %q, want req-123", got)
	}
}

func BenchmarkGetContextWithCancelBackgroundParent(b *testing.B) {
	gin.SetMode(gin.ReleaseMode)

	recorder := httptest.NewRecorder()
	ginCtx, _ := gin.CreateTestContext(recorder)
	ginCtx.Request = httptest.NewRequest(http.MethodGet, "/v1/test", nil)

	handler := &BaseAPIHandler{}
	apiHandler := testAPIHandler{}

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, cancel := handler.GetContextWithCancel(apiHandler, ginCtx, context.Background())
		cancel()
	}
}

var _ interfaces.APIHandler = testAPIHandler{}
