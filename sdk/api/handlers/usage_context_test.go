package handlers

import (
	"context"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

func TestGetContextWithCancelCopiesScopedUsageManager(t *testing.T) {
	gin.SetMode(gin.TestMode)
	usageManager := coreusage.NewManager(1)
	request := httptest.NewRequest("POST", "/v1/responses", nil)
	request = request.WithContext(coreusage.WithManager(request.Context(), usageManager))
	recorder := httptest.NewRecorder()
	ginContext, _ := gin.CreateTestContext(recorder)
	ginContext.Request = request
	handler := NewBaseAPIHandlers(&config.SDKConfig{}, nil)

	ctx, cancel := handler.GetContextWithCancel(nil, ginContext, context.Background())
	defer cancel()
	if got := coreusage.ManagerFromContext(ctx); got != usageManager {
		t.Fatalf("scoped usage manager = %p, want %p", got, usageManager)
	}
}
