package openai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
)

const (
	prematureResponsesStreamModel       = "premature-responses-stream-model"
	initialFailureResponsesModel        = "initial-failure-responses-stream-model"
	emptyResponsesStreamModel           = "empty-responses-stream-model"
	incompleteFirstFrameResponsesModel  = "incomplete-first-frame-responses-model"
	dataOnlyFirstFrameResponsesModel    = "data-only-first-frame-responses-model"
	dataOnlyCleanCloseResponsesModel    = "data-only-clean-close-responses-model"
	sensitiveInitialErrorResponsesModel = "sensitive-initial-error-responses-model"
	directInitialErrorResponsesModel    = "direct-initial-error-responses-model"
	crossChunkMultilineResponsesModel   = "cross-chunk-multiline-responses-model"
	validThenMalformedResponsesModel    = "valid-then-malformed-responses-model"
)

type prematureResponsesStreamExecutor struct{}

func (*prematureResponsesStreamExecutor) Identifier() string { return "premature-responses-stream" }

func (*prematureResponsesStreamExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (*prematureResponsesStreamExecutor) ExecuteStream(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	if req.Model == directInitialErrorResponsesModel {
		return nil, &coreexecutor.RequestTerminatedError{
			HTTPStatus: http.StatusTooManyRequests,
			Header:     http.Header{"Retry-After": []string{"17"}, "X-Plugin-Response": []string{"true"}},
			Body:       []byte(`{"error":{"message":"plugin direct response"}}`),
		}
	}
	chunks := make(chan coreexecutor.StreamChunk, 2)
	if req.Model == validThenMalformedResponsesModel {
		chunks <- coreexecutor.StreamChunk{Payload: []byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n" +
			"event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\"\n\n")}
		close(chunks)
		return &coreexecutor.StreamResult{Chunks: chunks}, nil
	}
	if req.Model == crossChunkMultilineResponsesModel {
		chunks <- coreexecutor.StreamChunk{Payload: []byte("event: response.completed\ndata: {\"type\":\"response.completed\",")}
		chunks <- coreexecutor.StreamChunk{Payload: []byte("data: \"response\":{\"id\":\"resp-1\",\"status\":\"completed\"}}\n\n")}
		close(chunks)
		return &coreexecutor.StreamResult{Chunks: chunks}, nil
	}
	if req.Model == sensitiveInitialErrorResponsesModel {
		chunks <- coreexecutor.StreamChunk{Err: errors.New(`{"error":{"type":"server_error","code":"upstream_failed","message":"initial upstream failure: {\"api_key\":\"initial-message-secret\"}"},"debug":{"token":"initial-debug-secret","trace":"` + strings.Repeat("x", 8192) + `"}}`)}
		close(chunks)
		return &coreexecutor.StreamResult{Chunks: chunks}, nil
	}
	if req.Model == dataOnlyFirstFrameResponsesModel || req.Model == dataOnlyCleanCloseResponsesModel {
		chunks <- coreexecutor.StreamChunk{Payload: []byte(`data: {"type":"response.output_text.delta","delta":"partial"}`)}
		if req.Model == dataOnlyFirstFrameResponsesModel {
			chunks <- coreexecutor.StreamChunk{Err: errors.New("upstream failed after data-only frame")}
		}
		close(chunks)
		return &coreexecutor.StreamResult{Chunks: chunks}, nil
	}
	if req.Model == incompleteFirstFrameResponsesModel {
		chunks <- coreexecutor.StreamChunk{Payload: []byte("event: response.created")}
		chunks <- coreexecutor.StreamChunk{Err: errors.New("upstream failed before first complete frame")}
		close(chunks)
		return &coreexecutor.StreamResult{Chunks: chunks}, nil
	}
	if req.Model == emptyResponsesStreamModel {
		close(chunks)
		return &coreexecutor.StreamResult{Chunks: chunks}, nil
	}
	if req.Model == initialFailureResponsesModel {
		chunks <- coreexecutor.StreamChunk{Err: errors.New("upstream failed before first payload")}
		close(chunks)
		return &coreexecutor.StreamResult{Chunks: chunks}, nil
	}
	chunks <- coreexecutor.StreamChunk{Payload: []byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n")}
	chunks <- coreexecutor.StreamChunk{Err: errors.New("unexpected EOF")}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (*prematureResponsesStreamExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (*prematureResponsesStreamExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (*prematureResponsesStreamExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func TestResponsesHandlerEmitsFailureWhenExecutorStopsAfterPartialOutput(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &prematureResponsesStreamExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{ID: "premature-responses-stream-auth", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: prematureResponsesStreamModel}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{RequestLog: true}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.POST("/v1/responses", h.Responses)

	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"premature-responses-stream-model","input":"hi","stream":true}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "Codex Desktop/26.803.41515")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 after stream start; body=%s", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "response.output_text.delta") || !strings.Contains(body, "event: response.failed") {
		t.Fatalf("handler did not preserve partial output and terminal failure: %q", body)
	}
	if !strings.Contains(body, "unexpected EOF") {
		t.Fatalf("handler terminal failure lost executor error: %q", body)
	}
}

func TestSanitizeResponsesStreamErrorMessageNormalizesSuccessStatus(t *testing.T) {
	got := sanitizeResponsesStreamErrorMessage(&interfaces.ErrorMessage{StatusCode: http.StatusOK, Error: errors.New("upstream failed")})
	if got == nil || got.StatusCode != http.StatusInternalServerError {
		t.Fatalf("sanitized status = %#v, want %d", got, http.StatusInternalServerError)
	}
}

func TestResponsesHandlerCommitsValidFrameBeforeMalformedFrameInSameChunk(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &prematureResponsesStreamExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{ID: "valid-then-malformed-responses-auth", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: validThenMalformedResponsesModel}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.POST("/v1/responses", h.Responses)
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"valid-then-malformed-responses-model","input":"hi","stream":true}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "Codex Desktop/26.803.41515")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "response.output_text.delta") || !strings.Contains(recorder.Body.String(), "event: response.failed") {
		t.Fatalf("valid then malformed response status=%d body=%q", recorder.Code, recorder.Body.String())
	}
}

