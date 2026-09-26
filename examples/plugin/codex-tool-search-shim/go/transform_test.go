package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestRewriteRequestToolDeclarations(t *testing.T) {
	input := []byte(`{
		"tools": [
			{"type":"tool_search","execution":"client","description":"search deferred tools","parameters":{"type":"object","properties":{"query":{"type":"string"}}}},
			{"type":"function","name":"exec_command","parameters":{}},
			{"type":"namespace","name":"codex_apps","tools":[
				{"type":"tool_search","execution":"client","description":"namespace search","parameters":{"type":"object"}}
			]}
		],
		"input": [
			{"type":"additional_tools","tools":[
				{"type":"tool_search","execution":"client","description":"additional search","parameters":{"type":"object"}}
			]}
		]
	}`)

	out, changed, errRewrite := rewriteRequestBody(input)
	if errRewrite != nil {
		t.Fatalf("rewriteRequestBody() error = %v", errRewrite)
	}
	if !changed {
		t.Fatal("rewriteRequestBody() changed = false")
	}

	var request struct {
		Tools []map[string]any `json:"tools"`
		Input []map[string]any `json:"input"`
	}
	if errUnmarshal := json.Unmarshal(out, &request); errUnmarshal != nil {
		t.Fatalf("json.Unmarshal() error = %v", errUnmarshal)
	}
	validateUpstreamToolSearchDeclaration(t, request.Tools[0], "top-level")
	if request.Tools[1]["type"] != "function" || request.Tools[1]["name"] != "exec_command" {
		t.Fatalf("ordinary tool changed: %#v", request.Tools[1])
	}
	if _, exists := request.Tools[2]["tools"]; exists {
		t.Fatalf("namespace children were not pruned: %#v", request.Tools[2])
	}
	namespaceDeferred, ok := request.Tools[2][deferredToolsKey].([]any)
	if !ok || len(namespaceDeferred) != 1 {
		t.Fatalf("deferred namespace metadata = %#v", request.Tools[2][deferredToolsKey])
	}
	additionalTools, ok := request.Input[0]["tools"].([]any)
	if !ok || len(additionalTools) != 1 {
		t.Fatalf("additional tools = %#v", request.Input[0]["tools"])
	}
	validateUpstreamToolSearchDeclaration(t, additionalTools[0].(map[string]any), "additional")
}

func TestRewriteRequestSearchHistory(t *testing.T) {
	input := []byte(`{
		"input": [
			{"type":"tool_search_call","call_id":"search-1","execution":"client","status":"completed","arguments":{"query":"slack message send"}},
			{"type":"tool_search_output","call_id":"search-1","execution":"client","status":"completed","tools":[
				{"type":"function","name":"mcp__codex_apps__slack___slack_send_message","parameters":{"type":"object"}}
			]},
			{"type":"function_call","call_id":"exec-1","name":"exec_command","arguments":"{}"},
			{"type":"tool_search_call","call_id":"server-search","execution":"server","arguments":{"query":"server"}}
		]
	}`)

	out, changed, errRewrite := rewriteRequestBody(input)
	if errRewrite != nil {
		t.Fatalf("rewriteRequestBody() error = %v", errRewrite)
	}
	if !changed {
		t.Fatal("rewriteRequestBody() changed = false")
	}

	var request struct {
		Input []map[string]any `json:"input"`
	}
	if errUnmarshal := json.Unmarshal(out, &request); errUnmarshal != nil {
		t.Fatalf("json.Unmarshal() error = %v", errUnmarshal)
	}
	call := request.Input[0]
	if call["type"] != "function_call" || call["name"] != "tool_search" || call["execution"] != nil {
		t.Fatalf("search call = %#v", call)
	}
	arguments, ok := call["arguments"].(string)
	if !ok || !strings.Contains(arguments, "slack message send") {
		t.Fatalf("search call arguments = %#v", call["arguments"])
	}
	output := request.Input[1]
	if output["type"] != "function_call_output" || output["execution"] != nil || output["status"] != nil {
		t.Fatalf("search output = %#v", output)
	}
	encodedTools, ok := output["output"].(string)
	if !ok || !strings.Contains(encodedTools, "slack___slack_send_message") {
		t.Fatalf("search output payload = %#v", output["output"])
	}
	if request.Input[2]["type"] != "function_call" || request.Input[2]["name"] != "exec_command" {
		t.Fatalf("ordinary call changed: %#v", request.Input[2])
	}
	if request.Input[3]["type"] != "tool_search_call" || request.Input[3]["execution"] != "server" {
		t.Fatalf("server search call changed: %#v", request.Input[3])
	}
}

