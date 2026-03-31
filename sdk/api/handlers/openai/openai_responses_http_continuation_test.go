package openai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
	"github.com/tidwall/gjson"
)

type responsesContinuationCaptureExecutor struct {
	calls    int
	payloads [][]byte
}

func (e *responsesContinuationCaptureExecutor) Identifier() string { return "test-provider" }

func (e *responsesContinuationCaptureExecutor) Execute(ctx context.Context, auth *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	e.calls++
	e.payloads = append(e.payloads, append([]byte(nil), req.Payload...))
	if e.calls == 1 {
		return coreexecutor.Response{Payload: []byte(`{"id":"resp-1","output":[{"type":"function_call","id":"fc-1","call_id":"call-1"},{"type":"message","id":"assistant-1"}]}`)}, nil
	}
	return coreexecutor.Response{Payload: []byte(`{"id":"resp-2","output":[{"type":"message","id":"assistant-2"}]}`)}, nil
}

func (e *responsesContinuationCaptureExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, errors.New("not implemented")
}

func (e *responsesContinuationCaptureExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *responsesContinuationCaptureExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *responsesContinuationCaptureExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func TestOpenAIResponsesHTTPContinuationMergesCachedTurn(t *testing.T) {
	gin.SetMode(gin.TestMode)
	executor := &responsesContinuationCaptureExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth := &coreauth.Auth{ID: "auth-http-cont", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("Register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.POST("/v1/responses", h.Responses)

	firstReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"test-model","input":[{"type":"message","id":"msg-1"}]}`))
	firstReq.Header.Set("Content-Type", "application/json")
	firstResp := httptest.NewRecorder()
	router.ServeHTTP(firstResp, firstReq)
	if firstResp.Code != http.StatusOK {
		t.Fatalf("first status = %d, want %d", firstResp.Code, http.StatusOK)
	}

	secondReq := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"test-model","previous_response_id":"resp-1","input":[{"type":"function_call_output","call_id":"call-1","id":"tool-out-1"}]}`))
	secondReq.Header.Set("Content-Type", "application/json")
	secondResp := httptest.NewRecorder()
	router.ServeHTTP(secondResp, secondReq)
	if secondResp.Code != http.StatusOK {
		t.Fatalf("second status = %d, want %d body=%s", secondResp.Code, http.StatusOK, secondResp.Body.String())
	}

	if executor.calls != 2 {
		t.Fatalf("executor calls = %d, want 2", executor.calls)
	}
	if gjson.GetBytes(executor.payloads[1], "previous_response_id").Exists() {
		t.Fatalf("second payload must not include previous_response_id: %s", executor.payloads[1])
	}
	input := gjson.GetBytes(executor.payloads[1], "input").Array()
	if len(input) != 4 {
		t.Fatalf("merged input len = %d, want 4: %s", len(input), executor.payloads[1])
	}
	if input[0].Get("id").String() != "msg-1" ||
		input[1].Get("id").String() != "fc-1" ||
		input[2].Get("id").String() != "assistant-1" ||
		input[3].Get("id").String() != "tool-out-1" {
		t.Fatalf("unexpected merged input order: %s", executor.payloads[1])
	}
}

func TestNormalizeContinuationRequestSupportsStringInputShorthand(t *testing.T) {
	h := &OpenAIResponsesAPIHandler{}
	h.rememberCompletedResponse(
		[]byte(`{"model":"test-model","input":"Use the weather tool for Paris."}`),
		[]byte(`{"id":"resp-str-1","output":[{"type":"function_call","id":"fc-1","call_id":"call-1"},{"type":"message","id":"assistant-1"}]}`),
	)

	normalized, errMsg := h.normalizeContinuationRequest([]byte(`{"previous_response_id":"resp-str-1","input":[{"type":"function_call_output","call_id":"call-1","id":"tool-out-1"}]}`))
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}
	input := gjson.GetBytes(normalized, "input").Array()
	if len(input) != 4 {
		t.Fatalf("merged input len = %d, want 4: %s", len(input), normalized)
	}
	if input[0].Get("role").String() != "user" {
		t.Fatalf("expected normalized first item to be user message: %s", normalized)
	}
	if input[0].Get("content").String() != "Use the weather tool for Paris." {
		t.Fatalf("unexpected normalized first item content: %s", normalized)
	}
}

func TestRememberCompletedResponseFromChunkCachesStreamingTurn(t *testing.T) {
	h := &OpenAIResponsesAPIHandler{}
	requestJSON := []byte(`{"model":"test-model","input":[{"type":"message","id":"msg-1"}]}`)
	chunk := []byte(`event: response.completed
data: {"type":"response.completed","response":{"id":"resp-stream-1","output":[{"type":"function_call","id":"fc-1","call_id":"call-1"},{"type":"message","id":"assistant-1"}]}}

`)
	h.rememberCompletedResponseFromChunk(requestJSON, chunk)

	normalized, errMsg := h.normalizeContinuationRequest([]byte(`{"previous_response_id":"resp-stream-1","input":[{"type":"function_call_output","call_id":"call-1","id":"tool-out-1"}]}`))
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}
	input := gjson.GetBytes(normalized, "input").Array()
	if len(input) != 4 {
		t.Fatalf("merged input len = %d, want 4: %s", len(input), normalized)
	}
	if input[3].Get("id").String() != "tool-out-1" {
		t.Fatalf("unexpected merged payload: %s", normalized)
	}
}