func TestResponsesHandlerAcceptsMultilineDataAcrossExecutorChunks(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &prematureResponsesStreamExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{ID: "cross-chunk-multiline-responses-auth", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: crossChunkMultilineResponsesModel}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.POST("/v1/responses", h.Responses)
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"cross-chunk-multiline-responses-model","input":"hi","stream":true}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "event: response.completed") {
		t.Fatalf("cross-chunk multiline response status=%d body=%q", recorder.Code, recorder.Body.String())
	}
}

func TestResponsesHandlerPreservesDirectResponseBeforeFirstFrame(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &prematureResponsesStreamExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{ID: "direct-initial-error-responses-auth", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: directInitialErrorResponsesModel}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.POST("/v1/responses", h.Responses)
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"direct-initial-error-responses-model","input":"hi","stream":true}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusTooManyRequests || recorder.Header().Get("Retry-After") != "17" || recorder.Header().Get("X-Plugin-Response") != "true" {
		t.Fatalf("direct response status=%d headers=%v body=%q", recorder.Code, recorder.Header(), recorder.Body.String())
	}
	if recorder.Body.String() != `{"error":{"message":"plugin direct response"}}` {
		t.Fatalf("direct response body = %q", recorder.Body.String())
	}
}

func TestResponsesHandlerSanitizesErrorBeforeFirstFrame(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &prematureResponsesStreamExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{ID: "sensitive-initial-error-responses-auth", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: sensitiveInitialErrorResponsesModel}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{RequestLog: true}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.POST("/v1/responses", h.Responses)

	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"sensitive-initial-error-responses-model","input":"hi","stream":true}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	body := recorder.Body.String()
	if recorder.Code == http.StatusOK || !strings.Contains(body, "upstream_failed") || !strings.Contains(body, "initial upstream failure") {
		t.Fatalf("initial error response = status %d body %q", recorder.Code, body)
	}
	for _, secret := range []string{"initial-message-secret", "initial-debug-secret"} {
		if strings.Contains(body, secret) {
			t.Fatalf("initial error leaked %q: %q", secret, body)
		}
	}
	if len(body) > 4096 {
		t.Fatalf("initial error response remained unbounded: len=%d", len(body))
	}
}

