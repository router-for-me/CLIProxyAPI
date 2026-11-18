package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

type executionContextParentKey struct{}

func TestGetContextWithCancelRetainsExecutionStateAcrossGinReuse(t *testing.T) {
	ginCtx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ownerA, ownerB := &struct{ id int }{1}, &struct{ id int }{2}
	requestCtx, cancelRequest := context.WithCancel(coreauth.WithAuthExecutionState(context.Background(), ownerA))
	defer cancelRequest()
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(requestCtx)
	parent := context.WithValue(context.Background(), executionContextParentKey{}, "retained")
	handler := &BaseAPIHandler{Cfg: &config.SDKConfig{}}
	ctx, cancel := handler.GetContextWithCancel(nil, ginCtx, parent)
	defer cancel()
	if coreauth.AuthExecutionState(ctx) != ownerA || ctx.Value(executionContextParentKey{}) != "retained" {
		t.Fatal("execution context lost server state or parent values")
	}
	// Simulate Gin reuse without asking the old execution to read Gin again.
	ginCtx.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(coreauth.WithAuthExecutionState(context.Background(), ownerB))
	ginCtx.Set("execution-owner", ownerB)
	cancelRequest()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("request cancellation did not reach execution context")
	}
	if coreauth.AuthExecutionState(ctx) != ownerA || ctx.Err() != context.Canceled || parent.Err() != nil {
		t.Fatal("Gin reuse changed execution authority or parent lifetime")
	}
}