func TestRewriteRequestBodyIsIdempotent(t *testing.T) {
	input := []byte(`{
		"tools":[{"type":"tool_search","execution":"client","description":"search","parameters":{"type":"object"}}],
		"input":[
			{"type":"tool_search_call","call_id":"search-1","execution":"client","arguments":{"query":"github"}},
			{"type":"tool_search_output","call_id":"search-1","execution":"client","tools":[]}
		]
	}`)

	first, changedFirst, errFirst := rewriteRequestBody(input)
	if errFirst != nil || !changedFirst {
		t.Fatalf("first rewrite = (%v, %v), want changed", changedFirst, errFirst)
	}
	second, changedSecond, errSecond := rewriteRequestBody(first)
	if errSecond != nil {
		t.Fatalf("second rewrite error = %v", errSecond)
	}
	if changedSecond {
		t.Fatal("second rewrite changed = true")
	}
	if string(second) != string(first) {
		t.Fatalf("second rewrite changed bytes: %s", second)
	}
}

func TestRewriteNonStreamingResponse(t *testing.T) {
	input := []byte(`{
		"output": [
			{"type":"message","role":"assistant","content":[]},
			{"type":"function_call","call_id":"call-1","name":"tool_search","arguments":"{\"query\":\"slack\"}"},
			{"type":"function_call","call_id":"call-2","name":"exec_command","arguments":"{}"}
		]
	}`)

	out, changed, errRewrite := rewriteResponseBody(input)
	if errRewrite != nil {
		t.Fatalf("rewriteResponseBody() error = %v", errRewrite)
	}
	if !changed {
		t.Fatal("rewriteResponseBody() changed = false")
	}

	var response struct {
		Output []map[string]any `json:"output"`
	}
	if errUnmarshal := json.Unmarshal(out, &response); errUnmarshal != nil {
		t.Fatalf("json.Unmarshal() error = %v", errUnmarshal)
	}
	if errValidate := validateToolSearchCall(response.Output[1]); errValidate != nil {
		t.Fatalf("validateToolSearchCall() error = %v", errValidate)
	}
	if response.Output[1]["call_id"] != "call-1" {
		t.Fatalf("call_id = %v", response.Output[1]["call_id"])
	}
	if _, exists := response.Output[1]["name"]; exists {
		t.Fatal("converted item still contains name")
	}
	if response.Output[2]["type"] != "function_call" || response.Output[2]["name"] != "exec_command" {
		t.Fatalf("ordinary function call changed: %#v", response.Output[2])
	}
}

func TestRewriteStreamingOutputItemDone(t *testing.T) {
	input := []byte("event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"call_id\":\"search-1\",\"name\":\"tool_search\",\"arguments\":{\"query\":\"github pull request\"}}}\n\ndata: [DONE]\n\n")

	out, changed, errRewrite := rewriteResponseBody(input)
	if errRewrite != nil {
		t.Fatalf("rewriteResponseBody() error = %v", errRewrite)
	}
	if !changed {
		t.Fatal("rewriteResponseBody() changed = false")
	}
	text := string(out)
	if !strings.Contains(text, `"type":"tool_search_call"`) ||
		!strings.Contains(text, `"execution":"client"`) ||
		strings.Contains(text, `"name":"tool_search"`) {
		t.Fatalf("unexpected SSE output: %s", text)
	}
	if !strings.Contains(text, "event: response.output_item.done\n") ||
		!strings.Contains(text, "data: [DONE]\n\n") {
		t.Fatalf("SSE framing changed: %s", text)
	}
}

func TestRewritePreservesNativeToolSearchCall(t *testing.T) {
	input := []byte(`{"type":"tool_search_call","call_id":"native","execution":"client","arguments":{"query":"slack"}}`)
	out, changed, errRewrite := rewriteResponseBody(input)
	if errRewrite != nil {
		t.Fatalf("rewriteResponseBody() error = %v", errRewrite)
	}
	if changed {
		t.Fatal("native tool_search_call changed")
	}
	if string(out) != string(input) {
		t.Fatalf("native body changed: %s", out)
	}
}

func TestRewriteLeavesUnrelatedAndMalformedBodiesUnchanged(t *testing.T) {
	bodies := [][]byte{
		[]byte(`{"type":"message","content":[]}`),
		[]byte(`not-json`),
		[]byte(""),
	}
	for _, input := range bodies {
		out, changed, errRewrite := rewriteResponseBody(input)
		if errRewrite != nil {
			t.Fatalf("rewriteResponseBody(%q) error = %v", input, errRewrite)
		}
		if changed || string(out) != string(input) {
			t.Fatalf("rewriteResponseBody(%q) = (%q, %v), want unchanged", input, out, changed)
		}
	}
}

