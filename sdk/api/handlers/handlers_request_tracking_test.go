package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestGetContextWithCancelInheritsRequestUsageTracker(t *testing.T) {
	gin.SetMode(gin.TestMode)
	ginContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	requestCtx, tracker := usage.WithRequestTracking(request.Context())
	ginContext.Request = request.WithContext(requestCtx)

	handler := NewBaseAPIHandlers(&config.SDKConfig{}, nil)
	executionCtx, cancel := handler.GetContextWithCancel(nil, ginContext, context.Background())
	defer cancel(nil)
	usage.PublishRecord(executionCtx, usage.Record{Provider: "test", Model: "test-model"})

	if !tracker.Published() {
		t.Fatal("execution usage did not mark the incoming request tracker")
	}
}
