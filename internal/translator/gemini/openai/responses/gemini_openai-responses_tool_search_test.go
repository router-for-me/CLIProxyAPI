package responses

import (
	"context"
	"testing"

	"github.com/tidwall/gjson"
)

const toolSearchFixtureRequest = `{
	"model": "gemini-3.8-flash-high",
	"stream": true,
	"tools": [
		{"type": "function", "name": "exec_command", "parameters": {"type": "object", "properties": {"cmd": {"type": "string"}}}},
		{"type": "tool_search", "execution": "client", "description": "Search deferred tools.", "parameters": {"type": "object", "properties": {"query": {"type": "string"}, "limit": {"type": "number"}}, "required": ["query"], "additionalProperties": false}}
	],
	"input": [
		{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "Find the fixture tools before answering."}]},
		{"type": "tool_search_call", "id": "ts_search_1", "execution": "client", "call_id": "search_1", "status": "completed", "arguments": {"query": "fixture tools", "limit": 1}},
		{"type": "tool_search_output", "id": "tso_search_1", "execution": "client", "call_id": "search_1", "status": "completed", "tools": [
			{"type": "namespace", "name": "fixture_tools", "description": "Minimal discovery fixture", "tools": [
				{"type": "function", "name": "ping", "description": "Return a test reply", "defer_loading": true, "parameters": {"type": "object", "properties": {}, "additionalProperties": false}}
			]}
		]},
		{"type": "function_call", "call_id": "call_ping", "name": "ping", "namespace": "fixture_tools", "arguments": "{}"},
		{"type": "function_call_output", "call_id": "call_ping", "output": "pong"}
	]
}`

func TestConvertOpenAIResponsesRequestToGemini_ToolSearchRoundTrip(t *testing.T) {
	out := ConvertOpenAIResponsesRequestToGemini("gemini-3.8-flash-high", []byte(toolSearchFixtureRequest), true)
	root := gjson.ParseBytes(out)

	declarations := root.Get("tools.0.functionDeclarations")
	names := make([]string, 0)
	declarations.ForEach(func(_, declaration gjson.Result) bool {
		names = append(names, declaration.Get("name").String())
		return true
	})
	if len(names) != 3 || names[0] != "exec_command" || names[1] != "fixture_tools__ping" || names[2] != "tool_search" {
		t.Fatalf("functionDeclarations = %v, want exec_command, discovered fixture_tools__ping and the tool_search shim; raw: %s", names, out)
	}
	if got := declarations.Get("2.description").String(); got != "Search deferred tools." {
		t.Fatalf("tool_search shim description = %q", got)
	}
	if got := declarations.Get("2.parametersJsonSchema.required.0").String(); got != "query" {
		t.Fatalf("tool_search shim parameters lost: %s", declarations.Get("2").Raw)
	}
	if got := declarations.Get("1.description").String(); got != "Return a test reply" {
		t.Fatalf("discovered tool description = %q", got)
	}

	contents := root.Get("contents").Array()
	if len(contents) != 5 {
		t.Fatalf("expected 5 contents (user, search call, search output, ping call, ping output), got %d: %s", len(contents), root.Get("contents").Raw)
	}
	searchCall := contents[1]
	if got := searchCall.Get("role").String(); got != "model" {
		t.Fatalf("search call role = %q, want model", got)
	}
	if got := searchCall.Get("parts.0.functionCall.name").String(); got != "tool_search" {
		t.Fatalf("search call function name = %q, want tool_search; raw: %s", got, searchCall.Raw)
	}
	if got := searchCall.Get("parts.0.functionCall.id").String(); got != "search_1" {
		t.Fatalf("search call id = %q, want search_1", got)
	}
	if got := searchCall.Get("parts.0.functionCall.args.query").String(); got != "fixture tools" {
		t.Fatalf("search call args = %s, want object-valued query", searchCall.Get("parts.0.functionCall.args").Raw)
	}
	if got := searchCall.Get("parts.0.functionCall.args.limit").Int(); got != 1 {
		t.Fatalf("search call limit = %d, want 1", got)
	}

	searchOutput := contents[2]
	if got := searchOutput.Get("role").String(); got != "user" {
		t.Fatalf("search output role = %q, want user", got)
	}
	if got := searchOutput.Get("parts.0.functionResponse.name").String(); got != "tool_search" {
		t.Fatalf("search output function name = %q, want tool_search; raw: %s", got, searchOutput.Raw)
	}
	if got := searchOutput.Get("parts.0.functionResponse.id").String(); got != "search_1" {
		t.Fatalf("search output id = %q, want search_1", got)
	}
	result := gjson.Parse(searchOutput.Get("parts.0.functionResponse.response.result").String())
	if got := result.Get("status").String(); got != "completed" {
		t.Fatalf("search output status = %q; raw result: %s", got, result.Raw)
	}
	if got := result.Get("tools.0").String(); got != "fixture_tools__ping" || len(result.Get("tools").Array()) != 1 {
		t.Fatalf("search output must list the Gemini-visible discovered names, got %s", result.Get("tools").Raw)
	}

	if got := contents[3].Get("parts.0.functionCall.name").String(); got != "fixture_tools__ping" {
		t.Fatalf("discovered tool call name = %q, want fixture_tools__ping", got)
	}
	if got := contents[4].Get("parts.0.functionResponse.name").String(); got != "fixture_tools__ping" {
		t.Fatalf("discovered tool response name = %q, want fixture_tools__ping", got)
	}
}