func TestResponsesHandlerFlushesDataOnlyFrameBeforeStreamingError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &prematureResponsesStreamExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{ID: "data-only-first-frame-responses-auth", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: dataOnlyFirstFrameResponsesModel}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.POST("/v1/responses", h.Responses)

	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"data-only-first-frame-responses-model","input":"hi","stream":true}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "Codex Desktop/26.803.41515")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 after complete data frame; body=%q", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "response.output_text.delta") || !strings.Contains(body, "event: response.failed") {
		t.Fatalf("data-only frame or terminal failure was lost: %q", body)
	}
}

func TestResponsesHandlerEmitsFailureWhenDataOnlyStreamClosesCleanly(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &prematureResponsesStreamExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{ID: "data-only-clean-close-responses-auth", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: dataOnlyCleanCloseResponsesModel}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.POST("/v1/responses", h.Responses)

	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"data-only-clean-close-responses-model","input":"hi","stream":true}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "Codex Desktop/26.803.41515")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 after complete data frame; body=%q", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "response.output_text.delta") || !strings.Contains(body, "event: response.failed") {
		t.Fatalf("clean close did not retain data and emit terminal failure: %q", body)
	}
	if strings.Contains(body, "event: response.completed") {
		t.Fatalf("clean close synthesized completion: %q", body)
	}
}

func TestResponsesHandlerDoesNotCommitHeadersForIncompleteFirstFrame(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &prematureResponsesStreamExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{ID: "incomplete-first-frame-responses-auth", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: incompleteFirstFrameResponsesModel}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.POST("/v1/responses", h.Responses)

	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"incomplete-first-frame-responses-model","input":"hi","stream":true}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code == http.StatusOK {
		t.Fatalf("incomplete first SSE frame committed HTTP 200: %q", recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "upstream failed before first complete frame") {
		t.Fatalf("initial frame error was lost: status=%d body=%q", recorder.Code, recorder.Body.String())
	}
}

func TestResponsesHandlerRejectsStreamClosedBeforeFirstPayload(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &prematureResponsesStreamExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{ID: "empty-responses-stream-auth", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: emptyResponsesStreamModel}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.POST("/v1/responses", h.Responses)

	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"empty-responses-stream-model","input":"hi","stream":true}`))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code == http.StatusOK {
		t.Fatalf("empty upstream stream returned HTTP 200: %q", recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "closed before first payload") {
		t.Fatalf("empty upstream stream error is unclear: status=%d body=%q", recorder.Code, recorder.Body.String())
	}
}

func TestResponsesHandlerDoesNotLoseErrorBeforeFirstPayload(t *testing.T) {
	gin.SetMode(gin.TestMode)

	for i := 0; i < 100; i++ {
		executor := &prematureResponsesStreamExecutor{}
		manager := coreauth.NewManager(nil, nil, nil)
		manager.RegisterExecutor(executor)
		auth := &coreauth.Auth{ID: fmt.Sprintf("initial-failure-responses-stream-auth-%d", i), Provider: executor.Identifier(), Status: coreauth.StatusActive}
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register auth %d: %v", i, errRegister)
		}
		registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: initialFailureResponsesModel}})

		base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
		h := NewOpenAIResponsesAPIHandler(base)
		router := gin.New()
		router.POST("/v1/responses", h.Responses)

		request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"initial-failure-responses-stream-model","input":"hi","stream":true}`))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)

		if recorder.Code == http.StatusOK {
			t.Fatalf("request %d lost the buffered initial error and returned HTTP 200: %q", i, recorder.Body.String())
		}
		if !strings.Contains(recorder.Body.String(), "upstream failed before first payload") {
			t.Fatalf("request %d lost the initial upstream error: status=%d body=%q", i, recorder.Code, recorder.Body.String())
		}
	}
}

