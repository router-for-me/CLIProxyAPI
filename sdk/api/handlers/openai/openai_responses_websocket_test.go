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
	"time"

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

type websocketCompactionCaptureExecutor struct {
	mu             sync.Mutex
	streamPayloads [][]byte
	compactPayload []byte
}

type orderedWebsocketSelector struct {
	mu     sync.Mutex
	order  []string
	cursor int
}

func (s *orderedWebsocketSelector) Pick(_ context.Context, _ string, _ string, _ coreexecutor.Options, auths []*coreauth.Auth) (*coreauth.Auth, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(auths) == 0 {
		return nil, errors.New("no auth available")
	}
	for len(s.order) > 0 && s.cursor < len(s.order) {
		authID := strings.TrimSpace(s.order[s.cursor])
		s.cursor++
		for _, auth := range auths {
			if auth != nil && auth.ID == authID {
				return auth, nil
			}
		}
	}
	for _, auth := range auths {
		if auth != nil {
			return auth, nil
		}
	}
	return nil, errors.New("no auth available")
}

type websocketAuthCaptureExecutor struct {
	mu      sync.Mutex
	authIDs []string
}

func (e *websocketAuthCaptureExecutor) Identifier() string { return "test-provider" }

func (e *websocketAuthCaptureExecutor) Execute(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *websocketAuthCaptureExecutor) ExecuteStream(_ context.Context, auth *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	e.mu.Lock()
	if auth != nil {
		e.authIDs = append(e.authIDs, auth.ID)
	}
	e.mu.Unlock()

	chunks := make(chan coreexecutor.StreamChunk, 1)
	chunks <- coreexecutor.StreamChunk{Payload: []byte(`{"type":"response.completed","response":{"id":"resp-upstream","output":[{"type":"message","id":"out-1"}]}}`)}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *websocketAuthCaptureExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *websocketAuthCaptureExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *websocketAuthCaptureExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("not implemented")
}

func (e *websocketAuthCaptureExecutor) AuthIDs() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.authIDs...)
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

func (e *websocketCompactionCaptureExecutor) Identifier() string { return "test-provider" }

func (e *websocketCompactionCaptureExecutor) Execute(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, opts coreexecutor.Options) (coreexecutor.Response, error) {
	e.mu.Lock()
	e.compactPayload = bytes.Clone(req.Payload)
	e.mu.Unlock()
	if opts.Alt != "responses/compact" {
		return coreexecutor.Response{}, fmt.Errorf("unexpected non-compact execute alt: %q", opts.Alt)
	}
	return coreexecutor.Response{Payload: []byte(`{"id":"cmp-1","object":"response.compaction"}`)}, nil
}

func (e *websocketCompactionCaptureExecutor) ExecuteStream(_ context.Context, _ *coreauth.Auth, req coreexecutor.Request, _ coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	e.mu.Lock()
	callIndex := len(e.streamPayloads)
	e.streamPayloads = append(e.streamPayloads, bytes.Clone(req.Payload))
	e.mu.Unlock()

	var payload []byte
	switch callIndex {
	case 0:
		payload = []byte(`{"type":"response.completed","response":{"id":"resp-1","output":[{"type":"function_call","id":"fc-1","call_id":"call-1","name":"tool"}]}}`)
	case 1:
		payload = []byte(`{"type":"response.completed","response":{"id":"resp-2","output":[{"type":"message","id":"assistant-1"}]}}`)
	default:
		payload = []byte(`{"type":"response.completed","response":{"id":"resp-3","output":[{"type":"message","id":"assistant-2"}]}}`)
	}

	chunks := make(chan coreexecutor.StreamChunk, 1)
	chunks <- coreexecutor.StreamChunk{Payload: payload}
	close(chunks)
	return &coreexecutor.StreamResult{Chunks: chunks}, nil
}

func (e *websocketCompactionCaptureExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	return auth, nil
}

func (e *websocketCompactionCaptureExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("not implemented")
}

func (e *websocketCompactionCaptureExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
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

func TestAppendWebsocketTimelineEvent(t *testing.T) {
	var builder strings.Builder
	ts := time.Date(2026, time.April, 1, 12, 34, 56, 789000000, time.UTC)

	appendWebsocketTimelineEvent(&builder, "request", []byte("  {\"type\":\"response.create\"}\n"), ts)

	got := builder.String()
	if !strings.Contains(got, "Timestamp: 2026-04-01T12:34:56.789Z") {
		t.Fatalf("timeline timestamp not found: %s", got)
	}
	if !strings.Contains(got, "Event: websocket.request") {
		t.Fatalf("timeline event not found: %s", got)
	}
	if !strings.Contains(got, "{\"type\":\"response.create\"}") {
		t.Fatalf("timeline payload not found: %s", got)
	}
}

func TestSetWebsocketTimelineBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)

	setWebsocketTimelineBody(c, " \n ")
	if _, exists := c.Get(wsTimelineBodyKey); exists {
		t.Fatalf("timeline body key should not be set for empty body")
	}

	setWebsocketTimelineBody(c, "timeline body")
	value, exists := c.Get(wsTimelineBodyKey)
	if !exists {
		t.Fatalf("timeline body key not set")
	}
	bodyBytes, ok := value.([]byte)
	if !ok {
		t.Fatalf("timeline body key type mismatch")
	}
	if string(bodyBytes) != "timeline body" {
		t.Fatalf("timeline body = %q, want %q", string(bodyBytes), "timeline body")
	}
}

