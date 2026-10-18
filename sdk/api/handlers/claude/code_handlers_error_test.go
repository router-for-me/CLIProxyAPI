package claude

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/tidwall/gjson"
)

func TestClaudeErrorTypeFromStatus(t *testing.T) {
	for _, tt := range []struct {
		status int
		want   string
	}{
		{http.StatusBadRequest, "invalid_request_error"},
		{http.StatusConflict, "conflict_error"},
		{http.StatusRequestTimeout, "timeout_error"},
		{http.StatusTooManyRequests, "rate_limit_error"},
		{http.StatusInternalServerError, "api_error"},
		{http.StatusGatewayTimeout, "timeout_error"},
	} {
		t.Run(http.StatusText(tt.status), func(t *testing.T) {
			if got := claudeErrorTypeFromStatus(tt.status); got != tt.want {
				t.Fatalf("error.type = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestClaudeIncompleteStreamError(t *testing.T) {
	const message = "stream error: stream disconnected before completion: stream closed before response.completed"
	for _, streaming := range []bool{false, true} {
		name := "JSON"
		if streaming {
			name = "committed SSE"
		}
		t.Run(name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			handler := NewClaudeCodeAPIHandler(&handlers.BaseAPIHandler{})
			msg := &interfaces.ErrorMessage{
				StatusCode: http.StatusRequestTimeout,
				Error:      errors.New(message),
			}
			wantStatus := http.StatusRequestTimeout
			if streaming {
				c.Header("Content-Type", "text/event-stream")
				_, _ = c.Writer.Write([]byte("event: message_start\ndata: {}\n\n"))
				c.Writer.Flush()
				errs := make(chan *interfaces.ErrorMessage, 1)
				errs <- msg
				close(errs)
				handler.forwardClaudeStream(c, c.Writer, func(err error) {
					if err != msg.Error {
						t.Errorf("cancel error = %v, want %v", err, msg.Error)
					}
				}, nil, errs)
				wantStatus = http.StatusOK
			} else {
				handler.WriteErrorResponse(c, msg)
			}
			if recorder.Code != wantStatus {
				t.Fatalf("status = %d, want %d", recorder.Code, wantStatus)
			}
			body := recorder.Body.String()
			if streaming {
				const prefix = "event: error\ndata: "
				if strings.Count(body, prefix) != 1 {
					t.Fatalf("expected one terminal error event; body=%s", body)
				}
				_, body, _ = strings.Cut(body, prefix)
				body = strings.TrimSpace(body)
			}
			if got := gjson.Get(body, "type").String(); got != "error" {
				t.Errorf("type = %q, want error", got)
			}
			if got := gjson.Get(body, "error.type").String(); got != "timeout_error" {
				t.Errorf("error.type = %q, want timeout_error", got)
			}
			if got := gjson.Get(body, "error.message").String(); got != message {
				t.Errorf("error.message = %q, want %q", got, message)
			}
		})
	}
}

func TestClaudeErrorExtractsOpenAIStyleUpstreamJSON(t *testing.T) {
	handler := &ClaudeCodeAPIHandler{}
	msg := &interfaces.ErrorMessage{
		StatusCode: http.StatusBadRequest,
		Error:      errors.New(`{"error":{"message":"Your input exceeds the context window of this model. Please adjust your input and try again.","type":"invalid_request_error","code":"context_too_large"}}`),
	}

	got := handler.toClaudeError(msg)

	if got.Type != "error" {
		t.Fatalf("type = %q, want error", got.Type)
	}
	if got.Error.Type != "invalid_request_error" {
		t.Fatalf("error.type = %q, want invalid_request_error", got.Error.Type)
	}
	if got.Error.Message != "Your input exceeds the context window of this model. Please adjust your input and try again." {
		t.Fatalf("error.message = %q", got.Error.Message)
	}
}

func TestClaudeErrorExtractsClaudeStyleUpstreamJSON(t *testing.T) {
	handler := &ClaudeCodeAPIHandler{}
	msg := &interfaces.ErrorMessage{
		StatusCode: http.StatusTooManyRequests,
		Error:      errors.New(`{"type":"error","error":{"type":"rate_limit_error","message":"This request would exceed your account's rate limit. Please try again later."},"request_id":"req_123"}`),
	}

	got := handler.toClaudeError(msg)

	if got.Error.Type != "rate_limit_error" {
		t.Fatalf("error.type = %q, want rate_limit_error", got.Error.Type)
	}
	if got.Error.Message != "This request would exceed your account's rate limit. Please try again later." {
		t.Fatalf("error.message = %q", got.Error.Message)
	}
}

type responseBodyOnlyClaudeError struct {
	body []byte
}

func (e responseBodyOnlyClaudeError) Error() string        { return "wrapped upstream error" }
func (e responseBodyOnlyClaudeError) ResponseBody() []byte { return e.body }

func TestClaudeErrorMarksWrappedMissingThreadForClientReplay(t *testing.T) {
	handler := &ClaudeCodeAPIHandler{}
	msg := &interfaces.ErrorMessage{
		StatusCode: http.StatusNotFound,
		Error:      responseBodyOnlyClaudeError{body: []byte(`{"type":"error","error":{"type":"not_found_error","message":"No thread state was found for the requested previous_message_id."}}`)},
	}

	body, errMarshal := json.Marshal(handler.toClaudeError(msg))
	if errMarshal != nil {
		t.Fatalf("marshal Claude error: %v", errMarshal)
	}
	if got := gjson.GetBytes(body, "error.details.error_code").String(); got != "thread_not_found" {
		t.Fatalf("error.details.error_code = %q, want thread_not_found; body=%s", got, body)
	}
}

func TestClaudeErrorMarksMissingThreadForClientReplay(t *testing.T) {
	handler := &ClaudeCodeAPIHandler{}
	msg := &interfaces.ErrorMessage{
		StatusCode: http.StatusNotFound,
		Error:      errors.New(`{"type":"error","error":{"type":"not_found_error","message":"No thread state was found for the requested previous_message_id. Replay the full conversation with thread create to start a new Thread."}}`),
	}

	body, errMarshal := json.Marshal(handler.toClaudeError(msg))
	if errMarshal != nil {
		t.Fatalf("marshal Claude error: %v", errMarshal)
	}
	if got := gjson.GetBytes(body, "error.details.error_code").String(); got != "thread_not_found" {
		t.Fatalf("error.details.error_code = %q, want thread_not_found; body=%s", got, body)
	}
}

func TestClaudeErrorMarksPlainTextMissingThreadForClientReplay(t *testing.T) {
	handler := &ClaudeCodeAPIHandler{}
	msg := &interfaces.ErrorMessage{
		StatusCode: http.StatusNotFound,
		Error:      errors.New("No thread state was found for the requested previous_message_id. Replay the full conversation with thread create to start a new Thread."),
	}

	body, errMarshal := json.Marshal(handler.toClaudeError(msg))
	if errMarshal != nil {
		t.Fatalf("marshal Claude error: %v", errMarshal)
	}
	if got := gjson.GetBytes(body, "error.details.error_code").String(); got != "thread_not_found" {
		t.Fatalf("error.details.error_code = %q, want thread_not_found; body=%s", got, body)
	}
}

type mockClaudeThreadNotFoundError struct {
	msg  string
	body []byte
}

func (e mockClaudeThreadNotFoundError) Error() string        { return e.msg }
func (e mockClaudeThreadNotFoundError) ResponseBody() []byte { return e.body }

func TestClaudeErrorMarksStructuredExecutorMissingThreadForClientReplay(t *testing.T) {
	handler := &ClaudeCodeAPIHandler{}
	msg := &interfaces.ErrorMessage{
		StatusCode: http.StatusNotFound,
		Error: mockClaudeThreadNotFoundError{
			msg:  "No thread state was found for the requested previous_message_id. Replay the full conversation with thread create to start a new Thread.",
			body: []byte(`{"type":"error","error":{"type":"not_found_error","message":"No thread state was found for the requested previous_message_id. Replay the full conversation with thread create to start a new Thread."}}`),
		},
	}

	body, errMarshal := json.Marshal(handler.toClaudeError(msg))
	if errMarshal != nil {
		t.Fatalf("marshal Claude error: %v", errMarshal)
	}
	if got := gjson.GetBytes(body, "error.details.error_code").String(); got != "thread_not_found" {
		t.Fatalf("error.details.error_code = %q, want thread_not_found; body=%s", got, body)
	}
}

func TestWriteClaudeDirectErrorMarksMissingThreadForClientReplay(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	handler := &ClaudeCodeAPIHandler{}
	msg := &interfaces.ErrorMessage{
		StatusCode:     http.StatusNotFound,
		DirectResponse: true,
		Body:           []byte(`{"type":"error","error":{"type":"not_found_error","message":"No thread state was found for the requested previous_message_id. Replay the full conversation with thread create to start a new Thread."}}`),
	}

	handler.WriteErrorResponse(c, msg)

	if got := gjson.GetBytes(recorder.Body.Bytes(), "error.details.error_code").String(); got != "thread_not_found" {
		t.Fatalf("error.details.error_code = %q, want thread_not_found; body=%s", got, recorder.Body.Bytes())
	}
}

func TestWriteClaudeErrorResponseUsesClaudeEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	handler := &ClaudeCodeAPIHandler{}
	msg := &interfaces.ErrorMessage{
		StatusCode: http.StatusBadRequest,
		Error:      errors.New(`{"error":{"message":"Your input exceeds the context window of this model. Please adjust your input and try again.","type":"invalid_request_error","code":"context_too_large"}}`),
	}

	handler.WriteErrorResponse(c, msg)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	body := recorder.Body.Bytes()
	if got := gjson.GetBytes(body, "type").String(); got != "error" {
		t.Fatalf("type = %q, want error; body=%s", got, body)
	}
	if got := gjson.GetBytes(body, "error.type").String(); got != "invalid_request_error" {
		t.Fatalf("error.type = %q, want invalid_request_error; body=%s", got, body)
	}
	if got := gjson.GetBytes(body, "error.message").String(); got != "Your input exceeds the context window of this model. Please adjust your input and try again." {
		t.Fatalf("error.message = %q; body=%s", got, body)
	}
}

func TestWriteClaudeErrorResponse_IncludesRetryAfterForModelCooldownDefaultSettings(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	handler := &ClaudeCodeAPIHandler{}

	cooldownErr := coreauth.NewManager(nil, nil, nil)
	_ = cooldownErr
	// Create mock model cooldown error
	msg := &interfaces.ErrorMessage{
		StatusCode: http.StatusTooManyRequests,
		Error:      coreauth.NewModelCooldownError("claude-sonnet-4-6", "claude", 20*time.Second),
	}

	handler.WriteErrorResponse(c, msg)

	if recorder.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusTooManyRequests)
	}
	if got := recorder.Header().Get("Retry-After"); got != "20" {
		t.Fatalf("Retry-After = %q, want 20", got)
	}
}

func TestPendingClaudeStreamErrorUsesBufferedError(t *testing.T) {
	wantErr := &interfaces.ErrorMessage{
		StatusCode: http.StatusBadRequest,
		Error:      errors.New(`{"error":{"message":"Your input exceeds the context window of this model. Please adjust your input and try again.","type":"invalid_request_error","code":"context_too_large"}}`),
	}
	errs := make(chan *interfaces.ErrorMessage, 1)
	errs <- wantErr
	close(errs)

	gotErr, ok := handlers.PendingStreamError(errs)
	if !ok {
		t.Fatal("expected pending stream error")
	}
	if gotErr != wantErr {
		t.Fatalf("pending error = %p, want %p", gotErr, wantErr)
	}
}

type preFTKeepAliveClaudeExecutor struct {
	chunks chan coreexecutor.StreamChunk
}

func (*preFTKeepAliveClaudeExecutor) Identifier() string { return "pre-ft-keepalive-claude-executor" }

func (*preFTKeepAliveClaudeExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *preFTKeepAliveClaudeExecutor) ExecuteStream(_ context.Context, _ *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return &coreexecutor.StreamResult{Chunks: e.chunks}, nil
}

func (*preFTKeepAliveClaudeExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (*preFTKeepAliveClaudeExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (*preFTKeepAliveClaudeExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func TestClaudeMessagesHandlerSendKeepAliveBeforeFTWritesSSEErrorOnFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tickChan := make(chan time.Time)
	restore := handlers.SetStreamingKeepAliveTickerFactoryForTest(func(time.Duration) (<-chan time.Time, func()) {
		return tickChan, func() {}
	})
	defer restore()

	chunks := make(chan coreexecutor.StreamChunk, 1)
	executor := &preFTKeepAliveClaudeExecutor{chunks: chunks}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	model := "pre-ft-keepalive-claude-model"
	auth := &coreauth.Auth{ID: "pre-ft-keepalive-claude-auth", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	go func() {
		tickChan <- time.Unix(1, 0)
		tickChan <- time.Unix(2, 0)
		chunks <- coreexecutor.StreamChunk{Err: errors.New("claude upstream failed after keepalive")}
		close(chunks)
	}()

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{
		Streaming: sdkconfig.StreamingConfig{
			KeepAliveSeconds:      10,
			SendKeepAliveBeforeFT: true,
		},
	}, manager)
	h := NewClaudeCodeAPIHandler(base)
	router := gin.New()
	router.POST("/v1/messages", h.ClaudeMessages)

	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{\"model\":\""+model+"\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}],\"stream\":true}"))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 after keepalive commits SSE; body=%q", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, ": keep-alive\n\n") {
		t.Fatalf("missing keep-alive comment: %q", body)
	}
	if !strings.Contains(body, "event: error\ndata: ") || !strings.Contains(body, "claude upstream failed after keepalive") {
		t.Fatalf("missing Claude SSE JSON error event after keepalive: %q", body)
	}
}

func TestClaudeMessagesHandlerDeduplicatesTranslatedSSEErrorAndTerminalChunkError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	chunks := make(chan coreexecutor.StreamChunk, 2)
	executor := &preFTKeepAliveClaudeExecutor{chunks: chunks}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	model := "dedup-sse-error-claude-model"
	auth := &coreauth.Auth{ID: "dedup-sse-error-claude-auth", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	const sseErrorChunk = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"m\",\"stop_reason\":null,\"stop_sequence\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":0}}}\n\nevent: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":\"Gemini response finished with MALFORMED_FUNCTION_CALL\"}}\n\n"
	chunks <- coreexecutor.StreamChunk{Payload: []byte(sseErrorChunk)}
	chunks <- coreexecutor.StreamChunk{Err: errors.New(`{"type":"error","error":{"type":"api_error","message":"Gemini response finished with MALFORMED_FUNCTION_CALL"}}`)}
	close(chunks)

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewClaudeCodeAPIHandler(base)
	router := gin.New()
	router.POST("/v1/messages", h.ClaudeMessages)

	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{\"model\":\""+model+"\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}],\"stream\":true}"))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	body := recorder.Body.String()
	if got := strings.Count(body, "event: error\n"); got != 1 {
		t.Fatalf("SSE error event count = %d, want 1; body=%q", got, body)
	}
}