// TestForwardResponsesStreamExposesTerminalErrors pins the SSE side: once a
// Responses stream has started, every terminal upstream error reaches the client.
func TestForwardResponsesStreamExposesTerminalErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name        string
		status      int
		message     string
		wantExposed bool
	}{
		{
			name:        "bad request",
			status:      http.StatusBadRequest,
			message:     `{"error":{"type":"invalid_request","code":"cyber_policy","message":"blocked"}}`,
			wantExposed: true,
		},
		{
			// Observed in production: the same cyber_policy rejection arrives with 502
			// when it is surfaced through the websocket disconnect channel.
			name:        "cyber policy behind bad gateway status",
			status:      http.StatusBadGateway,
			message:     `{"error":{"type":"invalid_request","code":"cyber_policy","message":"This content was flagged for possible cybersecurity risk.","param":null}}`,
			wantExposed: true,
		},
		{
			name:        "context length exceeded behind bad gateway status",
			status:      http.StatusBadGateway,
			message:     `{"error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"Your input exceeds the context window."}}`,
			wantExposed: true,
		},
		{name: "conflict", status: http.StatusConflict, message: "conflict", wantExposed: true},
		{name: "message too big", status: http.StatusRequestEntityTooLarge, message: "too large", wantExposed: true},
		{name: "unprocessable entity", status: http.StatusUnprocessableEntity, message: "invalid input", wantExposed: true},
		{name: "authentication", status: http.StatusUnauthorized, message: "invalid credential", wantExposed: true},
		{name: "payment required", status: http.StatusPaymentRequired, message: "insufficient credits", wantExposed: true},
		{name: "quota error", status: http.StatusTooManyRequests, message: "usage limit reached", wantExposed: true},
		{name: "request timeout", status: http.StatusRequestTimeout, message: "upstream timeout", wantExposed: true},
		{name: "transport error", status: http.StatusInternalServerError, message: "unexpected EOF", wantExposed: true},
		{name: "upstream websocket drop", status: http.StatusInternalServerError,
			message: `{"error":{"message":"websocket: close 1006 (abnormal closure): unexpected EOF","type":"server_error","code":"internal_server_error"}}`, wantExposed: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
			h := NewOpenAIResponsesAPIHandler(base)

			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

			flusher, ok := c.Writer.(http.Flusher)
			if !ok {
				t.Fatal("expected gin writer to implement http.Flusher")
			}

			data := make(chan []byte)
			errs := make(chan *interfaces.ErrorMessage, 1)
			errs <- &interfaces.ErrorMessage{StatusCode: tc.status, Error: errors.New(tc.message)}
			close(errs)

			h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)
			body := recorder.Body.String()
			exposed := strings.Contains(body, `"type":"error"`)
			if exposed != tc.wantExposed {
				t.Fatalf("error exposed = %t, want %t: %q", exposed, tc.wantExposed, body)
			}
			if exposed && strings.Contains(body, `"error":{`) {
				t.Fatalf("expected streaming error chunk, got HTTP error body: %q", body)
			}
		})
	}
}

func TestForwardResponsesStreamUsesResponseFailedForCodex(t *testing.T) {
	gin.SetMode(gin.TestMode)

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	h := NewOpenAIResponsesAPIHandler(base)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("User-Agent", "Codex Desktop/26.803.41515")

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		t.Fatal("expected gin writer to implement http.Flusher")
	}

	data := make(chan []byte)
	errs := make(chan *interfaces.ErrorMessage, 1)
	errs <- &interfaces.ErrorMessage{
		StatusCode: http.StatusBadRequest,
		Error:      errors.New(`{"error":{"type":"invalid_request","code":"cyber_policy","message":"blocked"}}`),
	}
	close(errs)

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, nil)
	body := recorder.Body.String()
	if !strings.Contains(body, "event: response.failed") {
		t.Fatalf("missing response.failed event: %q", body)
	}
	if strings.Contains(body, "event: error") {
		t.Fatalf("unexpected legacy error event for Codex: %q", body)
	}
	if !strings.Contains(body, `"type":"invalid_request"`) || !strings.Contains(body, `"code":"cyber_policy"`) {
		t.Fatalf("missing nested Codex error detail: %q", body)
	}
}