func TestRepairResponsesWebsocketToolCallsInsertsCachedOutput(t *testing.T) {
	cache := newWebsocketToolOutputCache(time.Minute, 10)
	sessionKey := "session-1"

	cacheWarm := []byte(`{"previous_response_id":"resp-1","input":[{"type":"function_call_output","call_id":"call-1","output":"ok"}]}`)
	warmed := repairResponsesWebsocketToolCallsWithCache(cache, sessionKey, cacheWarm)
	if gjson.GetBytes(warmed, "input.0.call_id").String() != "call-1" {
		t.Fatalf("expected warmup output to remain")
	}

	raw := []byte(`{"input":[{"type":"function_call","call_id":"call-1","name":"tool"},{"type":"message","id":"msg-1"}]}`)
	repaired := repairResponsesWebsocketToolCallsWithCache(cache, sessionKey, raw)

	input := gjson.GetBytes(repaired, "input").Array()
	if len(input) != 3 {
		t.Fatalf("repaired input len = %d, want 3", len(input))
	}
	if input[0].Get("type").String() != "function_call" || input[0].Get("call_id").String() != "call-1" {
		t.Fatalf("unexpected first item: %s", input[0].Raw)
	}
	if input[1].Get("type").String() != "function_call_output" || input[1].Get("call_id").String() != "call-1" {
		t.Fatalf("missing inserted output: %s", input[1].Raw)
	}
	if input[2].Get("type").String() != "message" || input[2].Get("id").String() != "msg-1" {
		t.Fatalf("unexpected trailing item: %s", input[2].Raw)
	}
}

func TestRepairResponsesWebsocketToolCallsDropsOrphanFunctionCall(t *testing.T) {
	cache := newWebsocketToolOutputCache(time.Minute, 10)
	sessionKey := "session-1"

	raw := []byte(`{"input":[{"type":"function_call","call_id":"call-1","name":"tool"},{"type":"message","id":"msg-1"}]}`)
	repaired := repairResponsesWebsocketToolCallsWithCache(cache, sessionKey, raw)

	input := gjson.GetBytes(repaired, "input").Array()
	if len(input) != 1 {
		t.Fatalf("repaired input len = %d, want 1", len(input))
	}
	if input[0].Get("type").String() != "message" || input[0].Get("id").String() != "msg-1" {
		t.Fatalf("unexpected remaining item: %s", input[0].Raw)
	}
}

func TestRepairResponsesWebsocketToolCallsInsertsCachedCallForOrphanOutput(t *testing.T) {
	outputCache := newWebsocketToolOutputCache(time.Minute, 10)
	callCache := newWebsocketToolOutputCache(time.Minute, 10)
	sessionKey := "session-1"

	callCache.record(sessionKey, "call-1", []byte(`{"type":"function_call","call_id":"call-1","name":"tool"}`))

	raw := []byte(`{"input":[{"type":"function_call_output","call_id":"call-1","output":"ok"},{"type":"message","id":"msg-1"}]}`)
	repaired := repairResponsesWebsocketToolCallsWithCaches(outputCache, callCache, sessionKey, raw)

	input := gjson.GetBytes(repaired, "input").Array()
	if len(input) != 3 {
		t.Fatalf("repaired input len = %d, want 3", len(input))
	}
	if input[0].Get("type").String() != "function_call" || input[0].Get("call_id").String() != "call-1" {
		t.Fatalf("missing inserted call: %s", input[0].Raw)
	}
	if input[1].Get("type").String() != "function_call_output" || input[1].Get("call_id").String() != "call-1" {
		t.Fatalf("unexpected output item: %s", input[1].Raw)
	}
	if input[2].Get("type").String() != "message" || input[2].Get("id").String() != "msg-1" {
		t.Fatalf("unexpected trailing item: %s", input[2].Raw)
	}
}