func TestClaudeMessagesHandlerTruncatedStreamWithoutMessageStopWritesSSEError(t *testing.T) {
	gin.SetMode(gin.TestMode)

	chunks := make(chan coreexecutor.StreamChunk, 2)
	executor := &preFTKeepAliveClaudeExecutor{chunks: chunks}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	model := "truncated-stream-claude-model"
	auth := &coreauth.Auth{ID: "truncated-stream-claude-auth", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	chunks <- coreexecutor.StreamChunk{Payload: []byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"m\",\"stop_reason\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":1}}}\n\n")}
	chunks <- coreexecutor.StreamChunk{Payload: []byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"partial\"}}\n\n")}
	close(chunks)

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewClaudeCodeAPIHandler(base)
	router := gin.New()
	router.POST("/v1/messages", h.ClaudeMessages)

	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{\"model\":\""+model+"\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}],\"stream\":true}"))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "event: error\ndata: ") {
		t.Fatalf("expected terminal SSE error event on truncated stream; body=%q", body)
	}
	if !strings.Contains(body, "message_stop") {
		t.Fatalf("expected terminal error message to mention missing message_stop; body=%q", body)
	}
}

func TestClaudeStreamTerminalErrorDoesNotOverwriteSSEStatusWhenUnwritten(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Header("Content-Type", "text/event-stream")

	handler := NewClaudeCodeAPIHandler(&handlers.BaseAPIHandler{})
	errs := make(chan *interfaces.ErrorMessage, 1)
	errs <- &interfaces.ErrorMessage{
		StatusCode: http.StatusBadGateway,
		Error:      errors.New("upstream failed after SSE commit"),
	}
	close(errs)

	handler.forwardClaudeStream(c, c.Writer, func(error) {}, nil, errs)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for committed SSE stream; body=%q", recorder.Code, recorder.Body.String())
	}
	if !strings.HasPrefix(recorder.Body.String(), "event: error\ndata: ") {
		t.Fatalf("unexpected SSE error body: %q", recorder.Body.String())
	}
}

