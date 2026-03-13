package openai

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
	"github.com/tidwall/gjson"
)

type websocketCaptureExecutor struct {
	streamCalls int
	payloads    [][]byte
}

type websocketStatusErr struct {
	code int
	msg  string
}

func (e websocketStatusErr) Error() string   { return e.msg }
func (e websocketStatusErr) StatusCode() int { return e.code }

type websocketSequenceStep struct {
	wantAuthID string
	responseID string
	outputID   string
	err        error
}

type websocketSequenceExecutor struct {
	id string
	t  *testing.T

	mu          sync.Mutex
	streamCalls int
	payloads    [][]byte
	authIDs     []string
	steps       []websocketSequenceStep
}

func (e *websocketSequenceExecutor) Identifier() string { return e.id }

func (e *websocketSequenceExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *websocketSequenceExecutor) ExecuteStream(_ context.Context, auth *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	idx := e.streamCalls
	if idx >= len(e.steps) {
		e.t.Fatalf("unexpected ExecuteStream call #%d for auth %s", idx+1, auth.ID)
	}
	step := e.steps[idx]
	e.streamCalls++
	e.authIDs = append(e.authIDs, auth.ID)
	e.payloads = append(e.payloads, bytes.Clone(req.Payload))
	if step.wantAuthID != "" && auth.ID != step.wantAuthID {
		e.t.Fatalf("ExecuteStream call #%d auth = %s, want %s", idx+1, auth.ID, step.wantAuthID)
	}
	chunks := make(chan coreexecutor.StreamChunk, 1)
	if step.err != nil {
		chunks <- coreexecutor.StreamChunk{Err: step.err}
		close(chunks)
		return &coreexecutor.StreamResult{Chunks: chunks}, nil
	}
	payload := fmt.Sprintf(`{"type":"response.completed","response":{"id":%q,"output":[{"type":"message","id":%q}]}}`, step.responseID, step.outputID)
	chunks <- coreexecutor.StreamChunk{Payload: []byte(payload)}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *websocketSequenceExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *websocketSequenceExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *websocketSequenceExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func (e *websocketCaptureExecutor) Identifier() string { return "test-provider" }

func (e *websocketCaptureExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *websocketCaptureExecutor) ExecuteStream(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	e.streamCalls++
	e.payloads = append(e.payloads, bytes.Clone(req.Payload))
	chunks := make(chan coreexecutor.StreamChunk, 1)
	chunks <- coreexecutor.StreamChunk{Payload: []byte(`{"type":"response.completed","response":{"id":"resp-upstream","output":[{"type":"message","id":"out-1"}]}}`)}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *websocketCaptureExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *websocketCaptureExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *websocketCaptureExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func TestNormalizeResponsesWebsocketRequestCreate(t *testing.T) {
	raw := []byte(`{"type":"response.create","model":"test-model","stream":false,"input":[{"type":"message","id":"msg-1"}]}`)

	normalized, last, errMsg := normalizeResponsesWebsocketRequest(raw, nil, nil)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}
	if gjson.GetBytes(normalized, "type").Exists() {
		t.Fatalf("normalized create request must not include type field")
	}
	if !gjson.GetBytes(normalized, "stream").Bool() {
		t.Fatalf("normalized create request must force stream=true")
	}
	if gjson.GetBytes(normalized, "model").String() != "test-model" {
		t.Fatalf("unexpected model: %s", gjson.GetBytes(normalized, "model").String())
	}
	if !bytes.Equal(last, normalized) {
		t.Fatalf("last request snapshot should match normalized request")
	}
}

func TestNormalizeResponsesWebsocketRequestCreateWithHistory(t *testing.T) {
	lastRequest := []byte(`{"model":"test-model","stream":true,"input":[{"type":"message","id":"msg-1"}]}`)
	lastResponseOutput := []byte(`[
		{"type":"function_call","id":"fc-1","call_id":"call-1"},
		{"type":"message","id":"assistant-1"}
	]`)
	raw := []byte(`{"type":"response.create","input":[{"type":"function_call_output","call_id":"call-1","id":"tool-out-1"}]}`)

	normalized, next, errMsg := normalizeResponsesWebsocketRequest(raw, lastRequest, lastResponseOutput)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}
	if gjson.GetBytes(normalized, "type").Exists() {
		t.Fatalf("normalized subsequent create request must not include type field")
	}
	if gjson.GetBytes(normalized, "model").String() != "test-model" {
		t.Fatalf("unexpected model: %s", gjson.GetBytes(normalized, "model").String())
	}

	input := gjson.GetBytes(normalized, "input").Array()
	if len(input) != 4 {
		t.Fatalf("merged input len = %d, want 4", len(input))
	}
	if input[0].Get("id").String() != "msg-1" ||
		input[1].Get("id").String() != "fc-1" ||
		input[2].Get("id").String() != "assistant-1" ||
		input[3].Get("id").String() != "tool-out-1" {
		t.Fatalf("unexpected merged input order")
	}
	if !bytes.Equal(next, normalized) {
		t.Fatalf("next request snapshot should match normalized request")
	}
}

