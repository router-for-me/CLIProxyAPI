package openai

import (
	"context"
	"errors"
	"fmt"
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

type statusCodeError struct {
	code int
	msg  string
}

func (e statusCodeError) Error() string   { return e.msg }
func (e statusCodeError) StatusCode() int { return e.code }

type responsesFallbackExecutor struct {
	calls            []string
	payloads         [][]byte
	originalRequests [][]byte
	responsesErrMsg  string
	streamPayload    []byte
}

func (e *responsesFallbackExecutor) Identifier() string { return "test-responses-fallback" }

func (e *responsesFallbackExecutor) Execute(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	e.calls = append(e.calls, opts.SourceFormat.String())
	e.payloads = append(e.payloads, append([]byte(nil), req.Payload...))
	e.originalRequests = append(e.originalRequests, append([]byte(nil), opts.OriginalRequest...))
	if opts.SourceFormat.String() == "openai-response" {
		return coreexecutor.Response{}, statusCodeError{code: http.StatusNotFound, msg: e.openAIResponsesErrorMessage()}
	}
	return coreexecutor.Response{Payload: []byte(`{"id":"chatcmpl-test","object":"chat.completion","created":123,"model":"test-model","choices":[{"index":0,"message":{"role":"assistant","content":"fallback ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":2,"completion_tokens":3,"total_tokens":5}}`)}, nil
}

func (e *responsesFallbackExecutor) ExecuteStream(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	e.calls = append(e.calls, opts.SourceFormat.String())
	e.payloads = append(e.payloads, append([]byte(nil), req.Payload...))
	e.originalRequests = append(e.originalRequests, append([]byte(nil), opts.OriginalRequest...))
	if opts.SourceFormat.String() == "openai-response" {
		return nil, statusCodeError{code: http.StatusNotFound, msg: e.openAIResponsesErrorMessage()}
	}
	chunks := make(chan coreexecutor.StreamChunk, 1)
	chunks <- coreexecutor.StreamChunk{Payload: e.streamPayload}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *responsesFallbackExecutor) openAIResponsesErrorMessage() string {
	if e.responsesErrMsg != "" {
		return e.responsesErrMsg
	}
	return "404 page not found"
}

func (e *responsesFallbackExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *responsesFallbackExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *responsesFallbackExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func newResponsesFallbackTestRouter(t *testing.T, executor *responsesFallbackExecutor) *gin.Engine {
	return newResponsesFallbackTestRouterWithCompatAuth(t, executor, true)
}

func newResponsesFallbackTestRouterWithCompatAuth(t *testing.T, executor *responsesFallbackExecutor, compatAuth bool) *gin.Engine {
	t.Helper()

	gin.SetMode(gin.TestMode)
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	authID := fmt.Sprintf("auth-%d", time.Now().UnixNano())
	auth := &coreauth.Auth{ID: authID, Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if compatAuth {
		auth.Attributes = map[string]string{
			"api_key":      "test-key",
			"compat_name":  executor.Identifier(),
			"provider_key": executor.Identifier(),
		}
	}
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
	return router
}

func TestOpenAIResponsesFallbackToChatCompletionsOn404(t *testing.T) {
	executor := &responsesFallbackExecutor{}
	router := newResponsesFallbackTestRouter(t, executor)

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"test-model","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if got := strings.Join(executor.calls, ","); got != "openai-response,openai" {
		t.Fatalf("executor calls = %q, want openai-response,openai", got)
	}
	body := resp.Body.String()
	if !strings.Contains(body, `"object":"response"`) || !strings.Contains(body, `"text":"fallback ok"`) {
		t.Fatalf("body was not converted to responses format: %s", body)
	}
	if !strings.Contains(string(executor.payloads[1]), `"messages"`) {
		t.Fatalf("fallback payload was not chat completions format: %s", string(executor.payloads[1]))
	}
	if string(executor.originalRequests[1]) != string(executor.payloads[1]) {
		t.Fatalf("fallback original request did not match converted payload: original=%s payload=%s", string(executor.originalRequests[1]), string(executor.payloads[1]))
	}
}

func TestOpenAIResponsesFallbackRequiresOpenAICompatibleAuth(t *testing.T) {
	executor := &responsesFallbackExecutor{}
	router := newResponsesFallbackTestRouterWithCompatAuth(t, executor, false)

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"test-model","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d, body: %s", resp.Code, http.StatusNotFound, resp.Body.String())
	}
	if got := strings.Join(executor.calls, ","); got != "openai-response" {
		t.Fatalf("executor calls = %q, want only openai-response", got)
	}
}

func TestOpenAIResponsesFallbackRequiresEndpointNotFoundBody(t *testing.T) {
	executor := &responsesFallbackExecutor{responsesErrMsg: `{"error":{"message":"model not found"}}`}
	router := newResponsesFallbackTestRouter(t, executor)

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"test-model","input":"hello"}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d, body: %s", resp.Code, http.StatusNotFound, resp.Body.String())
	}
	if got := strings.Join(executor.calls, ","); got != "openai-response" {
		t.Fatalf("executor calls = %q, want only openai-response", got)
	}
}

func TestOpenAIResponsesStreamFallbackToChatCompletionsOn404(t *testing.T) {
	executor := &responsesFallbackExecutor{
		streamPayload: []byte(`data: {"id":"chatcmpl-test","object":"chat.completion.chunk","created":123,"model":"test-model","choices":[{"index":0,"delta":{"role":"assistant","content":"stream ok"},"finish_reason":"stop"}]}`),
	}
	router := newResponsesFallbackTestRouter(t, executor)

	req := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"test-model","input":"hello","stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body: %s", resp.Code, http.StatusOK, resp.Body.String())
	}
	if got := strings.Join(executor.calls, ","); got != "openai-response,openai" {
		t.Fatalf("executor calls = %q, want openai-response,openai", got)
	}
	body := resp.Body.String()
	if !strings.Contains(body, "response.output_text.delta") || !strings.Contains(body, "stream ok") || !strings.Contains(body, "response.completed") {
		t.Fatalf("stream was not converted to responses SSE: %s", body)
	}
	if string(executor.originalRequests[1]) != string(executor.payloads[1]) {
		t.Fatalf("fallback original request did not match converted payload: original=%s payload=%s", string(executor.originalRequests[1]), string(executor.payloads[1]))
	}
}