func TestClaudeStreamTerminalErrorAfterPartialLineStartsOnNewLine(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Header("Content-Type", "text/event-stream")

	handler := NewClaudeCodeAPIHandler(&handlers.BaseAPIHandler{})
	data := make(chan []byte)
	errs := make(chan *interfaces.ErrorMessage)
	go func() {
		data <- []byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\"")
		errs <- &interfaces.ErrorMessage{
			StatusCode: http.StatusBadGateway,
			Error:      errors.New("stream cut mid-line"),
		}
		close(data)
		close(errs)
	}()

	handler.forwardClaudeStream(c, c.Writer, func(error) {}, data, errs)

	body := recorder.Body.String()
	if !strings.Contains(body, "\nevent: error\ndata: ") {
		t.Fatalf("expected event: error to start on a fresh line after partial chunk; body=%q", body)
	}
}

func TestClaudeStreamCompleteWithMessageStopDoesNotEmitErrorOnClose(t *testing.T) {
	gin.SetMode(gin.TestMode)

	chunks := make(chan coreexecutor.StreamChunk, 2)
	executor := &preFTKeepAliveClaudeExecutor{chunks: chunks}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	model := "complete-stream-claude-model"
	auth := &coreauth.Auth{ID: "complete-stream-claude-auth", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	chunks <- coreexecutor.StreamChunk{Payload: []byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"m\",\"stop_reason\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":1}}}\n\n")}
	chunks <- coreexecutor.StreamChunk{Payload: []byte("event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n")}
	close(chunks)

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewClaudeCodeAPIHandler(base)
	router := gin.New()
	router.POST("/v1/messages", h.ClaudeMessages)

	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{\"model\":\""+model+"\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}],\"stream\":true}"))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "event: error") {
		t.Fatalf("unexpected error event on completed stream: %q", recorder.Body.String())
	}
}

