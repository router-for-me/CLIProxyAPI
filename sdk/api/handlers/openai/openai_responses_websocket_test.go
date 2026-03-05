package openai

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

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
