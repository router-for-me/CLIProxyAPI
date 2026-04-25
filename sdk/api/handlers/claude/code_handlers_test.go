package claude

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

type bootstrapFailClaudeExecutor struct{}

func (e *bootstrapFailClaudeExecutor) Identifier() string { return "claude-test" }

func (e *bootstrapFailClaudeExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *bootstrapFailClaudeExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, &coreauth.Error{
		Code:       "bootstrap_failed",
		Message:    "unexpected EOF",
		Retryable:  false,
		HTTPStatus: http.StatusBadGateway,
	}
}

func (e *bootstrapFailClaudeExecutor) Refresh(ctx context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *bootstrapFailClaudeExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *bootstrapFailClaudeExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func TestForwardClaudeStreamTerminalErrorCompletesAnthropicLifecycle(t *testing.T) {
	gin.SetMode(gin.TestMode)
	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	h := NewClaudeCodeAPIHandler(base)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		t.Fatalf("expected gin writer to implement http.Flusher")
	}

	firstChunk := []byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_test\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"claude-sonnet\",\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":0,\"output_tokens\":0}}}\n\n")
	lifecycle := newClaudeStreamLifecycle()
	lifecycle.ObserveChunk(firstChunk)
	if _, err := recorder.Write(firstChunk); err != nil {
		t.Fatalf("write first chunk: %v", err)
	}

	data := make(chan []byte)
	close(data)

	errs := make(chan *interfaces.ErrorMessage, 1)
	errs <- &interfaces.ErrorMessage{
		StatusCode: http.StatusBadGateway,
		Error:      errors.New("upstream closed"),
	}
	close(errs)

	h.forwardClaudeStream(c, flusher, func(error) {}, lifecycle, data, errs)

	body := recorder.Body.String()
	if strings.Count(body, "event: message_start") != 1 {
		t.Fatalf("expected single message_start event, got body %q", body)
	}
	if !strings.Contains(body, "event: message_delta") {
		t.Fatalf("expected message_delta in terminal error flow, got %q", body)
	}
	if !strings.Contains(body, `"usage":{"input_tokens":0,"output_tokens":0}`) {
		t.Fatalf("expected usage tokens in terminal message_delta, got %q", body)
	}
	if !strings.Contains(body, `"stop_reason":"end_turn"`) {
		t.Fatalf("expected synthesized end_turn stop_reason, got %q", body)
	}
	if !strings.Contains(body, "event: error") {
		t.Fatalf("expected error event, got %q", body)
	}
	if !strings.Contains(body, "event: message_stop") {
		t.Fatalf("expected message_stop after terminal error, got %q", body)
	}
}

func TestClaudeMessagesStreamingBootstrapErrorReturnsAnthropicJSON(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &bootstrapFailClaudeExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	auth := &coreauth.Auth{
		ID:       "auth-claude-bootstrap",
		Provider: executor.Identifier(),
		Status:   coreauth.StatusActive,
	}
	if _, err := manager.Register(context.Background(), auth); err != nil {
		t.Fatalf("manager.Register(auth): %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	manager.RefreshSchedulerEntry(auth.ID)
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewClaudeCodeAPIHandler(base)

	router := gin.New()
	router.POST("/v1/messages", h.ClaudeMessages)

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"test-model","messages":[{"role":"user","content":"hello"}],"stream":true}`))
	req.Header.Set("Content-Type", "application/json")
	resp := httptest.NewRecorder()
	router.ServeHTTP(resp, req)

	if resp.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d, body = %q", resp.Code, http.StatusBadGateway, resp.Body.String())
	}
	body := resp.Body.String()
	if !strings.Contains(body, `"type":"error"`) {
		t.Fatalf("expected anthropic error type, got %q", body)
	}
	if !strings.Contains(body, `"error":{"type":"api_error","message":"`) {
		t.Fatalf("expected anthropic api_error body, got %q", body)
	}
	if !strings.Contains(body, "unexpected EOF") {
		t.Fatalf("expected anthropic api_error body, got %q", body)
	}
	if strings.Contains(body, `"code":"internal_server_error"`) {
		t.Fatalf("expected no openai-style error code field, got %q", body)
	}
}

func TestClaudeStreamLifecycleSynthesizesMessageStartWhenMissing(t *testing.T) {
	lifecycle := newClaudeStreamLifecycle()

	frames := lifecycle.BuildTerminalErrorFrames(claudeErrorResponse{
		Type: "error",
		Error: claudeErrorDetail{
			Type:    "api_error",
			Message: "boom",
		},
	})
	body := string(frames)
	if !strings.Contains(body, "event: message_start") {
		t.Fatalf("expected synthesized message_start, got %q", body)
	}
	if !strings.Contains(body, "event: message_delta") {
		t.Fatalf("expected synthesized message_delta, got %q", body)
	}
	if !strings.Contains(body, "event: message_stop") {
		t.Fatalf("expected synthesized message_stop, got %q", body)
	}
}