func TestRegistrationDeclaresAllInterceptors(t *testing.T) {
	registration := currentRegistration()
	if registration.SchemaVersion == 0 {
		t.Fatal("SchemaVersion is zero")
	}
	if !registration.Capabilities.RequestInterceptor {
		t.Fatal("request_interceptor is false")
	}
	if !registration.Capabilities.ResponseInterceptor {
		t.Fatal("response_interceptor is false")
	}
	if !registration.Capabilities.StreamChunkInterceptor {
		t.Fatal("response_stream_interceptor is false")
	}
}

func TestRequestInterceptorsRewriteOnlyWhenChanged(t *testing.T) {
	for _, method := range []string{"request.intercept_before", "request.intercept_after"} {
		raw, errIntercept := handleMethod(method, mustJSON(t, map[string]any{
			"Body":         []byte(`{"tools":[{"type":"tool_search","execution":"client","description":"search","parameters":{}}]}`),
			"SourceFormat": "openai-response",
			"ToFormat":     "openai",
		}))
		if errIntercept != nil {
			t.Fatalf("%s error = %v", method, errIntercept)
		}
		changedBody := decodeRequestBodyResult(t, raw)
		if !strings.Contains(string(changedBody), `"type":"function"`) ||
			!strings.Contains(string(changedBody), `"name":"tool_search"`) {
			t.Fatalf("%s result missing converted declaration: %s", method, changedBody)
		}

		unchanged, errUnchanged := handleMethod(method, mustJSON(t, map[string]any{
			"Body":         []byte(`{"tools":[{"type":"function","name":"tool_search","parameters":{}}]}`),
			"SourceFormat": "openai-response",
			"ToFormat":     "openai",
		}))
		if errUnchanged != nil {
			t.Fatalf("%s unchanged error = %v", method, errUnchanged)
		}
		if body := decodeRequestBodyResult(t, unchanged); len(body) != 0 {
			t.Fatalf("%s unchanged result contains body: %s", method, body)
		}
	}
}

func TestProbeHeaderOnlyWhenRequested(t *testing.T) {
	requested, errIntercept := interceptResponse(mustJSON(t, map[string]any{
		"RequestHeaders": http.Header{probeRequestHeader: []string{"1"}},
		"Body":           []byte(`{"type":"message"}`),
	}))
	if errIntercept != nil {
		t.Fatalf("interceptResponse() error = %v", errIntercept)
	}
	if !strings.Contains(string(requested), `"X-Codex-Tool-Search-Shim"`) {
		t.Fatalf("probe response missing header: %s", requested)
	}

	normal, errIntercept := interceptResponse(mustJSON(t, map[string]any{
		"RequestHeaders": http.Header{},
		"Body":           []byte(`{"type":"message"}`),
	}))
	if errIntercept != nil {
		t.Fatalf("interceptResponse() error = %v", errIntercept)
	}
	if strings.Contains(string(normal), `"X-Codex-Tool-Search-Shim"`) {
		t.Fatalf("normal response contains probe header: %s", normal)
	}
}

func validateUpstreamToolSearchDeclaration(t *testing.T, tool map[string]any, location string) {
	t.Helper()
	if tool["type"] != "function" || tool["name"] != "tool_search" {
		t.Fatalf("%s tool_search declaration = %#v", location, tool)
	}
	if tool["execution"] != nil {
		t.Fatalf("%s declaration still has execution: %#v", location, tool)
	}
	if _, exists := tool["description"]; !exists {
		t.Fatalf("%s declaration lost description: %#v", location, tool)
	}
	if _, exists := tool["parameters"]; !exists {
		t.Fatalf("%s declaration lost parameters: %#v", location, tool)
	}
}

func decodeRequestBodyResult(t *testing.T, raw []byte) []byte {
	t.Helper()
	var envelope struct {
		Result struct {
			Body []byte `json:"Body"`
		} `json:"result"`
	}
	if errUnmarshal := json.Unmarshal(raw, &envelope); errUnmarshal != nil {
		t.Fatalf("json.Unmarshal() error = %v", errUnmarshal)
	}
	return envelope.Result.Body
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, errMarshal := json.Marshal(value)
	if errMarshal != nil {
		t.Fatalf("json.Marshal() error = %v", errMarshal)
	}
	return raw
}