func TestConvertOpenAIResponsesRequestToGemini_ToolSearchCallWithStringArgumentsAndEmptyOutput(t *testing.T) {
	request := `{
		"model": "gemini-3.8-flash-high",
		"tools": [{"type": "tool_search", "execution": "client"}],
		"input": [
			{"type": "message", "role": "user", "content": "hi"},
			{"type": "tool_search_call", "execution": "client", "call_id": "search_2", "arguments": "{\"query\":\"nothing\"}"},
			{"type": "tool_search_output", "execution": "client", "call_id": "search_2", "status": "completed", "tools": []}
		]
	}`
	root := gjson.ParseBytes(ConvertOpenAIResponsesRequestToGemini("gemini-3.8-flash-high", []byte(request), false))
	contents := root.Get("contents").Array()
	if len(contents) != 3 {
		t.Fatalf("expected 3 contents, got %d: %s", len(contents), root.Get("contents").Raw)
	}
	if got := contents[1].Get("parts.0.functionCall.args.query").String(); got != "nothing" {
		t.Fatalf("string arguments must be parsed into args, got %s", contents[1].Raw)
	}
	result := gjson.Parse(contents[2].Get("parts.0.functionResponse.response.result").String())
	if !result.Get("tools").IsArray() || len(result.Get("tools").Array()) != 0 {
		t.Fatalf("empty discovery must produce an empty tools list, got %s", result.Raw)
	}
}

func TestConvertGeminiResponseToOpenAIResponses_ToolSearchCallStream(t *testing.T) {
	chunks := [][]byte{
		[]byte(`data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"tool_search","args":{"query":"fixture tools","limit":2}}}]},"finishReason":"STOP"}],"modelVersion":"gemini-3.8-flash-high","responseId":"resp_tool_search"}`),
	}

	var param any
	var added, done, completed gjson.Result
	for _, chunk := range chunks {
		for _, output := range ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini-3.8-flash-high", []byte(toolSearchFixtureRequest), nil, chunk, &param) {
			event, data := parseSSEEvent(t, output)
			switch event {
			case "response.output_item.added":
				added = data
			case "response.output_item.done":
				done = data
			case "response.function_call_arguments.delta", "response.function_call_arguments.done":
				t.Fatalf("tool_search_call must not emit function_call_arguments events, got %s", event)
			case "response.completed":
				completed = data
			}
		}
	}
	if !added.Exists() || !done.Exists() || !completed.Exists() {
		t.Fatalf("missing tool_search_call lifecycle events: added=%v done=%v completed=%v", added.Exists(), done.Exists(), completed.Exists())
	}
	for _, test := range []struct {
		label  string
		item   gjson.Result
		status string
	}{
		{label: "added", item: added.Get("item"), status: "in_progress"},
		{label: "done", item: done.Get("item"), status: "completed"},
		{label: "completed", item: completed.Get("response.output.0"), status: "completed"},
	} {
		if got := test.item.Get("type").String(); got != "tool_search_call" {
			t.Fatalf("%s type = %q, want tool_search_call; raw: %s", test.label, got, test.item.Raw)
		}
		if got := test.item.Get("execution").String(); got != "client" {
			t.Fatalf("%s execution = %q, want client", test.label, got)
		}
		if got := test.item.Get("status").String(); got != test.status {
			t.Fatalf("%s status = %q, want %s", test.label, got, test.status)
		}
		if test.item.Get("name").Exists() {
			t.Fatalf("%s must not carry a function name: %s", test.label, test.item.Raw)
		}
		if !test.item.Get("arguments").IsObject() {
			t.Fatalf("%s arguments must be an object: %s", test.label, test.item.Raw)
		}
		if got := test.item.Get("arguments.query").String(); got != "fixture tools" {
			t.Fatalf("%s arguments.query = %q", test.label, got)
		}
		if got := test.item.Get("arguments.limit").Int(); got != 2 {
			t.Fatalf("%s arguments.limit = %d", test.label, got)
		}
		callID := test.item.Get("call_id").String()
		if callID == "" || test.item.Get("id").String() != "ts_"+callID {
			t.Fatalf("%s call_id/id mismatch: %s", test.label, test.item.Raw)
		}
	}
	if added.Get("item.call_id").String() != done.Get("item.call_id").String() || done.Get("item.call_id").String() != completed.Get("response.output.0.call_id").String() {
		t.Fatalf("call_id must be stable across events")
	}
}