func TestClaudeStreamTerminalErrorAfterSingleNewlineInsertsBlankLine(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Header("Content-Type", "text/event-stream")

	handler := NewClaudeCodeAPIHandler(&handlers.BaseAPIHandler{})
	data := make(chan []byte)
	errs := make(chan *interfaces.ErrorMessage)
	go func() {
		// Ends with a single '\n' (complete line, but missing the blank-line SSE event separator).
		data <- []byte("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n")
		errs <- &interfaces.ErrorMessage{
			StatusCode: http.StatusBadGateway,
			Error:      errors.New("stream cut before blank line"),
		}
		close(data)
		close(errs)
	}()

	handler.forwardClaudeStream(c, c.Writer, func(error) {}, data, errs)

	body := recorder.Body.String()
	if !strings.Contains(body, "\n\nevent: error\ndata: ") {
		t.Fatalf("expected blank-line SSE separator (\\n\\n) before event: error when prior chunk ended with a single newline; body=%q", body)
	}
}

func TestClaudeStreamBareCompletedMessageJSONDoesNotEmitErrorOnClose(t *testing.T) {
	gin.SetMode(gin.TestMode)

	chunks := make(chan coreexecutor.StreamChunk, 1)
	executor := &preFTKeepAliveClaudeExecutor{chunks: chunks}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	model := "bare-json-stream-claude-model"
	auth := &coreauth.Auth{ID: "bare-json-stream-claude-auth", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	chunks <- coreexecutor.StreamChunk{Payload: []byte("{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"m\",\"content\":[{\"type\":\"text\",\"text\":\"ok\"}],\"stop_reason\":\"end_turn\",\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}\n")}
	close(chunks)

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewClaudeCodeAPIHandler(base)
	router := gin.New()
	router.POST("/v1/messages", h.ClaudeMessages)

	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{\"model\":\""+model+"\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}],\"stream\":true}"))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "event: error") {
		t.Fatalf("unexpected error event after complete bare message JSON: %q", recorder.Body.String())
	}
}

