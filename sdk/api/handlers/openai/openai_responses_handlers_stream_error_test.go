package openai

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/interfaces"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/api/handlers"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v7/sdk/config"
	"github.com/tidwall/gjson"
)

func TestForwardResponsesStreamTerminalErrorUsesResponseFailedEvent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
	h := NewOpenAIResponsesAPIHandler(base)

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		t.Fatalf("expected gin writer to implement http.Flusher")
	}

	framer := &responsesSSEFramer{requestJSON: []byte(`{"model":"gpt-test","instructions":"test instructions","metadata":{"source":"test"},"parallel_tool_calls":true,"temperature":0.5,"tool_choice":"auto","tools":[],"top_p":0.9,"text":{"format":{"type":"text"}},"max_output_tokens":128,"conversation":"conv_test"}`)}
	framer.WriteChunk(c.Writer, []byte("event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":4,\"response\":{\"id\":\"resp_test\",\"object\":\"response\",\"created_at\":123,\"status\":\"in_progress\",\"background\":false,\"error\":null,\"output\":[]}}\n\n"))
	framer.WriteChunk(c.Writer, []byte("event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"sequence_number\":5,\"output_index\":0,\"item\":{\"id\":\"msg_test\",\"type\":\"message\",\"status\":\"completed\",\"role\":\"assistant\",\"content\":[]}}\n\n"))

	data := make(chan []byte)
	errs := make(chan *interfaces.ErrorMessage, 1)
	errs <- &interfaces.ErrorMessage{StatusCode: http.StatusInternalServerError, Error: errors.New("unexpected EOF")}
	close(errs)

	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, framer)
	body := recorder.Body.String()
	if !strings.Contains(body, "event: response.failed\n") {
		t.Fatalf("expected response.failed terminal event, got: %q", body)
	}
	if !strings.Contains(body, `"type":"response.failed"`) {
		t.Fatalf("expected response.failed payload, got: %q", body)
	}
	if strings.Contains(body, "event: error\n") {
		t.Fatalf("unexpected generic error event: %q", body)
	}
	failedAt := strings.Index(body, "event: response.failed\n")
	payload, ok := responsesSSEDataPayload([]byte(body[failedAt:]))
	if !ok {
		t.Fatalf("expected response.failed data payload, got: %q", body)
	}
	var responseFields map[string]json.RawMessage
	responsePayload := gjson.GetBytes(payload, "response")
	if err := json.Unmarshal([]byte(responsePayload.Raw), &responseFields); err != nil {
		t.Fatalf("unmarshal response: %v; payload=%s", err, payload)
	}
	for _, field := range []string{"id", "created_at", "error", "incomplete_details", "instructions", "metadata", "model", "object", "output", "parallel_tool_calls", "temperature", "tool_choice", "tools", "top_p", "usage", "store"} {
		if _, exists := responseFields[field]; !exists {
			t.Fatalf("required response field %q is missing; payload=%s", field, payload)
		}
	}
	if _, exists := responseFields["output_text"]; exists {
		t.Fatalf("non-native output_text field was added; payload=%s", payload)
	}
	if got := gjson.GetBytes(payload, "response.id").String(); got != "resp_test" {
		t.Fatalf("response id = %q, want %q; payload=%s", got, "resp_test", payload)
	}
	if got := gjson.GetBytes(payload, "response.error.code").String(); got != "server_error" {
		t.Fatalf("error code = %q, want %q; payload=%s", got, "server_error", payload)
	}
	if got := gjson.GetBytes(payload, "sequence_number").Int(); got != 6 {
		t.Fatalf("sequence_number = %d, want 6; payload=%s", got, payload)
	}
	if got := gjson.GetBytes(payload, "response.created_at").Int(); got != 123 {
		t.Fatalf("created_at = %d, want 123; payload=%s", got, payload)
	}
	if got := gjson.GetBytes(payload, "response.model").String(); got != "gpt-test" {
		t.Fatalf("model = %q, want %q; payload=%s", got, "gpt-test", payload)
	}
	if !gjson.GetBytes(payload, "response.parallel_tool_calls").Bool() {
		t.Fatalf("parallel_tool_calls was not preserved; payload=%s", payload)
	}
	if got := gjson.GetBytes(payload, "response.tool_choice").String(); got != "auto" {
		t.Fatalf("tool_choice = %q, want %q; payload=%s", got, "auto", payload)
	}
	if !gjson.GetBytes(payload, "response.tools").IsArray() || !gjson.GetBytes(payload, "response.text").IsObject() {
		t.Fatalf("response schema fields were not populated; payload=%s", payload)
	}
	if got := gjson.GetBytes(payload, "response.output.0.id").String(); got != "msg_test" {
		t.Fatalf("output item id = %q, want %q; payload=%s", got, "msg_test", payload)
	}
	if got := gjson.GetBytes(payload, "response.instructions").String(); got != "test instructions" {
		t.Fatalf("instructions = %q, want %q; payload=%s", got, "test instructions", payload)
	}
	if got := gjson.GetBytes(payload, "response.conversation.id").String(); got != "conv_test" {
		t.Fatalf("conversation id = %q, want conv_test; payload=%s", got, payload)
	}
	if got := gjson.GetBytes(payload, "response.temperature").Float(); got != 0.5 {
		t.Fatalf("temperature = %v, want 0.5; payload=%s", got, payload)
	}
	if !gjson.GetBytes(payload, "response.store").Bool() {
		t.Fatalf("store default was not populated; payload=%s", payload)
	}
	if got := gjson.GetBytes(payload, "response.max_output_tokens").Int(); got != 128 {
		t.Fatalf("max_output_tokens = %d, want 128; payload=%s", got, payload)
	}
	if gjson.GetBytes(payload, "response.service_tier").Exists() {
		t.Fatalf("optional service_tier was synthesized; payload=%s", payload)
	}
	if got := gjson.GetBytes(payload, "response.top_p").Float(); got != 0.9 {
		t.Fatalf("top_p = %v, want 0.9; payload=%s", got, payload)
	}
}