func TestConvertGeminiResponseToOpenAIResponses_DiscoveredToolCallRestoresNamespace(t *testing.T) {
	chunks := [][]byte{
		[]byte(`data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"fixture_tools__ping","args":{}}}]},"finishReason":"STOP"}],"modelVersion":"gemini-3.8-flash-high","responseId":"resp_discovered"}`),
	}
	var param any
	var done gjson.Result
	for _, chunk := range chunks {
		for _, output := range ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini-3.8-flash-high", []byte(toolSearchFixtureRequest), nil, chunk, &param) {
			event, data := parseSSEEvent(t, output)
			if event == "response.output_item.done" && data.Get("item.type").String() == "function_call" {
				done = data
			}
		}
	}
	if !done.Exists() {
		t.Fatalf("missing function_call done event for discovered tool")
	}
	if got := done.Get("item.name").String(); got != "ping" {
		t.Fatalf("discovered tool name = %q, want ping", got)
	}
	if got := done.Get("item.namespace").String(); got != "fixture_tools" {
		t.Fatalf("discovered tool namespace = %q, want fixture_tools", got)
	}
}

func TestConvertGeminiResponseToOpenAIResponsesNonStream_ToolSearchCall(t *testing.T) {
	raw := []byte(`{"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"tool_search","args":{"query":"fixture tools"}}}]},"finishReason":"STOP"}],"modelVersion":"gemini-3.8-flash-high","responseId":"resp_tool_search_nonstream"}`)
	out := ConvertGeminiResponseToOpenAIResponsesNonStream(context.Background(), "gemini-3.8-flash-high", []byte(toolSearchFixtureRequest), nil, raw, nil)
	root := gjson.ParseBytes(out)

	item := root.Get("output.0")
	if got := item.Get("type").String(); got != "tool_search_call" {
		t.Fatalf("non-stream output type = %q, want tool_search_call; raw: %s", got, out)
	}
	if got := item.Get("execution").String(); got != "client" {
		t.Fatalf("non-stream execution = %q", got)
	}
	if got := item.Get("status").String(); got != "completed" {
		t.Fatalf("non-stream status = %q", got)
	}
	if got := item.Get("arguments.query").String(); got != "fixture tools" {
		t.Fatalf("non-stream arguments = %s", item.Get("arguments").Raw)
	}
	if item.Get("name").Exists() {
		t.Fatalf("non-stream tool_search_call must not carry a name: %s", item.Raw)
	}
	callID := item.Get("call_id").String()
	if callID == "" || item.Get("id").String() != "ts_"+callID {
		t.Fatalf("non-stream call_id/id mismatch: %s", item.Raw)
	}
}

func TestConvertGeminiResponseToOpenAIResponses_UserToolSearchFunctionStaysFunctionCall(t *testing.T) {
	request := []byte(`{"model":"gemini-3.8-flash-high","tools":[{"type":"function","name":"tool_search","parameters":{"type":"object","properties":{"query":{"type":"string"}}}}]}`)
	chunk := []byte(`data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"tool_search","args":{"query":"x"}}}]},"finishReason":"STOP"}],"responseId":"resp_user_fn"}`)
	var param any
	sawFunctionCall := false
	for _, output := range ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini-3.8-flash-high", request, nil, chunk, &param) {
		event, data := parseSSEEvent(t, output)
		if event == "response.output_item.done" {
			switch data.Get("item.type").String() {
			case "function_call":
				sawFunctionCall = true
			case "tool_search_call":
				t.Fatalf("a user-declared tool_search function must stay a function_call")
			}
		}
	}
	if !sawFunctionCall {
		t.Fatalf("expected a function_call done event")
	}
}
