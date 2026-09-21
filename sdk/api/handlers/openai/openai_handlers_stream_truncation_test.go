package openai

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

const truncatedStreamChatModel = "truncated-stream-chat-model"

// truncatedStreamExecutor emits well-formed chunks and then closes the channel
// cleanly, without any chunk carrying finish_reason. This is what a mid-stream
// upstream drop looks like to the handler: no error, just a short stream.
type truncatedStreamExecutor struct{ withFinishReason bool }

func (*truncatedStreamExecutor) Identifier() string { return "truncated-stream-executor" }

func (*truncatedStreamExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *truncatedStreamExecutor) ExecuteStream(_ context.Context, _ *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	chunks := make(chan coreexecutor.StreamChunk, 3)
	chunks <- coreexecutor.StreamChunk{Payload: []byte(`{"choices":[{"index":0,"delta":{"role":"assistant"}}]}`)}
	chunks <- coreexecutor.StreamChunk{Payload: []byte(`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":"do_thing","arguments":"{\"a\":1"}}]}}]}`)}
	if e.withFinishReason {
		chunks <- coreexecutor.StreamChunk{Payload: []byte(`{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`)}
	}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (*truncatedStreamExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (*truncatedStreamExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (*truncatedStreamExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func runTruncatedStreamTest(t *testing.T, withFinishReason bool, authID string) string {
	t.Helper()
	gin.SetMode(gin.TestMode)

	executor := &truncatedStreamExecutor{withFinishReason: withFinishReason}
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

// A stream that stops before any chunk carries finish_reason is truncated. Answering it
// with [DONE] tells the client it succeeded, and a client accumulating tool_call
// arguments across deltas is left holding unparseable JSON with no way to detect it.
func TestChatCompletionsStreamWithoutFinishReasonIsReportedAsError(t *testing.T) {
	out := runTruncatedStreamTest(t, false, "truncated-stream-auth-missing")
	if strings.Contains(out, "data: [DONE]") {
		t.Errorf("truncated stream was terminated with [DONE] as if it had succeeded: %q", out)
	}
	if !strings.Contains(out, "finish_reason") {
		t.Errorf("truncated stream did not surface an error naming the missing terminator: %q", out)
	}
}

// Control: a stream that does carry finish_reason must be unaffected.
func TestChatCompletionsStreamWithFinishReasonStillCompletes(t *testing.T) {
	out := runTruncatedStreamTest(t, true, "truncated-stream-auth-present")
	if !strings.Contains(out, "data: [DONE]") {
		t.Errorf("complete stream was not terminated with [DONE]: %q", out)
	}
	if strings.Contains(out, "upstream stream closed before") {
		t.Errorf("complete stream was wrongly flagged as truncated: %q", out)
	}
}
