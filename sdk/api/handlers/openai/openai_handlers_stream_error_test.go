package openai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

const (
	initialFailureChatModel = "initial-failure-chat-model"
)

type initialFailureStreamExecutor struct{}

func (*initialFailureStreamExecutor) Identifier() string { return "initial-failure-stream-executor" }

func (*initialFailureStreamExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (*initialFailureStreamExecutor) ExecuteStream(_ context.Context, _ *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	chunks := make(chan coreexecutor.StreamChunk, 1)
	chunks <- coreexecutor.StreamChunk{Err: errors.New("upstream failed before first payload")}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (*initialFailureStreamExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (*initialFailureStreamExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (*initialFailureStreamExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func runOpenAIStreamErrorTest(t *testing.T, endpoint string, body string) {
	gin.SetMode(gin.TestMode)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			executor := &initialFailureStreamExecutor{}
			manager := coreauth.NewManager(nil, nil, nil)
			manager.RegisterExecutor(executor)
			authID := fmt.Sprintf("initial-failure-auth-%s-%d", strings.ReplaceAll(endpoint, "/", "-"), idx)
			auth := &coreauth.Auth{ID: authID, Provider: executor.Identifier(), Status: coreauth.StatusActive}
			if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
				t.Errorf("register auth %d: %v", idx, errRegister)
				return
			}
			registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: initialFailureChatModel}})
			defer registry.GetGlobalRegistry().UnregisterClient(auth.ID)

			base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
			h := NewOpenAIAPIHandler(base)
			router := gin.New()
			if endpoint == "/v1/chat/completions" {
				router.POST(endpoint, h.ChatCompletions)
			} else {
				router.POST(endpoint, h.Completions)
			}

			request := httptest.NewRequest(http.MethodPost, endpoint, strings.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)

			if recorder.Code == http.StatusOK {
				t.Errorf("[%s] request %d lost the buffered initial error and returned HTTP 200: %q", endpoint, idx, recorder.Body.String())
			}
			if !strings.Contains(recorder.Body.String(), "upstream failed before first payload") {
				t.Errorf("[%s] request %d lost the initial upstream error: status=%d body=%q", endpoint, idx, recorder.Code, recorder.Body.String())
			}
		}(i)
	}
	wg.Wait()
}

func TestChatCompletionsHandlerDoesNotLoseErrorBeforeFirstPayload(t *testing.T) {
	runOpenAIStreamErrorTest(t, "/v1/chat/completions", `{"model":"initial-failure-chat-model","messages":[{"role":"user","content":"hi"}],"stream":true}`)
}

func TestCompletionsHandlerDoesNotLoseErrorBeforeFirstPayload(t *testing.T) {
	runOpenAIStreamErrorTest(t, "/v1/completions", `{"model":"initial-failure-chat-model","prompt":"hi","stream":true}`)
}