func TestRepairResponsesWebsocketToolCallsDropsOrphanOutputWhenCallMissing(t *testing.T) {
	outputCache := newWebsocketToolOutputCache(time.Minute, 10)
	callCache := newWebsocketToolOutputCache(time.Minute, 10)
	sessionKey := "session-1"

	raw := []byte(`{"input":[{"type":"function_call_output","call_id":"call-1","output":"ok"},{"type":"message","id":"msg-1"}]}`)
	repaired := repairResponsesWebsocketToolCallsWithCaches(outputCache, callCache, sessionKey, raw)

	input := gjson.GetBytes(repaired, "input").Array()
	if len(input) != 1 {
		t.Fatalf("repaired input len = %d, want 1", len(input))
	}
	if input[0].Get("type").String() != "message" || input[0].Get("id").String() != "msg-1" {
		t.Fatalf("unexpected remaining item: %s", input[0].Raw)
	}
}

func TestRepairResponsesWebsocketToolCallsInsertsCachedCustomToolOutput(t *testing.T) {
	cache := newWebsocketToolOutputCache(time.Minute, 10)
	sessionKey := "session-1"

	cacheWarm := []byte(`{"previous_response_id":"resp-1","input":[{"type":"custom_tool_call_output","call_id":"call-1","output":"ok"}]}`)
	warmed := repairResponsesWebsocketToolCallsWithCache(cache, sessionKey, cacheWarm)
	if gjson.GetBytes(warmed, "input.0.call_id").String() != "call-1" {
		t.Fatalf("expected warmup output to remain")
	}

	raw := []byte(`{"input":[{"type":"custom_tool_call","call_id":"call-1","name":"apply_patch"},{"type":"message","id":"msg-1"}]}`)
	repaired := repairResponsesWebsocketToolCallsWithCache(cache, sessionKey, raw)

	input := gjson.GetBytes(repaired, "input").Array()
	if len(input) != 3 {
		t.Fatalf("repaired input len = %d, want 3", len(input))
	}
	if input[0].Get("type").String() != "custom_tool_call" || input[0].Get("call_id").String() != "call-1" {
		t.Fatalf("unexpected first item: %s", input[0].Raw)
	}
	if input[1].Get("type").String() != "custom_tool_call_output" || input[1].Get("call_id").String() != "call-1" {
		t.Fatalf("missing inserted output: %s", input[1].Raw)
	}
	if input[2].Get("type").String() != "message" || input[2].Get("id").String() != "msg-1" {
		t.Fatalf("unexpected trailing item: %s", input[2].Raw)
	}
}

func TestRepairResponsesWebsocketToolCallsDropsOrphanCustomToolCall(t *testing.T) {
	cache := newWebsocketToolOutputCache(time.Minute, 10)
	sessionKey := "session-1"

	raw := []byte(`{"input":[{"type":"custom_tool_call","call_id":"call-1","name":"apply_patch"},{"type":"message","id":"msg-1"}]}`)
	repaired := repairResponsesWebsocketToolCallsWithCache(cache, sessionKey, raw)

	input := gjson.GetBytes(repaired, "input").Array()
	if len(input) != 1 {
		t.Fatalf("repaired input len = %d, want 1", len(input))
	}
	if input[0].Get("type").String() != "message" || input[0].Get("id").String() != "msg-1" {
		t.Fatalf("unexpected remaining item: %s", input[0].Raw)
	}
}

func TestRepairResponsesWebsocketToolCallsInsertsCachedCustomToolCallForOrphanOutput(t *testing.T) {
	outputCache := newWebsocketToolOutputCache(time.Minute, 10)
	callCache := newWebsocketToolOutputCache(time.Minute, 10)
	sessionKey := "session-1"

	callCache.record(sessionKey, "call-1", []byte(`{"type":"custom_tool_call","call_id":"call-1","name":"apply_patch"}`))

	raw := []byte(`{"input":[{"type":"custom_tool_call_output","call_id":"call-1","output":"ok"},{"type":"message","id":"msg-1"}]}`)
	repaired := repairResponsesWebsocketToolCallsWithCaches(outputCache, callCache, sessionKey, raw)

	input := gjson.GetBytes(repaired, "input").Array()
	if len(input) != 3 {
		t.Fatalf("repaired input len = %d, want 3", len(input))
	}
	if input[0].Get("type").String() != "custom_tool_call" || input[0].Get("call_id").String() != "call-1" {
		t.Fatalf("missing inserted call: %s", input[0].Raw)
	}
	if input[1].Get("type").String() != "custom_tool_call_output" || input[1].Get("call_id").String() != "call-1" {
		t.Fatalf("unexpected output item: %s", input[1].Raw)
	}
	if input[2].Get("type").String() != "message" || input[2].Get("id").String() != "msg-1" {
		t.Fatalf("unexpected trailing item: %s", input[2].Raw)
	}
}

