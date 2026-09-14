package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

const reproOAuthMessage = `token refresh failed with status 401: {"error":{"message":"Refresh credential has already been consumed; sign in again.","type":"invalid_request_error","code":"refresh_token_reused"}}`
const reproCompletedResponse = `{"id":"resp-fixture","object":"response","status":"completed","output":[]}`

type reproOAuthExecutor struct{ calls atomic.Int32 }

func (*reproOAuthExecutor) Identifier() string { return "codex" }
func (e *reproOAuthExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	e.calls.Add(1)
	return coreexecutor.Response{Payload: []byte(reproCompletedResponse)}, nil
}
func (e *reproOAuthExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	e.calls.Add(1)
	chunks := make(chan coreexecutor.StreamChunk, 1)
	chunks <- coreexecutor.StreamChunk{Payload: []byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":" + reproCompletedResponse + "}\n\n")}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}
func (*reproOAuthExecutor) Refresh(context.Context, *coreauth.Auth) (*coreauth.Auth, error) {
	return nil, errors.New(reproOAuthMessage)
}
func (*reproOAuthExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("unused fixture method")
}
func (*reproOAuthExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("network is not used in this reproduction")
}

func TestReproPermanentOAuthFailureClassification(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			model := fmt.Sprintf("repro-permanent-oauth-%t", stream)
			failedID := "failed-" + model
			healthyID := "healthy-" + model
			executor := &reproOAuthExecutor{}
			manager := coreauth.NewManager(nil, nil, nil)
			manager.RegisterExecutor(executor)
			defer manager.StopAutoRefresh()

			// This is the terminal state produced by refreshAuth when a 401
			// refresh failure has no remaining valid access token. The upstream
			// TestManager_RefreshAuthUnauthorizedFailureStopsAutoRefreshRetry
			// independently verifies that transition and unscheduling behavior.
			failed := &coreauth.Auth{
				ID: failedID, Provider: "codex", Status: coreauth.StatusError,
				Unavailable: true, StatusMessage: "unauthorized",
				LastError: &coreauth.Error{Code: "unauthorized", Message: reproOAuthMessage,
					Retryable: false, HTTPStatus: http.StatusUnauthorized},
				Metadata: map[string]any{"access_token": "expired-fixture",
					"expired": time.Now().Add(-time.Hour).Format(time.RFC3339)},
			}
			if _, errRegister := manager.Register(context.Background(), failed); errRegister != nil {
				t.Fatal(errRegister)
			}
			registry.GetGlobalRegistry().RegisterClient(failedID, "codex", []*registry.ModelInfo{{ID: model}})
			t.Cleanup(func() {
				registry.GetGlobalRegistry().UnregisterClient(failedID)
				registry.GetGlobalRegistry().UnregisterClient(healthyID)
			})
			base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
			router := gin.New()
			router.POST("/v1/responses", NewOpenAIResponsesAPIHandler(base).Responses)
			request := func() *httptest.ResponseRecorder {
				body := fmt.Sprintf(`{"model":%q,"input":"fixture","stream":%t}`, model, stream)
				req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(body))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("User-Agent", "codex_cli_rs/0.153.4")
				recorder := httptest.NewRecorder()
				router.ServeHTTP(recorder, req)
				return recorder
			}

			for attempt := 1; attempt <= 3; attempt++ {
				got := request()
				var payload struct {
					Error struct {
						Code, Type, Message string
						Retryable           *bool `json:"retryable"`
					} `json:"error"`
				}
				if errUnmarshal := json.Unmarshal(got.Body.Bytes(), &payload); errUnmarshal != nil {
					t.Fatalf("decode HTTP %d body %q: %v", got.Code, got.Body.String(), errUnmarshal)
				}
				t.Logf("attempt=%d HTTP=%d code=%q type=%q retryable=%v executor_calls=%d body=%s", attempt,
					got.Code, payload.Error.Code, payload.Error.Type, payload.Error.Retryable, executor.calls.Load(), got.Body.String())

				if got.Code != http.StatusServiceUnavailable {
					t.Errorf("attempt %d: HTTP status = %d, want %d", attempt, got.Code, http.StatusServiceUnavailable)
				}
				if payload.Error.Type != "authentication_error" {
					t.Errorf("attempt %d: Error.Type = %q, want %q", attempt, payload.Error.Type, "authentication_error")
				}
				if payload.Error.Code != "upstream_authentication_required" {
					t.Errorf("attempt %d: Error.Code = %q, want %q", attempt, payload.Error.Code, "upstream_authentication_required")
				}
				if payload.Error.Retryable == nil || *payload.Error.Retryable {
					t.Errorf("attempt %d: Error.Retryable = %v, want explicit false", attempt, payload.Error.Retryable)
				}
				if !strings.Contains(payload.Error.Message, "refresh_token_reused") {
					t.Errorf("attempt %d: terminal OAuth cause missing from response: %s", attempt, got.Body.String())
				}
				if !strings.Contains(payload.Error.Message, "[REDACTED]") {
					t.Errorf("attempt %d: sensitive token details not redacted in message: %s", attempt, payload.Error.Message)
				}
			}
			if executor.calls.Load() != 0 {
				t.Errorf("terminal auth reached executor %d times", executor.calls.Load())
			}

			// A healthy candidate must make this same route succeed, proving the
			// earlier errors are not missing model registration or a bad handler.
			healthy := &coreauth.Auth{ID: healthyID, Provider: "codex", Status: coreauth.StatusActive}
			if _, errRegisterHealthy := manager.Register(context.Background(), healthy); errRegisterHealthy != nil {
				t.Fatal(errRegisterHealthy)
			}
			registry.GetGlobalRegistry().RegisterClient(healthyID, "codex", []*registry.ModelInfo{{ID: model}})
			control := request()
			if control.Code != http.StatusOK || executor.calls.Load() != 1 {
				t.Errorf("healthy control HTTP=%d executor_calls=%d body=%s", control.Code, executor.calls.Load(), control.Body.String())
			}
			t.Logf("healthy control HTTP=%d executor_calls=%d", control.Code, executor.calls.Load())
		})
	}
}