func TestResponsesSSEFramerFailedResponseStorePrecedence(t *testing.T) {
	tests := []struct {
		name           string
		requestJSON    string
		responseObject string
		want           bool
	}{
		{name: "api default", requestJSON: `{"model":"gpt-test"}`, responseObject: `{"id":"resp_test"}`, want: true},
		{name: "null uses default", requestJSON: `{"model":"gpt-test","store":null}`, responseObject: `{"id":"resp_test"}`, want: true},
		{name: "request false", requestJSON: `{"model":"gpt-test","store":false}`, responseObject: `{"id":"resp_test"}`, want: false},
		{name: "request true", requestJSON: `{"model":"gpt-test","store":true}`, responseObject: `{"id":"resp_test"}`, want: true},
		{name: "response wins", requestJSON: `{"model":"gpt-test","store":true}`, responseObject: `{"id":"resp_test","store":false}`, want: false},
		{name: "invalid response uses request", requestJSON: `{"model":"gpt-test","store":false}`, responseObject: `{"id":"resp_test","store":null}`, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			framer := &responsesSSEFramer{
				responseID:     "resp_test",
				responseObject: []byte(test.responseObject),
				requestJSON:    []byte(test.requestJSON),
			}
			response := framer.failedResponseObject()
			store := gjson.GetBytes(response, "store")
			if store.Type != gjson.True && store.Type != gjson.False {
				t.Fatalf("store = %s, want boolean; response=%s", store.Raw, response)
			}
			if got := store.Bool(); got != test.want {
				t.Fatalf("store = %v, want %v; response=%s", got, test.want, response)
			}
		})
	}
}