func TestForwardResponsesStreamExposesTransportErrorAfterOutputForCodex(t *testing.T) {
	gin.SetMode(gin.TestMode)

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{RequestLog: true}, nil)
	h := NewOpenAIResponsesAPIHandler(base)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("User-Agent", "Codex Desktop/26.803.41515")

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		t.Fatal("expected gin writer to implement http.Flusher")
	}

	framer := &responsesSSEFramer{}
	framer.WriteChunk(c.Writer, []byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"))
	data := make(chan []byte)
	errs := make(chan *interfaces.ErrorMessage, 1)
	errs <- &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: errors.New("unexpected EOF")}
	close(errs)

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, framer)
	body := recorder.Body.String()
	if !strings.Contains(body, "event: response.failed") {
		t.Fatalf("transport failure ended without response.failed: %q", body)
	}
	if !strings.Contains(body, "unexpected EOF") {
		t.Fatalf("response.failed lost the upstream error: %q", body)
	}

	loggedValue, ok := c.Get("API_RESPONSE_ERROR")
	if !ok {
		t.Fatal("request log did not retain the stream error")
	}
	loggedErrors, ok := loggedValue.([]*interfaces.ErrorMessage)
	if !ok || len(loggedErrors) != 1 || loggedErrors[0] == nil || loggedErrors[0].Error == nil {
		t.Fatalf("unexpected request-log errors: %#v", loggedValue)
	}
	diagnostic := loggedErrors[0].Error.Error()
	if !strings.Contains(diagnostic, "response.output_text.delta") || !strings.Contains(diagnostic, "unexpected EOF") {
		t.Fatalf("request-log diagnostic lacks last event or upstream error: %q", diagnostic)
	}
}

func TestForwardResponsesStreamSanitizesDiagnosticErrorDetails(t *testing.T) {
	gin.SetMode(gin.TestMode)

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{RequestLog: true}, nil)
	h := NewOpenAIResponsesAPIHandler(base)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		t.Fatal("expected gin writer to implement http.Flusher")
	}

	debugSecret := "super-secret-provider-debug-value"
	messageSecret := "super-secret-provider-message-value"
	rawError := `{"error":{"type":"server_error","code":"upstream_failed","message":"upstream failed: {\"api_key\":\"` + messageSecret + `\"}"},"debug":{"api_key":"` + debugSecret + `","trace":"` + strings.Repeat("x", 8192) + `"}}`
	framer := &responsesSSEFramer{}
	framer.WriteChunk(c.Writer, []byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"))
	data := make(chan []byte)
	errs := make(chan *interfaces.ErrorMessage, 1)
	errs <- &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: errors.New(rawError)}
	close(errs)

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, framer)
	body := recorder.Body.String()
	if !strings.Contains(body, "upstream failed") || !strings.Contains(body, "upstream_failed") {
		t.Fatalf("client error lost safe structured fields: %q", body)
	}
	if strings.Contains(body, debugSecret) || strings.Contains(body, messageSecret) {
		t.Fatalf("client error leaked provider secret: %q", body)
	}

	loggedValue, ok := c.Get("API_RESPONSE_ERROR")
	if !ok {
		t.Fatal("request log did not retain the sanitized stream error")
	}
	loggedErrors, ok := loggedValue.([]*interfaces.ErrorMessage)
	if !ok || len(loggedErrors) != 1 || loggedErrors[0] == nil || loggedErrors[0].Error == nil {
		t.Fatalf("unexpected request-log errors: %#v", loggedValue)
	}
	diagnostic := loggedErrors[0].Error.Error()
	if strings.Contains(diagnostic, debugSecret) || strings.Contains(diagnostic, messageSecret) || len(diagnostic) > 4096 {
		t.Fatalf("request-log diagnostic leaked or retained an unbounded upstream body: len=%d diagnostic=%q", len(diagnostic), diagnostic)
	}
	if !strings.Contains(diagnostic, "upstream failed") {
		t.Fatalf("sanitized request-log diagnostic lost upstream message: %q", diagnostic)
	}
}