func TestClaudeStreamMessageStopWithArbitraryJSONFormattingCompletesCleanly(t *testing.T) {
	gin.SetMode(gin.TestMode)

	chunks := make(chan coreexecutor.StreamChunk, 2)
	executor := &preFTKeepAliveClaudeExecutor{chunks: chunks}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	model := "formatted-message-stop-claude-model"
	auth := &coreauth.Auth{ID: "formatted-message-stop-claude-auth", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	chunks <- coreexecutor.StreamChunk{Payload: []byte("event:message_start\ndata: {\"type\" : \"message_start\", \"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"m\",\"stop_reason\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":1}}}\n\n")}
	chunks <- coreexecutor.StreamChunk{Payload: []byte("event:message_stop\ndata: {\"type\" : \"message_stop\"}\n\n")}
	close(chunks)

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewClaudeCodeAPIHandler(base)
	router := gin.New()
	router.POST("/v1/messages", h.ClaudeMessages)

	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{\"model\":\""+model+"\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}],\"stream\":true}"))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "event: error") {
		t.Fatalf("unexpected error event when valid message_stop used custom spacing: %q", recorder.Body.String())
	}
}

func TestClaudeStreamTextDeltaMentioningMessageStopStillFailsWhenTruncated(t *testing.T) {
	gin.SetMode(gin.TestMode)

	chunks := make(chan coreexecutor.StreamChunk, 2)
	executor := &preFTKeepAliveClaudeExecutor{chunks: chunks}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	model := "false-positive-message-stop-claude-model"
	auth := &coreauth.Auth{ID: "false-positive-message-stop-claude-auth", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
		t.Fatalf("register auth: %v", errRegister)
	}
	registry.GetGlobalRegistry().RegisterClient(auth.ID, auth.Provider, []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(auth.ID)
	})

	chunks <- coreexecutor.StreamChunk{Payload: []byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"m\",\"stop_reason\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":1}}}\n\n")}
	chunks <- coreexecutor.StreamChunk{Payload: []byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"emit\",\"input\":{\"type\":\"message_stop\"}}}\n\n")}
	close(chunks)

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewClaudeCodeAPIHandler(base)
	router := gin.New()
	router.POST("/v1/messages", h.ClaudeMessages)

	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{\"model\":\""+model+"\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}],\"stream\":true}"))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)

	if !strings.Contains(recorder.Body.String(), "event: error\ndata: ") {
		t.Fatalf("expected truncated stream to emit terminal SSE error even if text delta mentioned message_stop; body=%q", recorder.Body.String())
	}
}