func TestResponsesSSEFramerFailedResponseParallelToolCallsPrecedence(t *testing.T) {
	tests := []struct {
		name           string
		requestJSON    string
		responseObject string
		want           bool
	}{
		{name: "api default", requestJSON: `{"model":"gpt-test"}`, responseObject: `{"id":"resp_test"}`, want: true},
		{name: "null uses default", requestJSON: `{"model":"gpt-test","parallel_tool_calls":null}`, responseObject: `{"id":"resp_test"}`, want: true},
		{name: "request false", requestJSON: `{"model":"gpt-test","parallel_tool_calls":false}`, responseObject: `{"id":"resp_test"}`, want: false},
		{name: "request true", requestJSON: `{"model":"gpt-test","parallel_tool_calls":true}`, responseObject: `{"id":"resp_test"}`, want: true},
		{name: "response wins", requestJSON: `{"model":"gpt-test","parallel_tool_calls":true}`, responseObject: `{"id":"resp_test","parallel_tool_calls":false}`, want: false},
		{name: "invalid response uses request", requestJSON: `{"model":"gpt-test","parallel_tool_calls":false}`, responseObject: `{"id":"resp_test","parallel_tool_calls":null}`, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			framer := &responsesSSEFramer{
				responseID:     "resp_test",
				responseObject: []byte(test.responseObject),
				requestJSON:    []byte(test.requestJSON),
			}
			response := framer.failedResponseObject()
			parallelToolCalls := gjson.GetBytes(response, "parallel_tool_calls")
			if parallelToolCalls.Type != gjson.True && parallelToolCalls.Type != gjson.False {
				t.Fatalf("parallel_tool_calls = %s, want boolean; response=%s", parallelToolCalls.Raw, response)
			}
			if got := parallelToolCalls.Bool(); got != test.want {
				t.Fatalf("parallel_tool_calls = %v, want %v; response=%s", got, test.want, response)
			}
		})
	}
}