func TestRepairResponsesWebsocketToolCallsDropsOrphanCustomToolOutputWhenCallMissing(t *testing.T) {
	outputCache := newWebsocketToolOutputCache(time.Minute, 10)
	callCache := newWebsocketToolOutputCache(time.Minute, 10)
	sessionKey := "session-1"

	raw := []byte(`{"input":[{"type":"custom_tool_call_output","call_id":"call-1","output":"ok"},{"type":"message","id":"msg-1"}]}`)
	repaired := repairResponsesWebsocketToolCallsWithCaches(outputCache, callCache, sessionKey, raw)

	input := gjson.GetBytes(repaired, "input").Array()
	if len(input) != 1 {
		t.Fatalf("repaired input len = %d, want 1", len(input))
	}
	if input[0].Get("type").String() != "message" || input[0].Get("id").String() != "msg-1" {
		t.Fatalf("unexpected remaining item: %s", input[0].Raw)
	}
}

func TestRecordResponsesWebsocketToolCallsFromPayloadWithCache(t *testing.T) {
	cache := newWebsocketToolOutputCache(time.Minute, 10)
	sessionKey := "session-1"

	payload := []byte(`{"type":"response.completed","response":{"id":"resp-1","output":[{"type":"function_call","id":"fc-1","call_id":"call-1","name":"tool","arguments":"{}"}]}}`)
	recordResponsesWebsocketToolCallsFromPayloadWithCache(cache, sessionKey, payload)

	cached, ok := cache.get(sessionKey, "call-1")
	if !ok {
		t.Fatalf("expected cached tool call")
	}
	if gjson.GetBytes(cached, "type").String() != "function_call" || gjson.GetBytes(cached, "call_id").String() != "call-1" {
		t.Fatalf("unexpected cached tool call: %s", cached)
	}
}

func TestRecordResponsesWebsocketCustomToolCallsFromCompletedPayloadWithCache(t *testing.T) {
	cache := newWebsocketToolOutputCache(time.Minute, 10)
	sessionKey := "session-1"

	payload := []byte(`{"type":"response.completed","response":{"id":"resp-1","output":[{"type":"custom_tool_call","id":"ctc-1","call_id":"call-1","name":"apply_patch","input":"*** Begin Patch"}]}}`)
	recordResponsesWebsocketToolCallsFromPayloadWithCache(cache, sessionKey, payload)

	cached, ok := cache.get(sessionKey, "call-1")
	if !ok {
		t.Fatalf("expected cached custom tool call")
	}
	if gjson.GetBytes(cached, "type").String() != "custom_tool_call" || gjson.GetBytes(cached, "call_id").String() != "call-1" {
		t.Fatalf("unexpected cached custom tool call: %s", cached)
	}
}