func TestNormalizeResponsesWebsocketRequestWithPreviousResponseIDIncremental(t *testing.T) {
	lastRequest := []byte(`{"model":"test-model","stream":true,"instructions":"be helpful","input":[{"type":"message","id":"msg-1"}]}`)
	lastResponseOutput := []byte(`[
		{"type":"function_call","id":"fc-1","call_id":"call-1"},
		{"type":"message","id":"assistant-1"}
	]`)
	raw := []byte(`{"type":"response.create","previous_response_id":"resp-1","input":[{"type":"function_call_output","call_id":"call-1","id":"tool-out-1"}]}`)

	normalized, next, errMsg := normalizeResponsesWebsocketRequestWithMode(raw, lastRequest, lastResponseOutput, true)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}
	if gjson.GetBytes(normalized, "type").Exists() {
		t.Fatalf("normalized request must not include type field")
	}
	if gjson.GetBytes(normalized, "previous_response_id").String() != "resp-1" {
		t.Fatalf("previous_response_id must be preserved in incremental mode")
	}
	input := gjson.GetBytes(normalized, "input").Array()
	if len(input) != 1 {
		t.Fatalf("incremental input len = %d, want 1", len(input))
	}
	if input[0].Get("id").String() != "tool-out-1" {
		t.Fatalf("unexpected incremental input item id: %s", input[0].Get("id").String())
	}
	if gjson.GetBytes(normalized, "model").String() != "test-model" {
		t.Fatalf("unexpected model: %s", gjson.GetBytes(normalized, "model").String())
	}
	if gjson.GetBytes(normalized, "instructions").String() != "be helpful" {
		t.Fatalf("unexpected instructions: %s", gjson.GetBytes(normalized, "instructions").String())
	}
	if !bytes.Equal(next, normalized) {
		t.Fatalf("next request snapshot should match normalized request")
	}
}

func TestNormalizeResponsesWebsocketRequestWithPreviousResponseIDMergedWhenIncrementalDisabled(t *testing.T) {
	lastRequest := []byte(`{"model":"test-model","stream":true,"input":[{"type":"message","id":"msg-1"}]}`)
	lastResponseOutput := []byte(`[
		{"type":"function_call","id":"fc-1","call_id":"call-1"},
		{"type":"message","id":"assistant-1"}
	]`)
	raw := []byte(`{"type":"response.create","previous_response_id":"resp-1","input":[{"type":"function_call_output","call_id":"call-1","id":"tool-out-1"}]}`)

	normalized, next, errMsg := normalizeResponsesWebsocketRequestWithMode(raw, lastRequest, lastResponseOutput, false)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}
	if gjson.GetBytes(normalized, "previous_response_id").Exists() {
		t.Fatalf("previous_response_id must be removed when incremental mode is disabled")
	}
	input := gjson.GetBytes(normalized, "input").Array()
	if len(input) != 4 {
		t.Fatalf("merged input len = %d, want 4", len(input))
	}
	if input[0].Get("id").String() != "msg-1" ||
		input[1].Get("id").String() != "fc-1" ||
		input[2].Get("id").String() != "assistant-1" ||
		input[3].Get("id").String() != "tool-out-1" {
		t.Fatalf("unexpected merged input order")
	}
	if !bytes.Equal(next, normalized) {
		t.Fatalf("next request snapshot should match normalized request")
	}
}