func TestClaudeStreamTruncatedErrorEventHeaderDoesNotSuppressTerminalError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Header("Content-Type", "text/event-stream")

	handler := NewClaudeCodeAPIHandler(&handlers.BaseAPIHandler{})
	data := make(chan []byte)
	errs := make(chan *interfaces.ErrorMessage)
	go func() {
		// Upstream started an error event header but dropped before sending a valid data JSON frame.
		data <- []byte("event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"model\":\"m\",\"stop_reason\":null,\"usage\":{\"input_tokens\":10,\"output_tokens\":1}}}\n\nevent: error\n")
		errs <- &interfaces.ErrorMessage{
			StatusCode: http.StatusBadGateway,
			Error:      errors.New("upstream stream closed before message_stop"),
		}
		close(data)
		close(errs)
	}()

	handler.forwardClaudeStream(c, c.Writer, func(error) {}, data, errs)

	body := recorder.Body.String()
	if !strings.Contains(body, "event: error\ndata: {\"type\":\"error\"") {
		t.Fatalf("expected valid terminal SSE error frame after truncated event: error header; body=%q", body)
	}
}

func TestClaudeStreamErrorEventWithoutTrailingBlankLineIsTerminatedOnEOF(t *testing.T) {
	for _, tc := range []struct {
		name   string
		suffix string
	}{
		{name: "zero trailing newlines", suffix: ""},
		{name: "single trailing newline", suffix: "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			c.Header("Content-Type", "text/event-stream")

			handler := NewClaudeCodeAPIHandler(&handlers.BaseAPIHandler{})
			data := make(chan []byte)
			errs := make(chan *interfaces.ErrorMessage)
			go func() {
				data <- []byte("event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":\"failed\"}}" + tc.suffix)
				errs <- &interfaces.ErrorMessage{
					StatusCode: http.StatusBadGateway,
					Error:      errors.New(`{"type":"error","error":{"type":"api_error","message":"failed"}}`),
				}
				close(data)
				close(errs)
			}()

			handler.forwardClaudeStream(c, c.Writer, func(error) {}, data, errs)

			body := recorder.Body.String()
			want := "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"api_error\",\"message\":\"failed\"}}\n\n"
			if body != want {
				t.Fatalf("SSE body = %q, want %q", body, want)
			}
		})
	}
}