func TestRecordResponsesWebsocketCustomToolCallsFromOutputItemDoneWithCache(t *testing.T) {
	cache := newWebsocketToolOutputCache(time.Minute, 10)
	sessionKey := "session-1"

	payload := []byte(`{"type":"response.output_item.done","item":{"type":"custom_tool_call","id":"ctc-1","call_id":"call-1","name":"apply_patch","input":"*** Begin Patch"}}`)
	recordResponsesWebsocketToolCallsFromPayloadWithCache(cache, sessionKey, payload)

	cached, ok := cache.get(sessionKey, "call-1")
	if !ok {
		t.Fatalf("expected cached custom tool call")
	}
	if gjson.GetBytes(cached, "type").String() != "custom_tool_call" || gjson.GetBytes(cached, "call_id").String() != "call-1" {
		t.Fatalf("unexpected cached custom tool call: %s", cached)
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

		var timelineLog strings.Builder
		completedOutput, err := (*OpenAIResponsesAPIHandler)(nil).forwardResponsesWebsocket(
			ctx,
			conn,
			func(...interface{}) {},
			data,
			errCh,
			&timelineLog,
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
		if !strings.Contains(timelineLog.String(), "Event: websocket.response") {
			serverErrCh <- errors.New("websocket timeline did not capture downstream response")
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

func TestForwardResponsesWebsocketLogsAttemptedResponseOnWriteFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)

	serverErrCh := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := responsesWebsocketUpgrader.Upgrade(w, r, nil)
		if err != nil {
			serverErrCh <- err
			return
		}

		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Request = r

		data := make(chan []byte, 1)
		errCh := make(chan *interfaces.ErrorMessage)
		data <- []byte("data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-1\",\"output\":[{\"type\":\"message\",\"id\":\"out-1\"}]}}\n\n")
		close(data)
		close(errCh)

		var timelineLog strings.Builder
		if errClose := conn.Close(); errClose != nil {
			serverErrCh <- errClose
			return
		}

		_, err = (*OpenAIResponsesAPIHandler)(nil).forwardResponsesWebsocket(
			ctx,
			conn,
			func(...interface{}) {},
			data,
			errCh,
			&timelineLog,
			"session-1",
		)
		if err == nil {
			serverErrCh <- errors.New("expected websocket write failure")
			return
		}
		if !strings.Contains(timelineLog.String(), "Event: websocket.response") {
			serverErrCh <- errors.New("websocket timeline did not capture attempted downstream response")
			return
		}
		if !strings.Contains(timelineLog.String(), "\"type\":\"response.completed\"") {
			serverErrCh <- errors.New("websocket timeline did not retain attempted payload")
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
		_ = conn.Close()
	}()

	if errServer := <-serverErrCh; errServer != nil {
		t.Fatalf("server error: %v", errServer)
	}
}

func TestResponsesWebsocketTimelineRecordsDisconnectEvent(t *testing.T) {
	gin.SetMode(gin.TestMode)

	manager := coreauth.NewManager(nil, nil, nil)
	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager)
	h := NewOpenAIResponsesAPIHandler(base)

	timelineCh := make(chan string, 1)
	router := gin.New()
	router.GET("/v1/responses/ws", func(c *gin.Context) {
		h.ResponsesWebsocket(c)
		timeline := ""
		if value, exists := c.Get(wsTimelineBodyKey); exists {
			if body, ok := value.([]byte); ok {
				timeline = string(body)
			}
		}
		timelineCh <- timeline
	})

	server := httptest.NewServer(router)
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses/ws"
	conn, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatalf("dial websocket: %v", err)
	}

	closePayload := websocket.FormatCloseMessage(websocket.CloseGoingAway, "client closing")
	if err = conn.WriteControl(websocket.CloseMessage, closePayload, time.Now().Add(time.Second)); err != nil {
		t.Fatalf("write close control: %v", err)
	}
	_ = conn.Close()

	select {
	case timeline := <-timelineCh:
		if !strings.Contains(timeline, "Event: websocket.disconnect") {
			t.Fatalf("websocket timeline missing disconnect event: %s", timeline)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for websocket timeline")
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

func TestWebsocketClientAddressUsesGinClientIP(t *testing.T) {
	gin.SetMode(gin.TestMode)

	recorder := httptest.NewRecorder()
	c, engine := gin.CreateTestContext(recorder)
	if err := engine.SetTrustedProxies([]string{"0.0.0.0/0", "::/0"}); err != nil {
		t.Fatalf("SetTrustedProxies: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/responses/ws", nil)
	req.RemoteAddr = "172.18.0.1:34282"
	req.Header.Set("X-Forwarded-For", "203.0.113.7")
	c.Request = req

	if got := websocketClientAddress(c); got != strings.TrimSpace(c.ClientIP()) {
		t.Fatalf("websocketClientAddress = %q, ClientIP = %q", got, c.ClientIP())
	}
}

func TestWebsocketClientAddressReturnsEmptyForNilContext(t *testing.T) {
	if got := websocketClientAddress(nil); got != "" {
		t.Fatalf("websocketClientAddress(nil) = %q, want empty", got)
	}
}

func TestResponsesWebsocketPinsOnlyWebsocketCapableAuth(t *testing.T) {
	gin.SetMode(gin.TestMode)

	selector := &orderedWebsocketSelector{order: []string{"auth-sse", "auth-ws"}}
	executor := &websocketAuthCaptureExecutor{}
	manager := coreauth.NewManager(nil, selector, nil)
	manager.RegisterExecutor(executor)

	authSSE := &coreauth.Auth{ID: "auth-sse", Provider: executor.Identifier(), Status: coreauth.StatusActive}
	if _, err := manager.Register(context.Background(), authSSE); err != nil {
		t.Fatalf("Register SSE auth: %v", err)
	}
	authWS := &coreauth.Auth{
		ID:         "auth-ws",
		Provider:   executor.Identifier(),
		Status:     coreauth.StatusActive,
		Attributes: map[string]string{"websockets": "true"},
	}
	if _, err := manager.Register(context.Background(), authWS); err != nil {
		t.Fatalf("Register websocket auth: %v", err)
	}

	registry.GetGlobalRegistry().RegisterClient(authSSE.ID, authSSE.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	registry.GetGlobalRegistry().RegisterClient(authWS.ID, authWS.Provider, []*registry.ModelInfo{{ID: "test-model"}})
	t.Cleanup(func() {
		registry.GetGlobalRegistry().UnregisterClient(authSSE.ID)
		registry.GetGlobalRegistry().UnregisterClient(authWS.ID)
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

	requests := []string{
		`{"type":"response.create","model":"test-model","input":[{"type":"message","id":"msg-1"}]}`,
		`{"type":"response.create","input":[{"type":"message","id":"msg-2"}]}`,
	}
	for i := range requests {
		if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(requests[i])); errWrite != nil {
			t.Fatalf("write websocket message %d: %v", i+1, errWrite)
		}
		_, payload, errReadMessage := conn.ReadMessage()
		if errReadMessage != nil {
			t.Fatalf("read websocket message %d: %v", i+1, errReadMessage)
		}
		if got := gjson.GetBytes(payload, "type").String(); got != wsEventTypeCompleted {
			t.Fatalf("message %d payload type = %s, want %s", i+1, got, wsEventTypeCompleted)
		}
	}

	if got := executor.AuthIDs(); len(got) != 2 || got[0] != "auth-sse" || got[1] != "auth-ws" {
		t.Fatalf("selected auth IDs = %v, want [auth-sse auth-ws]", got)
	}
}

func TestNormalizeResponsesWebsocketRequestTreatsTranscriptReplacementAsReset(t *testing.T) {
	lastRequest := []byte(`{"model":"test-model","stream":true,"input":[{"type":"message","id":"msg-1"},{"type":"function_call","id":"fc-1","call_id":"call-1"},{"type":"function_call_output","id":"tool-out-1","call_id":"call-1"},{"type":"message","id":"assistant-1","role":"assistant"}]}`)
	lastResponseOutput := []byte(`[
		{"type":"message","id":"assistant-1","role":"assistant"}
	]`)
	raw := []byte(`{"type":"response.create","input":[{"type":"function_call","id":"fc-compact","call_id":"call-1","name":"tool"},{"type":"message","id":"msg-2"}]}`)

	normalized, next, errMsg := normalizeResponsesWebsocketRequest(raw, lastRequest, lastResponseOutput)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}
	if gjson.GetBytes(normalized, "previous_response_id").Exists() {
		t.Fatalf("previous_response_id must not exist in transcript replacement mode")
	}
	items := gjson.GetBytes(normalized, "input").Array()
	if len(items) != 2 {
		t.Fatalf("replacement input len = %d, want 2: %s", len(items), normalized)
	}
	if items[0].Get("id").String() != "fc-compact" || items[1].Get("id").String() != "msg-2" {
		t.Fatalf("replacement transcript was not preserved as-is: %s", normalized)
	}
	if !bytes.Equal(next, normalized) {
		t.Fatalf("next request snapshot should match replacement request")
	}
}

func TestNormalizeResponsesWebsocketRequestDoesNotTreatDeveloperMessageAsReplacement(t *testing.T) {
	lastRequest := []byte(`{"model":"test-model","stream":true,"input":[{"type":"message","id":"msg-1"}]}`)
	lastResponseOutput := []byte(`[
		{"type":"message","id":"assistant-1","role":"assistant"}
	]`)
	raw := []byte(`{"type":"response.create","input":[{"type":"message","id":"dev-1","role":"developer"},{"type":"message","id":"msg-2"}]}`)

	normalized, next, errMsg := normalizeResponsesWebsocketRequest(raw, lastRequest, lastResponseOutput)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}
	items := gjson.GetBytes(normalized, "input").Array()
	if len(items) != 4 {
		t.Fatalf("merged input len = %d, want 4: %s", len(items), normalized)
	}
	if items[0].Get("id").String() != "msg-1" ||
		items[1].Get("id").String() != "assistant-1" ||
		items[2].Get("id").String() != "dev-1" ||
		items[3].Get("id").String() != "msg-2" {
		t.Fatalf("developer follow-up should preserve merge behavior: %s", normalized)
	}
	if !bytes.Equal(next, normalized) {
		t.Fatalf("next request snapshot should match merged request")
	}
}

func TestNormalizeResponsesWebsocketRequestDropsDuplicateFunctionCallsByCallID(t *testing.T) {
	lastRequest := []byte(`{"model":"test-model","stream":true,"input":[{"type":"function_call","id":"fc-1","call_id":"call-1"},{"type":"function_call_output","id":"tool-out-1","call_id":"call-1"}]}`)
	lastResponseOutput := []byte(`[
		{"type":"function_call","id":"fc-1","call_id":"call-1","name":"tool"}
	]`)
	raw := []byte(`{"type":"response.create","input":[{"type":"message","id":"msg-2"}]}`)

	normalized, _, errMsg := normalizeResponsesWebsocketRequest(raw, lastRequest, lastResponseOutput)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}

	items := gjson.GetBytes(normalized, "input").Array()
	if len(items) != 3 {
		t.Fatalf("merged input len = %d, want 3: %s", len(items), normalized)
	}
	if items[0].Get("id").String() != "fc-1" ||
		items[1].Get("id").String() != "tool-out-1" ||
		items[2].Get("id").String() != "msg-2" {
		t.Fatalf("unexpected merged input order: %s", normalized)
	}
}

func TestNormalizeResponsesWebsocketRequestTreatsCustomToolTranscriptReplacementAsReset(t *testing.T) {
	lastRequest := []byte(`{"model":"test-model","stream":true,"input":[{"type":"message","id":"msg-1"},{"type":"custom_tool_call","id":"ctc-1","call_id":"call-1","name":"apply_patch"},{"type":"custom_tool_call_output","id":"tool-out-1","call_id":"call-1"},{"type":"message","id":"assistant-1","role":"assistant"}]}`)
	lastResponseOutput := []byte(`[
		{"type":"message","id":"assistant-1","role":"assistant"}
	]`)
	raw := []byte(`{"type":"response.create","input":[{"type":"custom_tool_call","id":"ctc-compact","call_id":"call-1","name":"apply_patch"},{"type":"custom_tool_call_output","id":"tool-out-compact","call_id":"call-1"},{"type":"message","id":"msg-2"}]}`)

	normalized, next, errMsg := normalizeResponsesWebsocketRequest(raw, lastRequest, lastResponseOutput)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}
	if gjson.GetBytes(normalized, "previous_response_id").Exists() {
		t.Fatalf("previous_response_id must not exist in transcript replacement mode")
	}
	items := gjson.GetBytes(normalized, "input").Array()
	if len(items) != 3 {
		t.Fatalf("replacement input len = %d, want 3: %s", len(items), normalized)
	}
	if items[0].Get("id").String() != "ctc-compact" ||
		items[1].Get("id").String() != "tool-out-compact" ||
		items[2].Get("id").String() != "msg-2" {
		t.Fatalf("replacement transcript was not preserved as-is: %s", normalized)
	}
	if !bytes.Equal(next, normalized) {
		t.Fatalf("next request snapshot should match replacement request")
	}
}

func TestNormalizeResponsesWebsocketRequestDropsDuplicateCustomToolCallsByCallID(t *testing.T) {
	lastRequest := []byte(`{"model":"test-model","stream":true,"input":[{"type":"custom_tool_call","id":"ctc-1","call_id":"call-1","name":"apply_patch"},{"type":"custom_tool_call_output","id":"tool-out-1","call_id":"call-1"}]}`)
	lastResponseOutput := []byte(`[
		{"type":"custom_tool_call","id":"ctc-1","call_id":"call-1","name":"apply_patch"}
	]`)
	raw := []byte(`{"type":"response.create","input":[{"type":"message","id":"msg-2"}]}`)

	normalized, _, errMsg := normalizeResponsesWebsocketRequest(raw, lastRequest, lastResponseOutput)
	if errMsg != nil {
		t.Fatalf("unexpected error: %v", errMsg.Error)
	}

	items := gjson.GetBytes(normalized, "input").Array()
	if len(items) != 3 {
		t.Fatalf("merged input len = %d, want 3: %s", len(items), normalized)
	}
	if items[0].Get("id").String() != "ctc-1" ||
		items[1].Get("id").String() != "tool-out-1" ||
		items[2].Get("id").String() != "msg-2" {
		t.Fatalf("unexpected merged input order: %s", normalized)
	}
}

func TestResponsesWebsocketCompactionResetsTurnStateOnCustomToolTranscriptReplacement(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &websocketCompactionCaptureExecutor{}
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
	router.POST("/v1/responses/compact", h.Compact)

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

	requests := []string{
		`{"type":"response.create","model":"test-model","input":[{"type":"message","id":"msg-1"}]}`,
		`{"type":"response.create","input":[{"type":"custom_tool_call_output","call_id":"call-1","id":"tool-out-1"}]}`,
	}
	for i := range requests {
		if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(requests[i])); errWrite != nil {
			t.Fatalf("write websocket message %d: %v", i+1, errWrite)
		}
		_, payload, errReadMessage := conn.ReadMessage()
		if errReadMessage != nil {
			t.Fatalf("read websocket message %d: %v", i+1, errReadMessage)
		}
		if got := gjson.GetBytes(payload, "type").String(); got != wsEventTypeCompleted {
			t.Fatalf("message %d payload type = %s, want %s", i+1, got, wsEventTypeCompleted)
		}
	}

	compactResp, errPost := server.Client().Post(
		server.URL+"/v1/responses/compact",
		"application/json",
		strings.NewReader(`{"model":"test-model","input":[{"type":"message","id":"summary-1"}]}`),
	)
	if errPost != nil {
		t.Fatalf("compact request failed: %v", errPost)
	}
	if errClose := compactResp.Body.Close(); errClose != nil {
		t.Fatalf("close compact response body: %v", errClose)
	}
	if compactResp.StatusCode != http.StatusOK {
		t.Fatalf("compact status = %d, want %d", compactResp.StatusCode, http.StatusOK)
	}

	postCompact := `{"type":"response.create","input":[{"type":"custom_tool_call","id":"ctc-compact","call_id":"call-1","name":"apply_patch"},{"type":"custom_tool_call_output","id":"tool-out-compact","call_id":"call-1"},{"type":"message","id":"msg-2"}]}`
	if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(postCompact)); errWrite != nil {
		t.Fatalf("write post-compact websocket message: %v", errWrite)
	}
	_, payload, errReadMessage := conn.ReadMessage()
	if errReadMessage != nil {
		t.Fatalf("read post-compact websocket message: %v", errReadMessage)
	}
	if got := gjson.GetBytes(payload, "type").String(); got != wsEventTypeCompleted {
		t.Fatalf("post-compact payload type = %s, want %s", got, wsEventTypeCompleted)
	}

	executor.mu.Lock()
	defer executor.mu.Unlock()

	if executor.compactPayload == nil {
		t.Fatalf("compact payload was not captured")
	}
	if len(executor.streamPayloads) != 3 {
		t.Fatalf("stream payload count = %d, want 3", len(executor.streamPayloads))
	}

	merged := executor.streamPayloads[2]
	items := gjson.GetBytes(merged, "input").Array()
	if len(items) != 3 {
		t.Fatalf("merged input len = %d, want 3: %s", len(items), merged)
	}
	if items[0].Get("id").String() != "ctc-compact" ||
		items[1].Get("id").String() != "tool-out-compact" ||
		items[2].Get("id").String() != "msg-2" {
		t.Fatalf("unexpected post-compact input order: %s", merged)
	}
	if items[0].Get("call_id").String() != "call-1" {
		t.Fatalf("post-compact custom tool call id = %s, want call-1", items[0].Get("call_id").String())
	}
}

