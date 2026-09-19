package responses

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// Antigravity reuses the Gemini Responses translator, so the client-executed
// tool_search shim and the discovered tools must survive the Antigravity
// request envelope and the response unwrapping as well.
func TestConvertOpenAIResponsesRequestToAntigravity_ExposesClientToolSearch(t *testing.T) {
	raw := []byte(`{
		"model": "gemini-3.8-flash-high",
		"tools": [
			{"type": "tool_search", "execution": "client", "description": "Search deferred tools.", "parameters": {"type": "object", "properties": {"query": {"type": "string"}}, "required": ["query"]}}
		],
		"input": [
			{"type": "message", "role": "user", "content": "find tools"},
			{"type": "tool_search_call", "execution": "client", "call_id": "search_1", "status": "completed", "arguments": {"query": "fixture"}},
			{"type": "tool_search_output", "execution": "client", "call_id": "search_1", "status": "completed", "tools": [
				{"type": "namespace", "name": "fixture_tools", "tools": [{"type": "function", "name": "ping", "defer_loading": true, "parameters": {"type": "object", "properties": {}}}]}
			]}
		]
	}`)
	out := ConvertOpenAIResponsesRequestToAntigravity("gemini-3.8-flash-high", raw, true)
	root := gjson.ParseBytes(out)

	names := make([]string, 0)
	root.Get("request.tools.0.functionDeclarations").ForEach(func(_, declaration gjson.Result) bool {
		names = append(names, declaration.Get("name").String())
		return true
	})
	if len(names) != 2 || names[0] != "fixture_tools__ping" || names[1] != "tool_search" {
		t.Fatalf("functionDeclarations = %v, want discovered ping and the tool_search shim; raw: %s", names, out)
	}
	contents := root.Get("request.contents").Array()
	if len(contents) != 3 {
		t.Fatalf("expected 3 contents, got %d: %s", len(contents), root.Get("request.contents").Raw)
	}
	if got := contents[1].Get("parts.0.functionCall.name").String(); got != "tool_search" {
		t.Fatalf("replayed search call name = %q, want tool_search", got)
	}
	if got := contents[2].Get("parts.0.functionResponse.name").String(); got != "tool_search" {
		t.Fatalf("replayed search output name = %q, want tool_search", got)
	}
}

func TestConvertAntigravityResponseToOpenAIResponses_ToolSearchCall(t *testing.T) {
	originalRequest := []byte(`{"model":"gemini-3.8-flash-high","tools":[{"type":"tool_search","execution":"client"}],"input":"find tools"}`)
	chunk := []byte(`data: {"response":{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"tool_search","args":{"query":"fixture"}}}]},"finishReason":"STOP"}],"responseId":"resp_ag_tool_search"}}`)

	var param any
	var done gjson.Result
	for _, output := range ConvertAntigravityResponseToOpenAIResponses(context.Background(), "gemini-3.8-flash-high", originalRequest, nil, chunk, &param) {
		payload := string(output)
		if idx := strings.Index(payload, "data:"); idx >= 0 {
			data := gjson.Parse(payload[idx+len("data:"):])
			if data.Get("type").String() == "response.output_item.done" {
				done = data
			}
		}
	}
	if !done.Exists() {
		t.Fatalf("missing response.output_item.done event")
	}
	if got := done.Get("item.type").String(); got != "tool_search_call" {
		t.Fatalf("item type = %q, want tool_search_call; raw: %s", got, done.Raw)
	}
	if got := done.Get("item.arguments.query").String(); got != "fixture" {
		t.Fatalf("item arguments = %s", done.Get("item.arguments").Raw)
	}
	if got := done.Get("item.execution").String(); got != "client" {
		t.Fatalf("item execution = %q", got)
	}
}