func TestResponsesSSEFramerFailedResponseConversationNormalization(t *testing.T) {
	tests := []struct {
		name           string
		requestJSON    string
		responseObject string
		wantID         string
		wantNull       bool
	}{
		{name: "absent", requestJSON: `{"model":"gpt-test"}`, responseObject: `{"id":"resp_test"}`},
		{name: "request id string", requestJSON: `{"model":"gpt-test","conversation":"conv_string"}`, responseObject: `{"id":"resp_test"}`, wantID: "conv_string"},
		{name: "request object", requestJSON: `{"model":"gpt-test","conversation":{"id":"conv_object","extra":"drop"}}`, responseObject: `{"id":"resp_test"}`, wantID: "conv_object"},
		{name: "response wins", requestJSON: `{"model":"gpt-test","conversation":"conv_request"}`, responseObject: `{"id":"resp_test","conversation":{"id":"conv_response"}}`, wantID: "conv_response"},
		{name: "response null wins", requestJSON: `{"model":"gpt-test","conversation":"conv_request"}`, responseObject: `{"id":"resp_test","conversation":null}`, wantNull: true},
		{name: "invalid request omitted", requestJSON: `{"model":"gpt-test","conversation":false}`, responseObject: `{"id":"resp_test"}`},
		{name: "numeric request id omitted", requestJSON: `{"model":"gpt-test","conversation":{"id":123}}`, responseObject: `{"id":"resp_test"}`},
		{name: "invalid response uses request", requestJSON: `{"model":"gpt-test","conversation":"conv_request"}`, responseObject: `{"id":"resp_test","conversation":{"id":123}}`, wantID: "conv_request"},
		{name: "invalid response omitted", requestJSON: `{"model":"gpt-test"}`, responseObject: `{"id":"resp_test","conversation":false}`},
		{name: "invalid response and request omitted", requestJSON: `{"model":"gpt-test","conversation":{"id":123}}`, responseObject: `{"id":"resp_test","conversation":{"id":456}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			framer := &responsesSSEFramer{
				responseID:     "resp_test",
				responseObject: []byte(test.responseObject),
				requestJSON:    []byte(test.requestJSON),
			}
			response := framer.failedResponseObject()
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(response, &fields); err != nil {
				t.Fatalf("unmarshal response: %v; response=%s", err, response)
			}
			conversationRaw, exists := fields["conversation"]
			if test.wantNull {
				if !exists || string(conversationRaw) != "null" {
					t.Fatalf("conversation = %s, want null; response=%s", conversationRaw, response)
				}
				return
			}
			if test.wantID == "" {
				if exists {
					t.Fatalf("conversation = %s, want omitted; response=%s", conversationRaw, response)
				}
				return
			}
			conversation := gjson.GetBytes(response, "conversation")
			if !conversation.IsObject() {
				t.Fatalf("conversation = %s, want object; response=%s", conversation.Raw, response)
			}
			if got := conversation.Get("id").String(); got != test.wantID {
				t.Fatalf("conversation id = %q, want %q; response=%s", got, test.wantID, response)
			}
			if conversation.Get("extra").Exists() {
				t.Fatalf("request-only conversation fields were preserved; response=%s", response)
			}
		})
	}
}

func TestForwardResponsesStreamTerminalErrorPreservesMixedOutputOrder(t *testing.T) {
	h, recorder, c, flusher := newResponsesStreamTestHandler(t)
	framer := &responsesSSEFramer{requestJSON: []byte(`{"model":"gpt-test","store":false}`)}
	framer.WriteChunk(c.Writer, []byte("event: response.created\ndata: {\"type\":\"response.created\",\"sequence_number\":1,\"response\":{\"id\":\"resp_test\",\"output\":[]}}\n\n"))
	framer.WriteChunk(c.Writer, []byte("event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"sequence_number\":2,\"item\":{\"type\":\"message\",\"id\":\"msg-1\",\"role\":\"assistant\",\"content\":[{\"type\":\"output_text\",\"text\":\"done\"}]}}\n\n"))
	framer.WriteChunk(c.Writer, []byte("event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"sequence_number\":3,\"output_index\":1,\"item\":{\"type\":\"function_call\",\"id\":\"fc-1\",\"call_id\":\"call-1\",\"name\":\"shell\",\"arguments\":\"{}\",\"status\":\"completed\"}}\n\n"))

	data := make(chan []byte)
	close(data)
	errs := make(chan *interfaces.ErrorMessage, 1)
	errs <- &interfaces.ErrorMessage{StatusCode: http.StatusInternalServerError, Error: errors.New("unexpected EOF")}
	close(errs)
	h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, framer)

	body := recorder.Body.String()
	failedAt := strings.LastIndex(body, "event: response.failed\n")
	if failedAt < 0 {
		t.Fatalf("expected response.failed event; body=%q", body)
	}
	payload, ok := responsesSSEDataPayload([]byte(body[failedAt:]))
	if !ok {
		t.Fatalf("expected response.failed data payload; body=%q", body)
	}
	output := gjson.GetBytes(payload, "response.output")
	if !output.IsArray() || len(output.Array()) != 2 {
		t.Fatalf("output = %s, want 2 items; payload=%s", output.Raw, payload)
	}
	if got := gjson.GetBytes(payload, "response.output.0.id").String(); got != "fc-1" {
		t.Fatalf("first output id = %q, want fc-1; payload=%s", got, payload)
	}
	if got := gjson.GetBytes(payload, "response.output.1.id").String(); got != "msg-1" {
		t.Fatalf("second output id = %q, want msg-1; payload=%s", got, payload)
	}
	if got := gjson.GetBytes(payload, "sequence_number").Int(); got != 4 {
		t.Fatalf("sequence_number = %d, want 4; payload=%s", got, payload)
	}
}

func TestForwardResponsesStreamTerminalErrorDoesNotDuplicateTerminalEvent(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, terminalType := range []string{"response.completed", "response.incomplete", "response.failed"} {
		t.Run(terminalType, func(t *testing.T) {
			base := handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, nil)
			h := NewOpenAIResponsesAPIHandler(base)

			recorder := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(recorder)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			flusher, ok := c.Writer.(http.Flusher)
			if !ok {
				t.Fatalf("expected gin writer to implement http.Flusher")
			}

			status := strings.TrimPrefix(terminalType, "response.")
			framer := &responsesSSEFramer{}
			framer.WriteChunk(c.Writer, []byte("event: "+terminalType+"\ndata: {\"type\":\""+terminalType+"\",\"sequence_number\":4,\"response\":{\"id\":\"resp_test\",\"status\":\""+status+"\"}}\n\n"))

			data := make(chan []byte)
			errs := make(chan *interfaces.ErrorMessage, 1)
			errs <- &interfaces.ErrorMessage{StatusCode: http.StatusInternalServerError, Error: errors.New("unexpected EOF")}
			close(errs)

			h.forwardResponsesStream(c, flusher, func(error) {}, data, errs, framer)
			body := recorder.Body.String()
			terminalCount := 0
			for _, candidate := range []string{"response.completed", "response.incomplete", "response.failed"} {
				terminalCount += strings.Count(body, `"type":"`+candidate+`"`)
			}
			if terminalCount != 1 {
				t.Fatalf("terminal event count = %d, want 1; body=%q", terminalCount, body)
			}
		})
	}
}
