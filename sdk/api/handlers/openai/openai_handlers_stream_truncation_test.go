package openai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

const truncatedStreamChatModel = "truncated-stream-chat-model"

type mockStreamExecutor struct {
	chunks []string
}

func (*mockStreamExecutor) Identifier() string { return "mock-stream-executor" }

func (*mockStreamExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *mockStreamExecutor) ExecuteStream(_ context.Context, _ *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	chunks := make(chan coreexecutor.StreamChunk, len(e.chunks))
	for _, c := range e.chunks {
		chunks <- coreexecutor.StreamChunk{Payload: []byte(c)}
	}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (*mockStreamExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (*mockStreamExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (*mockStreamExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func runMockStreamTest(t *testing.T, chunks []string, authID string) string {
	t.Helper()
	gin.SetMode(gin.TestMode)

	executor := &mockStreamExecutor{chunks: chunks}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{ID: authID, Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: truncatedStreamChatModel}})
	defer registry.GetGlobalRegistry().UnregisterClient(auth.ID)

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIAPIHandler(base)
	router := gin.New()
	router.POST("/v1/chat/completions", h.ChatCompletions)

	body := `{"model":"truncated-stream-chat-model","messages":[{"role":"user","content":"hi"}],"stream":true}`
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder.Body.String()
}

// Issue #6371: A stream that emits multiple chunks and then cleanly closes without
// any chunk carrying finish_reason is truncated. It must NOT be answered with [DONE],
// but must surface an error so clients can detect truncation and retry.
func TestChatCompletionsStreamWithoutFinishReasonIsReportedAsError(t *testing.T) {
	chunks := []string{
		`{"choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"do_thing","arguments":"{\"a\":1"}}]}}]}`,
	}
	out := runMockStreamTest(t, chunks, "truncated-stream-auth-multi-chunk-missing")
	if strings.Contains(out, "data: [DONE]") {
		t.Errorf("truncated stream was terminated with [DONE] as if it had succeeded: %q", out)
	}
	if !strings.Contains(out, "finish_reason") {
		t.Errorf("truncated stream did not surface an error naming the missing terminator: %q", out)
	}
	if !strings.Contains(out, `"error":{"message":"upstream stream closed before any chunk carried finish_reason"`) {
		t.Errorf("truncated stream did not format structured error payload: %q", out)
	}
}

// Issue #6371: A stream with explicit null or empty finish_reason must still be detected as truncated.
func TestChatCompletionsStreamExplicitNullOrEmptyFinishReasonIsReportedAsError(t *testing.T) {
	chunks := []string{
		`{"choices":[{"index":0,"delta":{"role":"assistant","content":"part1"},"finish_reason":null}]}`,
		`{"choices":[{"index":0,"delta":{"content":"part2"},"finish_reason":""}]}`,
	}
	out := runMockStreamTest(t, chunks, "truncated-stream-auth-null-finish")
	if strings.Contains(out, "data: [DONE]") {
		t.Errorf("stream with null/empty finish_reason was terminated with [DONE]: %q", out)
	}
	if !strings.Contains(out, "finish_reason") {
		t.Errorf("stream with null/empty finish_reason did not surface error: %q", out)
	}
}

// Issue #6371: A stream that emits only 1 chunk without finish_reason and closes must also
// be flagged as truncated, not [DONE].
func TestChatCompletionsStreamSingleChunkWithoutFinishReasonIsReportedAsError(t *testing.T) {
	chunks := []string{
		`{"choices":[{"index":0,"delta":{"role":"assistant","content":"hello"}}]}`,
	}
	out := runMockStreamTest(t, chunks, "truncated-stream-auth-single-chunk-missing")
	if strings.Contains(out, "data: [DONE]") {
		t.Errorf("single-chunk truncated stream was terminated with [DONE] as if it had succeeded: %q", out)
	}
	if !strings.Contains(out, "finish_reason") {
		t.Errorf("single-chunk truncated stream did not surface an error naming the missing terminator: %q", out)
	}
}

// Control: A multi-chunk stream that does carry finish_reason must complete with [DONE].
func TestChatCompletionsStreamWithFinishReasonStillCompletes(t *testing.T) {
	chunks := []string{
		`{"choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"do_thing","arguments":"{\"a\":1}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`,
	}
	out := runMockStreamTest(t, chunks, "truncated-stream-auth-multi-chunk-present")
	if !strings.Contains(out, "data: [DONE]") {
		t.Errorf("complete stream was not terminated with [DONE]: %q", out)
	}
	if strings.Contains(out, "upstream stream closed before") {
		t.Errorf("complete stream was wrongly flagged as truncated: %q", out)
	}
}

// Control: A single-chunk stream that carries finish_reason must complete with [DONE].
func TestChatCompletionsStreamSingleChunkWithFinishReasonStillCompletes(t *testing.T) {
	chunks := []string{
		`{"choices":[{"index":0,"delta":{"content":"instant"},"finish_reason":"stop"}]}`,
	}
	out := runMockStreamTest(t, chunks, "truncated-stream-auth-single-chunk-present")
	if !strings.Contains(out, "data: [DONE]") {
		t.Errorf("single-chunk complete stream was not terminated with [DONE]: %q", out)
	}
	if strings.Contains(out, "upstream stream closed before") {
		t.Errorf("single-chunk complete stream was wrongly flagged as truncated: %q", out)
	}
}

// Issue #6371: Legacy /v1/completions stream that ends without finish_reason must also be flagged as error.
func TestCompletionsStreamWithoutFinishReasonIsReportedAsError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	chunks := []string{
		`{"choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
		`{"choices":[{"index":0,"delta":{"content":"trunc"}}]}`,
	}
	executor := &mockStreamExecutor{chunks: chunks}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{ID: "truncated-legacy-auth-missing", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "legacy-model"}})
	defer registry.GetGlobalRegistry().UnregisterClient(auth.ID)

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIAPIHandler(base)
	router := gin.New()
	router.POST("/v1/completions", h.Completions)

	body := `{"model":"legacy-model","prompt":"hi","stream":true}`
	request := httptest.NewRequest(http.MethodPost, "/v1/completions", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	out := recorder.Body.String()

	if strings.Contains(out, "data: [DONE]") {
		t.Errorf("truncated legacy completions stream was terminated with [DONE]: %q", out)
	}
	if !strings.Contains(out, "finish_reason") {
		t.Errorf("truncated legacy completions stream did not surface error: %q", out)
	}
}

// Control: Legacy /v1/completions stream that carries finish_reason completes with [DONE].
func TestCompletionsStreamWithFinishReasonStillCompletes(t *testing.T) {
	gin.SetMode(gin.TestMode)

	chunks := []string{
		`{"choices":[{"index":0,"delta":{"role":"assistant"}}]}`,
		`{"choices":[{"index":0,"delta":{"content":"hello"},"finish_reason":"stop"}]}`,
	}
	executor := &mockStreamExecutor{chunks: chunks}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{ID: "truncated-legacy-auth-present", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "legacy-model-ok"}})
	defer registry.GetGlobalRegistry().UnregisterClient(auth.ID)

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIAPIHandler(base)
	router := gin.New()
	router.POST("/v1/completions", h.Completions)

	body := `{"model":"legacy-model-ok","prompt":"hi","stream":true}`
	request := httptest.NewRequest(http.MethodPost, "/v1/completions", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	out := recorder.Body.String()

	if !strings.Contains(out, "data: [DONE]") {
		t.Errorf("complete legacy completions stream was not terminated with [DONE]: %q", out)
	}
	if strings.Contains(out, "upstream stream closed before") {
		t.Errorf("complete legacy completions stream was wrongly flagged as truncated: %q", out)
	}
}

// Control: A stream with usage chunk arriving after finish_reason chunk must complete with [DONE].
func TestChatCompletionsStreamUsageAfterFinishReasonStillCompletes(t *testing.T) {
	chunks := []string{
		`{"choices":[{"index":0,"delta":{"content":"hi"}}]}`,
		`{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`,
		`{"choices":[],"usage":{"prompt_tokens":5,"completion_tokens":2,"total_tokens":7}}`,
	}
	out := runMockStreamTest(t, chunks, "truncated-stream-auth-usage-after-finish")
	if !strings.Contains(out, "data: [DONE]") {
		t.Errorf("stream with usage after finish_reason was not terminated with [DONE]: %q", out)
	}
	if strings.Contains(out, "upstream stream closed before") {
		t.Errorf("stream with usage after finish_reason was wrongly flagged as truncated: %q", out)
	}
}

// Control: Legacy /v1/completions single-chunk stream with finish_reason completes with [DONE].
func TestCompletionsStreamSingleChunkWithFinishReasonStillCompletes(t *testing.T) {
	gin.SetMode(gin.TestMode)

	chunks := []string{
		`{"choices":[{"index":0,"delta":{"content":"single"},"finish_reason":"stop"}]}`,
	}
	executor := &mockStreamExecutor{chunks: chunks}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{ID: "truncated-legacy-auth-single-present", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("register auth: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "legacy-model-single-ok"}})
	defer registry.GetGlobalRegistry().UnregisterClient(auth.ID)

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIAPIHandler(base)
	router := gin.New()
	router.POST("/v1/completions", h.Completions)

	body := `{"model":"legacy-model-single-ok","prompt":"hi","stream":true}`
	request := httptest.NewRequest(http.MethodPost, "/v1/completions", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	out := recorder.Body.String()

	if !strings.Contains(out, "data: [DONE]") {
		t.Errorf("single chunk legacy completions stream was not terminated with [DONE]: %q", out)
	}
	if strings.Contains(out, "upstream stream closed before") {
		t.Errorf("single chunk legacy completions stream was wrongly flagged as truncated: %q", out)
	}
}