func TestClaudeStreamMessageStopWithoutTrailingBlankLineIsTerminatedOnEOF(t *testing.T) {
	for _, tc := range []struct {
		name   string
		suffix string
	}{
		{name: "zero trailing newlines", suffix: ""},
		{name: "single trailing newline", suffix: "\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
			c.Header("Content-Type", "text/event-stream")

			handler := NewClaudeCodeAPIHandler(&handlers.BaseAPIHandler{})
			data := make(chan []byte, 1)
			data <- []byte("event: message_stop\ndata: {\"type\":\"message_stop\"}" + tc.suffix)
			close(data)

			handler.forwardClaudeStream(c, c.Writer, func(error) {}, data, nil)

			body := recorder.Body.String()
			want := "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
			if body != want {
				t.Fatalf("SSE body = %q, want %q", body, want)
			}
		})
	}
}

func TestClaudeMessagesHandlerEmptyStreamClosesWithSSEError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Header("Content-Type", "text/event-stream")

	handler := NewClaudeCodeAPIHandler(&handlers.BaseAPIHandler{})
	data := make(chan []byte)
	close(data)
	errs := make(chan *interfaces.ErrorMessage)
	close(errs)

	done := make(chan struct{})
	go func() {
		handler.forwardClaudeStream(c, c.Writer, func(error) {}, data, errs)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("forwardClaudeStream hung when data and errs channels closed without chunks")
	}

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%q", recorder.Code, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "event: error\ndata: ") || !strings.Contains(body, "message_stop") {
		t.Fatalf("expected terminal SSE error event mentioning message_stop on empty stream; body=%q", body)
	}
}

func TestClaudeStreamMultiLineDataEventsAreRecognizedAcrossChunks(t *testing.T) {
	t.Run("multiline message_stop split across chunks", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		c.Header("Content-Type", "text/event-stream")

		handler := NewClaudeCodeAPIHandler(&handlers.BaseAPIHandler{})
		data := make(chan []byte, 2)
		data <- []byte("event: message_stop\ndata: {\"type\":\n")
		data <- []byte("data: \"message_stop\"}\n\n")
		close(data)

		handler.forwardClaudeStream(c, c.Writer, func(error) {}, data, nil)

		body := recorder.Body.String()
		if strings.Contains(body, "event: error") {
			t.Fatalf("unexpected error event after valid multi-line message_stop; body=%q", body)
		}
	})

	t.Run("multiline error event split across chunks", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		c.Header("Content-Type", "text/event-stream")

		handler := NewClaudeCodeAPIHandler(&handlers.BaseAPIHandler{})
		data := make(chan []byte)
		errs := make(chan *interfaces.ErrorMessage)
		go func() {
			data <- []byte("event: error\ndata: {\"type\":\"error\",\n")
			data <- []byte("data: \"error\":{\"type\":\"api_error\",\"message\":\"boom\"}}\n\n")
			errs <- &interfaces.ErrorMessage{
				StatusCode: http.StatusBadGateway,
				Error:      errors.New("boom"),
			}
			close(data)
			close(errs)
		}()

		handler.forwardClaudeStream(c, c.Writer, func(error) {}, data, errs)

		body := recorder.Body.String()
		if got := strings.Count(body, "event: error"); got != 1 {
			t.Fatalf("event: error count = %d, want 1; body=%q", got, body)
		}
	})

	t.Run("multiline tool_use with standalone nested message_stop line still fails when truncated", func(t *testing.T) {
		gin.SetMode(gin.TestMode)
		recorder := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(recorder)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
		c.Header("Content-Type", "text/event-stream")

		handler := NewClaudeCodeAPIHandler(&handlers.BaseAPIHandler{})
		data := make(chan []byte, 1)
		data <- []byte("event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"toolu_1\",\"name\":\"emit\",\"input\":\ndata: {\"type\":\"message_stop\"}\ndata: }}\n\n")
		close(data)

		handler.forwardClaudeStream(c, c.Writer, func(error) {}, data, nil)

		body := recorder.Body.String()
		if !strings.Contains(body, "event: error\ndata: ") {
			t.Fatalf("expected terminal error event when stream truncated after multiline tool_use containing nested message_stop line; body=%q", body)
		}
	})
}
