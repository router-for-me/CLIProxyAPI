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

// peekStreamExecutor feeds a fake executor stream that closes the chunk channel
// immediately. All three initial streaming peek loops (chat, ViaResponses and
// legacy completions) then race a closed dataChan against any buffered pending
// error on errChan.
type peekStreamExecutor struct {
	// secret, when non-empty, is emitted as a chunk error so the producer
	// buffers it on errChan before closing dataChan.
	secret string
	// payload, when non-empty, is emitted as a valid content chunk so the
	// stream is a clean deterministic completion (forwarding emits [DONE]).
	payload string
}

func (*peekStreamExecutor) Identifier() string { return "peek-stream" }

func (*peekStreamExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *peekStreamExecutor) ExecuteStream(_ context.Context, _ *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	chunks := make(chan coreexecutor.StreamChunk, 2)
	if e.payload != "" {
		chunks <- coreexecutor.StreamChunk{Payload: []byte(e.payload)}
	}
	if e.secret != "" {
		chunks <- coreexecutor.StreamChunk{Err: errors.New("upstream failure: api_key=" + e.secret)}
	}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (*peekStreamExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (*peekStreamExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (*peekStreamExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

// sendPeekRequest drives the given handler method through a registered executor.
// endpoints restricts which OpenAI endpoints the registered model supports;
// a model that only advertises the responses endpoint routes chat through the
// ViaResponses streaming peek.
func sendPeekRequest(t *testing.T, route, body string, executor *peekStreamExecutor, endpoints []string) *httptest.ResponseRecorder {
	t.Helper()
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	authID := "peek-stream-auth"
	auth := &coreauth.Auth{ID: authID, Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(authID, executor.Identifier(), []*registry.ModelInfo{{ID: "peek-stream-model", SupportedEndpoints: endpoints}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIAPIHandler(base)
	router := gin.New()
	router.POST("/v1/chat/completions", h.ChatCompletions)
	router.POST("/v1/completions", h.Completions)

	request := httptest.NewRequest(http.MethodPost, route, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder
}

// TestStreamingPeekConsumesBufferedPendingError covers chat completions,
// ViaResponses and legacy completions: when the peek loop sees the data
// channel close while a buffered pending error is already queued on errChan,
// the handler MUST return an error status (never 200) and MUST NOT commit
// `data: [DONE]`, and MUST redact credential material from the upstream error.
func TestStreamingPeekConsumesBufferedPendingError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const secret = "upstream-secret-fx-8849"
	cases := []struct {
		name      string
		route     string
		body      string
		endpoints []string
	}{
		{
			name:  "chat",
			route: "/v1/chat/completions",
			body:  `{"model":"peek-stream-model","stream":true,"messages":[{"role":"user","content":"hi"}]}`,
		},
		{
			// A model that only advertises the responses endpoint routes chat
			// through the ViaResponses streaming peek.
			name:      "via-responses",
			route:     "/v1/chat/completions",
			body:      `{"model":"peek-stream-model","stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			endpoints: []string{openAIResponsesEndpoint},
		},
		{
			name:  "legacy",
			route: "/v1/completions",
			body:  `{"model":"peek-stream-model","stream":true,"prompt":"hi"}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := sendPeekRequest(t, tc.route, tc.body, &peekStreamExecutor{secret: secret}, tc.endpoints)
			body := recorder.Body.String()
			if recorder.Code == http.StatusOK {
				t.Fatalf("handler returned 200 despite buffered pending error: %q", body)
			}
			if recorder.Code < http.StatusBadRequest {
				t.Fatalf("status = %d, want error status; body=%q", recorder.Code, body)
			}
			if strings.Contains(body, "data: [DONE]") {
				t.Fatalf("stream emitted [DONE] despite pending error: %q", body)
			}
			if strings.Contains(body, secret) {
				t.Fatalf("stream body leaked upstream secret: %q", body)
			}
			if !strings.Contains(body, "[REDACTED]") {
				t.Fatalf("stream body did not redact upstream error: %q", body)
			}
		})
	}
}

// TestStreamingPeekCleanCloseStillEmitsDone is the control: a clean data-channel
// close with no pending error must still emit SSE [DONE] with a success status.
func TestStreamingPeekCleanCloseStillEmitsDone(t *testing.T) {
	gin.SetMode(gin.TestMode)
	chatPayload := `data: {"id":"cmpl-1","object":"chat.completion.chunk","created":1,"model":"peek-stream-model","choices":[{"index":0,"delta":{"content":"hi"},"finish_reason":"stop"}]}`
	responsesPayload := `data: {"type":"response.output_text.delta","delta":"hi"}`
	cases := []struct {
		name      string
		route     string
		body      string
		payload   string
		endpoints []string
	}{
		{
			name:    "chat",
			route:   "/v1/chat/completions",
			body:    `{"model":"peek-stream-model","stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			payload: chatPayload,
		},
		{
			name:      "via-responses",
			route:     "/v1/chat/completions",
			body:      `{"model":"peek-stream-model","stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			payload:   responsesPayload,
			endpoints: []string{openAIResponsesEndpoint},
		},
		{
			name:    "legacy",
			route:   "/v1/completions",
			body:    `{"model":"peek-stream-model","stream":true,"prompt":"hi"}`,
			payload: chatPayload,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A single valid content chunk then a clean close: forwarding must
			// emit [DONE] with no error.
			recorder := sendPeekRequest(t, tc.route, tc.body, &peekStreamExecutor{payload: tc.payload}, tc.endpoints)
			body := recorder.Body.String()
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body=%q", recorder.Code, body)
			}
			if !strings.Contains(body, "data: [DONE]") {
				t.Fatalf("clean close did not emit [DONE]: %q", body)
			}
		})
	}
}