func TestNormalizeResponsesWebsocketRequestAppend(t *testing.T) {
	lastRequest := []byte(`{"model":"test-model","stream":true,"input":[{"type":"message","id":"msg-1"}]}`)
	lastResponseOutput := []byte(`[
		{"type":"message","id":"assistant-1"},
		{"type":"function_call_output","id":"tool-out-1"}
	]`)
	raw := []byte(`{"type":"response.append","input":[{"type":"message","id":"msg-2"},{"type":"message","id":"msg-3"}]}`)

	normalized, next, errMsg := normalizeResponsesWebsocketRequest(raw, lastRequest, lastResponseOutput)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}
	input := gjson.GetBytes(normalized, "input").Array()
	if len(input) != 5 {
		t.Fatalf("merged input len = %d, want 5", len(input))
	}
	if input[0].Get("id").String() != "msg-1" ||
		input[1].Get("id").String() != "assistant-1" ||
		input[2].Get("id").String() != "tool-out-1" ||
		input[3].Get("id").String() != "msg-2" ||
		input[4].Get("id").String() != "msg-3" {
		t.Fatalf("unexpected merged input order")
	}
	if !bytes.Equal(next, normalized) {
		t.Fatalf("next request snapshot should match normalized append request")
	}
}

func TestNormalizeResponsesWebsocketRequestAppendWithoutCreate(t *testing.T) {
	raw := []byte(`{"type":"response.append","input":[]}`)

	_, _, errMsg := normalizeResponsesWebsocketRequest(raw, nil, nil)
	if errMsg == nil {
		t.Fatalf("expected error for append without previous request")
	}
	if errMsg.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", errMsg.StatusCode, http.StatusBadRequest)
	}
}

func TestWebsocketJSONPayloadsFromChunk(t *testing.T) {
	chunk := []byte("event: response.created\n\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp-1\"}}\n\ndata: [DONE]\n")

	payloads := websocketJSONPayloadsFromChunk(chunk)
	if len(payloads) != 1 {
		t.Fatalf("payloads len = %d, want 1", len(payloads))
	}
	if gjson.GetBytes(payloads[0], "type").String() != "response.created" {
		t.Fatalf("unexpected payload type: %s", gjson.GetBytes(payloads[0], "type").String())
	}
}

func TestWebsocketJSONPayloadsFromPlainJSONChunk(t *testing.T) {
	chunk := []byte(`{"type":"response.completed","response":{"id":"resp-1"}}`)

	payloads := websocketJSONPayloadsFromChunk(chunk)
	if len(payloads) != 1 {
		t.Fatalf("payloads len = %d, want 1", len(payloads))
	}
	if gjson.GetBytes(payloads[0], "type").String() != "response.completed" {
		t.Fatalf("unexpected payload type: %s", gjson.GetBytes(payloads[0], "type").String())
	}
}

func TestResponseCompletedOutputFromPayload(t *testing.T) {
	payload := []byte(`{"type":"response.completed","response":{"id":"resp-1","output":[{"type":"message","id":"out-1"}]}}`)

	output := responseCompletedOutputFromPayload(payload)
	items := gjson.ParseBytes(output).Array()
	if len(items) != 1 {
		t.Fatalf("output len = %d, want 1", len(items))
	}
	if items[0].Get("id").String() != "out-1" {
		t.Fatalf("unexpected output id: %s", items[0].Get("id").String())
	}
}

func TestAppendWebsocketEvent(t *testing.T) {
	var builder strings.Builder

	appendWebsocketEvent(&builder, "request", []byte("  {\"type\":\"response.create\"}\n"))
	appendWebsocketEvent(&builder, "response", []byte("{\"type\":\"response.created\"}"))

	got := builder.String()
	if !strings.Contains(got, "websocket.request\n{\"type\":\"response.create\"}\n") {
		t.Fatalf("request event not found in body: %s", got)
	}
	if !strings.Contains(got, "websocket.response\n{\"type\":\"response.created\"}\n") {
		t.Fatalf("response event not found in body: %s", got)
	}
}

func TestAppendWebsocketEventTruncatesAtLimit(t *testing.T) {
	var builder strings.Builder
	payload := bytes.Repeat([]byte("x"), wsBodyLogMaxSize)

	appendWebsocketEvent(&builder, "request", payload)

	got := builder.String()
	if len(got) > wsBodyLogMaxSize {
		t.Fatalf("body log len = %d, want <= %d", len(got), wsBodyLogMaxSize)
	}
	if !strings.Contains(got, wsBodyLogTruncated) {
		t.Fatalf("expected truncation marker in body log")
	}
}

func TestAppendWebsocketEventNoGrowthAfterLimit(t *testing.T) {
	var builder strings.Builder
	appendWebsocketEvent(&builder, "request", bytes.Repeat([]byte("x"), wsBodyLogMaxSize))
	initial := builder.String()

	appendWebsocketEvent(&builder, "response", []byte(`{"type":"response.completed"}`))

	if builder.String() != initial {
		t.Fatalf("builder grew after reaching limit")
	}
}

func TestSetWebsocketRequestBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	setWebsocketRequestBody(c, " \n ")
	if _, exists := c.Get(wsRequestBodyKey); exists {
		t.Fatalf("request body key should not be set for empty body")
	}

	setWebsocketRequestBody(c, "event body")
	value, exists := c.Get(wsRequestBodyKey)
	if !exists {
		t.Fatalf("request body key not set")
	}
	bodyBytes, ok := value.([]byte)
	if !ok {
		t.Fatalf("request body key type mismatch")
	}
	if string(bodyBytes) != "event body" {
		t.Fatalf("request body = %q, want %q", string(bodyBytes), "event body")
	}
}

func TestForwardResponsesWebsocketPreservesCompletedEvent(t *testing.T) {
	gin.SetMode(gin.TestMode)

	serverErrCh := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := responsesWebsocketUpgrader.Upgrade(w, r, nil)
		if err != nil {
			serverErrCh <- err
			return
		}
		defer func() {
			errClose := conn.Close()
			if errClose != nil {
				serverErrCh <- errClose
			}
		}()

		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Request = r

		data := make(chan []byte, 1)
		errCh := make(chan *interfaces.ErrorMessage)
		data <- []byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"output\":[{\"type\":\"message\",\"id\":\"out-1\"}]}}\n\n")
		close(data)
		close(errCh)

		var bodyLog strings.Builder
		completedOutput, err, _ := (*OpenAIResponsesAPIHandler)(nil).forwardResponsesWebsocket(
			ctx,
			conn,
			func(...interface{}) {},
			data,
			errCh,
			&bodyLog,
			"session-1",
		)
		if err != nil {
			serverErrCh <- err
			return
		}
		if gjson.GetBytes(completedOutput, "0.id").String() != "out-1" {
			serverErrCh <- errors.New("completed output not captured")
			return
		}
		serverErrCh <- nil
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() {
		errClose := conn.Close()
		if errClose != nil {
			t.Fatalf("close websocket: %v", errClose)
		}
	}()

	_, payload, errReadMessage := conn.ReadMessage()
	if errReadMessage != nil {
		t.Fatalf("read websocket message: %v", errReadMessage)
	}
	if gjson.GetBytes(payload, "type").String() != wsEventTypeCompleted {
		t.Fatalf("payload type = %s, want %s", gjson.GetBytes(payload, "type").String(), wsEventTypeCompleted)
	}
	if strings.Contains(string(payload), "response.done") {
		t.Fatalf("payload unexpectedly rewrote completed event: %s", payload)
	}

	if errServer := <-serverErrCh; errServer != nil {
		t.Fatalf("server error: %v", errServer)
	}
}

func TestWebsocketUpstreamSupportsIncrementalInputForModel(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	auth := &coreauth.Auth{
		ID:         "auth-ws",
		Provider:   "test-provider",
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"websockets": "true"},
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
	if !h.websocketUpstreamSupportsIncrementalInputForModel("test-model") {
		t.Fatalf("expected websocket-capable upstream for test-model")
	}
}

