package openai

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

type delayedOpenAIExecutor struct {
	delay   time.Duration
	payload []byte
}

func (e *delayedOpenAIExecutor) Identifier() string { return "openai" }

func (e *delayedOpenAIExecutor) Execute(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	_ = auth
	_ = req
	_ = opts
	select {
	case <-time.After(e.delay):
		return coreexecutor.Response{Payload: e.payload}, nil
	case <-ctx.Done():
		return coreexecutor.Response{}, ctx.Err()
	}
}

func (e *delayedOpenAIExecutor) ExecuteStream(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	_ = ctx
	_ = auth
	_ = req
	_ = opts
	return nil, nil
}

func (e *delayedOpenAIExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	_ = ctx
	return auth, nil
}

func (e *delayedOpenAIExecutor) CountTokens(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	return e.Execute(ctx, auth, req, opts)
}

func (e *delayedOpenAIExecutor) HttpRequest(ctx context.Context, auth *coreauth.Auth, req *http.Request) (*http.Response, error) {
	_ = ctx
	_ = auth
	_ = req
	return nil, nil
}

func TestChatCompletionsNonStreamingEmitsKeepAliveWhileWaiting(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const model = "slow-openai-model"
	const authID = "openai-keepalive-test-auth"

	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(&delayedOpenAIExecutor{
		delay:   1200 * time.Millisecond,
		payload: []byte(`{"id":"chatcmpl-test"}`),
	})
	if _, err := manager.Register(context.Background(), &coreauth.Auth{
		ID:       authID,
		Provider: "openai",
		Status:   coreauth.StatusActive,
	}); err != nil {
		t.Fatalf("register auth: %v", err)
	}

	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(authID, "openai", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		reg.UnregisterClient(authID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{NonStreamKeepAliveInterval: 1}, manager)
	handler := NewOpenAIAPIHandler(base)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"`+model+`","messages":[{"role":"user","content":"hi"}]}`))

	handler.ChatCompletions(ctx)

	if body := rec.Body.String(); !strings.HasPrefix(body, "\n") {
		t.Fatalf("expected non-streaming chat completions to emit keepalive before final JSON, got %q", body)
	}
}