func TestResponsesWebsocketCompactionResetsTurnStateOnTranscriptReplacement(t *testing.T) {
	gin.SetMode(gin.TestMode)

	executor := &websocketCompactionCaptureExecutor{}
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
	router.POST("/v1/responses/compact", h.Compact)

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

	requests := []string{
		`{"type":"response.create","model":"test-model","input":[{"type":"message","id":"msg-1"}]}`,
		`{"type":"response.create","input":[{"type":"function_call_output","call_id":"call-1","id":"tool-out-1"}]}`,
	}
	for i := range requests {
		if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(requests[i])); errWrite != nil {
			t.Fatalf("write websocket message %d: %v", i+1, errWrite)
		}
		_, payload, errReadMessage := conn.ReadMessage()
		if errReadMessage != nil {
			t.Fatalf("read websocket message %d: %v", i+1, errReadMessage)
		}
		if got := gjson.GetBytes(payload, "type").String(); got != wsEventTypeCompleted {
			t.Fatalf("message %d payload type = %s, want %s", i+1, got, wsEventTypeCompleted)
		}
	}

	compactResp, errPost := server.Client().Post(
		server.URL+"/v1/responses/compact",
		"application/json",
		strings.NewReader(`{"model":"test-model","input":[{"type":"message","id":"summary-1"}]}`),
	)
	if errPost != nil {
		t.Fatalf("compact request failed: %v", errPost)
	}
	if errClose := compactResp.Body.Close(); errClose != nil {
		t.Fatalf("close compact response body: %v", errClose)
	}
	if compactResp.StatusCode != http.StatusOK {
		t.Fatalf("compact status = %d, want %d", compactResp.StatusCode, http.StatusOK)
	}

	// Simulate a post-compaction client turn that replaces local history with a compacted transcript.
	// The websocket handler must treat this as a state reset, not append it to stale pre-compaction state.
	postCompact := `{"type":"response.create","input":[{"type":"function_call","id":"fc-compact","call_id":"call-1","name":"tool"},{"type":"message","id":"msg-2"}]}`
	if errWrite := conn.WriteMessage(websocket.TextMessage, []byte(postCompact)); errWrite != nil {
		t.Fatalf("write post-compact websocket message: %v", errWrite)
	}
	_, payload, errReadMessage := conn.ReadMessage()
	if errReadMessage != nil {
		t.Fatalf("read post-compact websocket message: %v", errReadMessage)
	}
	if got := gjson.GetBytes(payload, "type").String(); got != wsEventTypeCompleted {
		t.Fatalf("post-compact payload type = %s, want %s", got, wsEventTypeCompleted)
	}

	executor.mu.Lock()
	defer executor.mu.Unlock()

	if executor.compactPayload == nil {
		t.Fatalf("compact payload was not captured")
	}
	if len(executor.streamPayloads) != 3 {
		t.Fatalf("stream payload count = %d, want 3", len(executor.streamPayloads))
	}

	merged := executor.streamPayloads[2]
	items := gjson.GetBytes(merged, "input").Array()
	if len(items) != 2 {
		t.Fatalf("merged input len = %d, want 2: %s", len(items), merged)
	}
	if items[0].Get("id").String() != "fc-compact" ||
		items[1].Get("id").String() != "msg-2" {
		t.Fatalf("unexpected post-compact input order: %s", merged)
	}
	if items[0].Get("call_id").String() != "call-1" {
		t.Fatalf("post-compact function call id = %s, want call-1", items[0].Get("call_id").String())
	}
}