func TestForwardResponsesStreamPreservesNestedResponseError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{RequestLog: true}, nil)
	h := NewOpenAIResponsesAPIHandler(base)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("User-Agent", "Codex Desktop/26.803.41515")
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		t.Fatal("expected gin writer to implement http.Flusher")
	}

	framer := &responsesSSEFramer{}
	framer.WriteChunk(c.Writer, []byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"))
	data := make(chan []byte)
	errs := make(chan *interfaces.ErrorMessage, 1)
	errs <- &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: errors.New(`{"type":"response.failed","response":{"error":{"type":"server_error","code":"upstream_failed","message":"nested response failure","param":"input"}}}`)}
	close(errs)

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, framer)
	body := recorder.Body.String()
	for _, want := range []string{"nested response failure", "upstream_failed", "server_error"} {
		if !strings.Contains(body, want) {
			t.Fatalf("response.failed lost nested response error field %q: %q", want, body)
		}
	}
}

func TestForwardResponsesStreamSanitizesLastEventDiagnostic(t *testing.T) {
	gin.SetMode(gin.TestMode)

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{RequestLog: true}, nil)
	h := NewOpenAIResponsesAPIHandler(base)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		t.Fatal("expected gin writer to implement http.Flusher")
	}

	eventSecret := "event-secret-value"
	eventName := "custom-event-Bearer " + eventSecret + strings.Repeat("x", 1024)
	framer := &responsesSSEFramer{}
	framer.WriteChunk(c.Writer, []byte("event: "+eventName+"\ndata: {\"message\":\"partial\"}\n\n"))
	data := make(chan []byte)
	errs := make(chan *interfaces.ErrorMessage, 1)
	errs <- &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: errors.New("unexpected EOF")}
	close(errs)

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, framer)
	loggedValue, ok := c.Get("API_RESPONSE_ERROR")
	if !ok {
		t.Fatal("request log did not retain the stream error")
	}
	loggedErrors, ok := loggedValue.([]*interfaces.ErrorMessage)
	if !ok || len(loggedErrors) != 1 || loggedErrors[0] == nil || loggedErrors[0].Error == nil {
		t.Fatalf("unexpected request-log errors: %#v", loggedValue)
	}
	diagnostic := loggedErrors[0].Error.Error()
	if strings.Contains(diagnostic, eventSecret) || len(diagnostic) > 1024 {
		t.Fatalf("last-event diagnostic leaked or remained unbounded: len=%d diagnostic=%q", len(diagnostic), diagnostic)
	}
}

func TestForwardResponsesStreamSanitizesPayloadErrorsAndStopsAtFailure(t *testing.T) {
	for _, tc := range []struct {
		name  string
		frame string
	}{
		{
			name:  "event error with payload type",
			frame: "event: error\ndata: {\"type\":\"provider.error\",\"error\":{\"code\":\"failed\",\"message\":\"token=payload-secret\"}}\n\n",
		},
		{
			name:  "typed nested error",
			frame: "data: {\"type\":\"provider.error\",\"error\":{\"code\":\"failed\",\"message\":\"token=payload-secret\"}}\n\n",
		},
		{
			name:  "top level error fields",
			frame: "data: {\"code\":\"failed\",\"message\":\"token=payload-secret\"}\n\n",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{RequestLog: true}, nil)
			h := NewOpenAIResponsesAPIHandler(base)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			c.Request.Header.Set("User-Agent", "Codex Desktop/26.803.41515")
			flusher, ok := c.Writer.(http.Flusher)
			if !ok {
				t.Fatal("expected gin writer to implement http.Flusher")
			}

			data := make(chan []byte, 1)
			data <- []byte(tc.frame + "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\"}}\n\n")
			close(data)
			errs := make(chan *interfaces.ErrorMessage)
			close(errs)
			var canceled error

			h.forwardResponsesStream(c, flusher, func(err error) { canceled = err }, data, errs, &responsesSSEFramer{})
			body := recorder.Body.String()
			if canceled == nil {
				t.Fatalf("payload error canceled with nil: %q", body)
			}
			if strings.Contains(body, "payload-secret") || strings.Contains(body, "event: response.completed") {
				t.Fatalf("payload error leaked or accepted later completion: %q", body)
			}
			if strings.Count(body, "event: response.failed") != 1 || !strings.Contains(body, "[REDACTED]") {
				t.Fatalf("payload error was not converted to one sanitized response.failed: %q", body)
			}
		})
	}
}

