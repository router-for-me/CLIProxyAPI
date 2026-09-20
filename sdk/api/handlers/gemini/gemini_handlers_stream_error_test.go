package gemini

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
	initialFailureGeminiModel = "initial-failure-gemini-model"
)

type initialFailureGeminiStreamExecutor struct{}

func (*initialFailureGeminiStreamExecutor) Identifier() string {
	return "initial-failure-gemini-stream-executor"
}

func (*initialFailureGeminiStreamExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (*initialFailureGeminiStreamExecutor) ExecuteStream(_ context.Context, _ *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	chunks := make(chan coreexecutor.StreamChunk, 1)
	chunks <- coreexecutor.StreamChunk{Err: errors.New("upstream failed before first payload")}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (*initialFailureGeminiStreamExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (*initialFailureGeminiStreamExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (*initialFailureGeminiStreamExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func TestGeminiStreamGenerateContentDoesNotLoseErrorBeforeFirstPayload(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			executor := &initialFailureGeminiStreamExecutor{}
			manager := coreauth.NewManager(nil, nil, nil)
			manager.RegisterExecutor(executor)
			authID := fmt.Sprintf("initial-failure-gemini-auth-%d", idx)
			auth := &coreauth.Auth{ID: authID, Provider: executor.Identifier(), Status: coreauth.StatusActive}
			if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
				t.Errorf("register auth %d: %v", idx, errRegister)
				return
			}
			registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: initialFailureGeminiModel}})
			defer registry.GetGlobalRegistry().UnregisterClient(auth.ID)

			base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
			h := NewGeminiAPIHandler(base)
			router := gin.New()
			router.POST("/v1beta/models/*action", h.GeminiHandler)

			request := httptest.NewRequest(http.MethodPost, "/v1beta/models/initial-failure-gemini-model:streamGenerateContent", strings.NewReader(`{"contents":[{"parts":[{"text":"hi"}]}]}`))
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)

			if recorder.Code == http.StatusOK {
				t.Errorf("request %d lost the buffered initial error and returned HTTP 200: %q", idx, recorder.Body.String())
			}
			if !strings.Contains(recorder.Body.String(), "upstream failed before first payload") {
				t.Errorf("request %d lost the initial upstream error: status=%d body=%q", idx, recorder.Code, recorder.Body.String())
			}
		}(i)
	}
	wg.Wait()
}