func TestResponsesWebsocketPrewarmHandledLocallyForSSEUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &websocketCaptureExecutor{}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)
	auth := &coreauth.Auth{ID: "auth-sse", Provider: executor.Identifier(), Status: coreauth.StatusActive}
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
	router.GET("/v1/responses/ws", h.ResponsesWebsocket)

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() {
		errClose := conn.Close()
		if errClose != nil {
			t.Fatalf("close websocket: %v", errClose)
		}
	}()

	errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"test-model","generate":false}`))
	if errWrite != nil {
		t.Fatalf("write prewarm websocket message: %v", errWrite)
	}

	_, createdPayload, errReadMessage := conn.ReadMessage()
	if errReadMessage != nil {
		t.Fatalf("read prewarm created message: %v", errReadMessage)
	}
	if gjson.GetBytes(createdPayload, "type").String() != "response.created" {
		t.Fatalf("created payload type = %s, want response.created", gjson.GetBytes(createdPayload, "type").String())
	}
	prewarmResponseID := gjson.GetBytes(createdPayload, "response.id").String()
	if prewarmResponseID == "" {
		t.Fatalf("prewarm response id is empty")
	}
	if executor.streamCalls != 0 {
		t.Fatalf("stream calls after prewarm = %d, want 0", executor.streamCalls)
	}

	_, completedPayload, errReadMessage := conn.ReadMessage()
	if errReadMessage != nil {
		t.Fatalf("read prewarm completed message: %v", errReadMessage)
	}
	if gjson.GetBytes(completedPayload, "type").String() != wsEventTypeCompleted {
		t.Fatalf("completed payload type = %s, want %s", gjson.GetBytes(completedPayload, "type").String(), wsEventTypeCompleted)
	}
	if gjson.GetBytes(completedPayload, "response.id").String() != prewarmResponseID {
		t.Fatalf("completed response id = %s, want %s", gjson.GetBytes(completedPayload, "response.id").String(), prewarmResponseID)
	}
	if gjson.GetBytes(completedPayload, "response.usage.total_tokens").Int() != 0 {
		t.Fatalf("prewarm total tokens = %d, want 0", gjson.GetBytes(completedPayload, "response.usage.total_tokens").Int())
	}

	secondRequest := fmt.Sprintf(`{"type":"response.create","previous_response_id":%q,"input":[{"type":"message","id":"msg-1"}]}`, prewarmResponseID)
	errWrite = conn.WriteMessage(websocket.TextMessage, []byte(secondRequest))
	if errWrite != nil {
		t.Fatalf("write follow-up websocket message: %v", errWrite)
	}

	_, upstreamPayload, errReadMessage := conn.ReadMessage()
	if errReadMessage != nil {
		t.Fatalf("read upstream completed message: %v", errReadMessage)
	}
	if gjson.GetBytes(upstreamPayload, "type").String() != wsEventTypeCompleted {
		t.Fatalf("upstream payload type = %s, want %s", gjson.GetBytes(upstreamPayload, "type").String(), wsEventTypeCompleted)
	}
	if executor.streamCalls != 1 {
		t.Fatalf("stream calls after follow-up = %d, want 1", executor.streamCalls)
	}
	if len(executor.payloads) != 1 {
		t.Fatalf("captured upstream payloads = %d, want 1", len(executor.payloads))
	}
	forwarded := executor.payloads[0]
	if gjson.GetBytes(forwarded, "previous_response_id").Exists() {
		t.Fatalf("previous_response_id leaked upstream: %s", forwarded)
	}
	if gjson.GetBytes(forwarded, "generate").Exists() {
		t.Fatalf("generate leaked upstream: %s", forwarded)
	}
	if gjson.GetBytes(forwarded, "model").String() != "test-model" {
		t.Fatalf("forwarded model = %s, want test-model", gjson.GetBytes(forwarded, "model").String())
	}
	input := gjson.GetBytes(forwarded, "input").Array()
	if len(input) != 1 || input[0].Get("id").String() != "msg-1" {
		t.Fatalf("unexpected forwarded input: %s", forwarded)
	}
}

func TestResponsesWebsocket_DropsPreviousResponseIDAfterPinnedAuthReset(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &websocketSequenceExecutor{
		id: "test-provider",
		t:  t,
		steps: []websocketSequenceStep{
			{wantAuthID: "auth-a", responseID: "resp-a-1", outputID: "out-a-1"},
			{wantAuthID: "auth-a", err: websocketStatusErr{code: http.StatusForbidden, msg: `{"error":{"code":"token_invalidated"}}`}},
			{wantAuthID: "auth-b", responseID: "resp-b-1", outputID: "out-b-1"},
		},
	}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.RegisterExecutor(executor)

	authA := &coreauth.Auth{ID: "auth-a", Provider: executor.Identifier(), Status: coreauth.StatusActive, Attributes: map[string]string{"websockets": "true"}}
	authB := &coreauth.Auth{ID: "auth-b", Provider: executor.Identifier(), Status: coreauth.StatusActive, Attributes: map[string]string{"websockets": "true"}}
	if _, err := manager.Register(context.Background(), authA); err != nil {
		t.Fatalf("Register authA: %v", err)
	}
	if _, err := manager.Register(context.Background(), authB); err != nil {
		t.Fatalf("Register authB: %v", err)
	}
	registry.GetGlobalRegistry().RegisterClient(authA.ID, authA.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	registry.GetGlobalRegistry().RegisterClient(authB.ID, authB.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(authA.ID)
		registry.GetGlobalRegistry().UnregisterClient(authB.ID)
	})

	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)
	router := gin.New()
	router.GET("/v1/responses/ws", h.ResponsesWebsocket)

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}
	defer func() {
		if errClose := conn.Close(); errClose != nil {
			t.Fatalf("close websocket: %v", errClose)
		}
	}()

	if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(`{"type":"response.create","model":"test-model","input":[{"type":"message","id":"msg-1"}]}`)); errWrite != nil {
		t.Fatalf("write first websocket message: %v", errWrite)
	}
	_, payload, errRead := conn.ReadMessage()
	if errRead != nil {
		t.Fatalf("read first websocket response: %v", errRead)
	}
	if gjson.GetBytes(payload, "type").String() != wsEventTypeCompleted {
		t.Fatalf("first payload type = %s, want %s", gjson.GetBytes(payload, "type").String(), wsEventTypeCompleted)
	}
	previousResponseID := gjson.GetBytes(payload, "response.id").String()
	if previousResponseID != "resp-a-1" {
		t.Fatalf("first response id = %s, want resp-a-1", previousResponseID)
	}

	secondRequest := fmt.Sprintf(`{"type":"response.create","previous_response_id":%q,"input":[{"type":"message","id":"msg-2"}]}`, previousResponseID)
	if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(secondRequest)); errWrite != nil {
		t.Fatalf("write second websocket message: %v", errWrite)
	}
	_, payload, errRead = conn.ReadMessage()
	if errRead != nil {
		t.Fatalf("read second websocket response: %v", errRead)
	}
	if !gjson.GetBytes(payload, "error").Exists() {
		t.Fatalf("expected websocket error payload after pinned auth failure, got %s", payload)
	}

	thirdRequest := fmt.Sprintf(`{"type":"response.create","previous_response_id":%q,"input":[{"type":"message","id":"msg-3"}]}`, previousResponseID)
	if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(thirdRequest)); errWrite != nil {
		t.Fatalf("write third websocket message: %v", errWrite)
	}
	_, payload, errRead = conn.ReadMessage()
	if errRead != nil {
		t.Fatalf("read third websocket response: %v", errRead)
	}
	if gjson.GetBytes(payload, "type").String() != wsEventTypeCompleted {
		t.Fatalf("third payload type = %s, want %s", gjson.GetBytes(payload, "type").String(), wsEventTypeCompleted)
	}

	if executor.streamCalls != 3 {
		t.Fatalf("stream calls = %d, want 3", executor.streamCalls)
	}
	if len(executor.authIDs) != 3 || executor.authIDs[0] != "auth-a" || executor.authIDs[1] != "auth-a" || executor.authIDs[2] != "auth-b" {
		t.Fatalf("unexpected auth selection sequence: %#v", executor.authIDs)
	}
	if !gjson.GetBytes(executor.payloads[1], "previous_response_id").Exists() {
		t.Fatalf("second upstream request should preserve previous_response_id while pin is valid: %s", executor.payloads[1])
	}
	if gjson.GetBytes(executor.payloads[2], "previous_response_id").Exists() {
		t.Fatalf("third upstream request leaked previous_response_id after pin reset: %s", executor.payloads[2])
	}
}

func TestShouldResetPinnedAuthForWebsocketError(t *testing.T) {
	cases := []struct {
		name   string
		errMsg *interfaces.ErrorMessage
		want   bool
	}{
		{
			name:   "nil error message",
			errMsg: nil,
			want:   false,
		},
		{
			name: "status too many requests",
			errMsg: &interfaces.ErrorMessage{
				StatusCode: http.StatusTooManyRequests,
				Error:      errors.New(`{"error":{"type":"usage_limit_reached"}}`),
			},
			want: true,
		},
		{
			name: "status forbidden",
			errMsg: &interfaces.ErrorMessage{
				StatusCode: http.StatusForbidden,
				Error:      errors.New(`{"error":{"code":"token_invalidated"}}`),
			},
			want: true,
		},
		{
			name: "textual quota signal",
			errMsg: &interfaces.ErrorMessage{
				StatusCode: http.StatusBadRequest,
				Error:      errors.New("insufficient_quota"),
			},
			want: true,
		},
		{
			name: "non credential error",
			errMsg: &interfaces.ErrorMessage{
				StatusCode: http.StatusBadRequest,
				Error:      errors.New("invalid_request_error"),
			},
			want: false,
		},
	}

	for _, tc := range cases {
		if got := shouldResetPinnedAuthForWebsocketError(tc.errMsg); got != tc.want {
			t.Fatalf("%s: got %v want %v", tc.name, got, tc.want)
		}
	}
}