func TestForwardResponsesStreamReportsDataOnlyErrorFlushedAtEOF(t *testing.T) {
	gin.SetMode(gin.TestMode)
	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{RequestLog: true}, nil)
	h := NewOpenAIResponsesAPIHandler(base)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("User-Agent", "Codex Desktop/26.803.41515")
	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		t.Fatal("expected gin writer to implement http.Flusher")
	}

	data := make(chan []byte, 1)
	data <- []byte(`data: {"type":"error","error":{"message":"failed at EOF"}}`)
	close(data)
	errs := make(chan *interfaces.ErrorMessage)
	close(errs)
	var canceled error
	h.forwardResponsesStream(c, flusher, func(err error) { canceled = err }, data, errs, &responsesSSEFramer{})

	if canceled == nil || !strings.Contains(canceled.Error(), "failed at EOF") {
		t.Fatalf("EOF error cancel = %v, body=%q", canceled, recorder.Body.String())
	}
	if strings.Count(recorder.Body.String(), "event: response.failed") != 1 {
		t.Fatalf("EOF error terminal output = %q", recorder.Body.String())
	}
	if _, okLog := c.Get("API_RESPONSE_ERROR"); !okLog {
		t.Fatal("EOF error was not retained in request diagnostics")
	}
}

func TestForwardResponsesStreamDoesNotAppendFailureAfterTerminalEvent(t *testing.T) {
	gin.SetMode(gin.TestMode)

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{RequestLog: true}, nil)
	h := NewOpenAIResponsesAPIHandler(base)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("User-Agent", "Codex Desktop/26.803.41515")

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		t.Fatal("expected gin writer to implement http.Flusher")
	}

	framer := &responsesSSEFramer{}
	framer.WriteChunk(c.Writer, []byte("event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"status\":\"completed\"}}\n\n"))
	data := make(chan []byte)
	errs := make(chan *interfaces.ErrorMessage, 1)
	errs <- &interfaces.ErrorMessage{StatusCode: http.StatusBadGateway, Error: errors.New("unexpected EOF after completion")}
	close(errs)

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, framer)
	body := recorder.Body.String()
	if strings.Contains(body, "event: response.failed") || strings.Contains(body, "event: error") {
		t.Fatalf("stream appended a second terminal event after response.completed: %q", body)
	}

	loggedValue, ok := c.Get("API_RESPONSE_ERROR")
	if !ok {
		t.Fatal("request log did not retain the post-terminal upstream error")
	}
	loggedErrors, ok := loggedValue.([]*interfaces.ErrorMessage)
	if !ok || len(loggedErrors) != 1 || loggedErrors[0] == nil || loggedErrors[0].Error == nil {
		t.Fatalf("unexpected request-log errors: %#v", loggedValue)
	}
	diagnostic := loggedErrors[0].Error.Error()
	if !strings.Contains(diagnostic, "response.completed") || !strings.Contains(diagnostic, "unexpected EOF after completion") {
		t.Fatalf("request-log diagnostic lacks terminal event or upstream error: %q", diagnostic)
	}
}

func TestForwardResponsesStreamFailsWhenUpstreamClosesWithoutTerminalEvent(t *testing.T) {
	gin.SetMode(gin.TestMode)

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	h := NewOpenAIResponsesAPIHandler(base)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	c.Request.Header.Set("User-Agent", "Codex Desktop/26.803.41515")

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		t.Fatal("expected gin writer to implement http.Flusher")
	}

	framer := &responsesSSEFramer{}
	framer.WriteChunk(c.Writer, []byte("event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"partial\"}\n\n"))
	data := make(chan []byte)
	close(data)
	errs := make(chan *interfaces.ErrorMessage)

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, framer)
	body := recorder.Body.String()
	if !strings.Contains(body, "event: response.failed") {
		t.Fatalf("unterminated stream ended without response.failed: %q", body)
	}
	if !strings.Contains(body, "closed before a terminal event") {
		t.Fatalf("response.failed does not explain the premature close: %q", body)
	}
}
