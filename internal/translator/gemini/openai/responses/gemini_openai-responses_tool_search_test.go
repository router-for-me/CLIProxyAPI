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
		{"type": "tool_search", "execution": "client", "description": "Search available deferred tools. Multi-agent tools: Spawn and manage sub-agents.", "parameters": {"type": "object", "properties": {"query": {"type": "string"}, "limit": {"type": "integer"}}, "required": ["query"], "additionalProperties": false}},
		{"type": "function", "name": "spawn_agent", "defer_loading": true, "parameters": {"type": "object", "properties": {"prompt": {"type": "string"}}}}
	],
	"input": [
		{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "Find the multi-agent tools before answering."}]},
		{"type": "tool_search_call", "id": "tsc_search_1", "execution": "client", "call_id": "search_1", "status": "completed", "arguments": {"query": "fixture tools", "limit": 1}},
		{"type": "tool_search_output", "id": "tso_search_1", "execution": "client", "call_id": "search_1", "status": "completed", "tools": [
			{"type": "namespace", "name": "fixture_tools", "description": "Minimal discovery fixture, not the actual Codex agent schema", "tools": [
				{"type": "function", "name": "ping", "description": "Return a test reply", "defer_loading": true, "parameters": {"type": "object", "properties": {}, "additionalProperties": false}}
			]}
		]},
		{"type": "function_call", "call_id": "call_ping", "name": "ping", "namespace": "fixture_tools", "arguments": "{}"},
		{"type": "function_call_output", "call_id": "call_ping", "output": "pong"}
	]
}`

func TestConvertOpenAIResponsesRequestToGemini_ToolSearchRoundTrip(t *testing.T) {
	out, err := ConvertOpenAIResponsesRequestToGemini("gemini-3.8-flash-high", []byte(toolSearchFixtureRequest), true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	root := gjson.ParseBytes(out)

	declarations := root.Get("tools.0.functionDeclarations")
	names := make([]string, 0)
	declarations.ForEach(func(_, declaration gjson.Result) bool {
		names = append(names, declaration.Get("name").String())
		return true
	})
	if len(names) != 3 || names[0] != "exec_command" || names[1] != "tool_search" || names[2] != "fixture_tools__ping" {
		t.Fatalf("functionDeclarations = %v, want [exec_command tool_search fixture_tools__ping]; raw: %s", names, out)
	}
	if got := declarations.Get("1.description").String(); got != "Search available deferred tools. Multi-agent tools: Spawn and manage sub-agents." {
		t.Fatalf("tool_search shim description = %q", got)
	}
	if got := declarations.Get("1.parametersJsonSchema.required.0").String(); got != "query" {
		t.Fatalf("tool_search shim parameters lost: %s", declarations.Get("1").Raw)
	}
	if got := declarations.Get("2.description").String(); got != "Return a test reply" {
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

func TestConvertGeminiResponseToOpenAIResponses_ToolSearchCallStream(t *testing.T) {
	chunks := [][]byte{
		[]byte(`data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"tool_search","args":{"query":"fixture tools","limit":2}}}]},"finishReason":"STOP"}],"modelVersion":"gemini-3.8-flash-high","responseId":"resp_tool_search"}`),
		[]byte(`data: [DONE]`),
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
	}
	for _, test := range []struct {
		label string
		item  gjson.Result
	}{
		{label: "done", item: done.Get("item")},
		{label: "completed", item: completed.Get("response.output.0")},
	} {
		if !test.item.Get("arguments").IsObject() {
			t.Fatalf("%s arguments must be an object: %s", test.label, test.item.Raw)
		}
		if got := test.item.Get("arguments.query").String(); got != "fixture tools" {
			t.Fatalf("%s arguments.query = %q", test.label, got)
		}
		if got := test.item.Get("arguments.limit").Int(); got != 2 {
			t.Fatalf("%s arguments.limit = %d", test.label, got)
		}
	}
	if added.Get("item.call_id").String() == "" || added.Get("item.call_id").String() != done.Get("item.call_id").String() || done.Get("item.call_id").String() != completed.Get("response.output.0.call_id").String() {
		t.Fatalf("call_id must be non-empty and stable across events")
	}
	wantItemID := "tsc_" + added.Get("item.call_id").String()
	for _, test := range []struct {
		label string
		item  gjson.Result
	}{
		{label: "added", item: added.Get("item")},
		{label: "done", item: done.Get("item")},
		{label: "completed", item: completed.Get("response.output.0")},
	} {
		if got := test.item.Get("id").String(); got != wantItemID {
			t.Fatalf("%s id = %q, want %q; raw: %s", test.label, got, wantItemID, test.item.Raw)
		}
	}
}

func TestOpenAIResponsesToolSearchCallItemID(t *testing.T) {
	got := openAIResponsesToolSearchCallItemID("call_fixture")
	if got != "tsc_call_fixture" {
		t.Fatalf("item ID = %q, want tsc_call_fixture", got)
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
	if callID := item.Get("call_id").String(); callID == "" {
		t.Fatalf("non-stream call_id must be non-empty: %s", item.Raw)
	} else if got := item.Get("id").String(); got != "tsc_"+callID {
		t.Fatalf("non-stream id = %q, want %q; raw: %s", got, "tsc_"+callID, item.Raw)
	}
	if item.Get("name").Exists() {
		t.Fatalf("non-stream tool_search_call must not carry a name: %s", item.Raw)
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
	out, err := ConvertOpenAIResponsesRequestToGemini("gemini-3.8-flash-high", []byte(request), false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	root := gjson.ParseBytes(out)
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

func TestConvertGeminiResponseToOpenAIResponses_DiscoveredToolCallRestoresNamespace(t *testing.T) {
	chunks := [][]byte{
		[]byte(`data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"fixture_tools__ping","args":{}}}]},"finishReason":"STOP"}],"modelVersion":"gemini-3.8-flash-high","responseId":"resp_discovered"}`),
		[]byte(`data: [DONE]`),
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

func TestConvertGeminiResponseToOpenAIResponses_UserToolSearchFunctionStaysFunctionCall(t *testing.T) {
	request := []byte(`{"model":"gemini-3.8-flash-high","tools":[{"type":"function","name":"tool_search","parameters":{"type":"object","properties":{"query":{"type":"string"}}}}]}`)
	chunks := [][]byte{
		[]byte(`data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"name":"tool_search","args":{"query":"x"}}}]},"finishReason":"STOP"}],"responseId":"resp_user_fn"}`),
		[]byte(`data: [DONE]`),
	}
	var param any
	sawFunctionCall := false
	for _, chunk := range chunks {
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
	}
	if !sawFunctionCall {
		t.Fatalf("expected a function_call done event")
	}
}

func TestConvertGeminiResponses_SameNamedUserFunctionAndClientToolSearchCoexist(t *testing.T) {
	userFn := `{"type":"function","name":"tool_search","description":"User tool.","parameters":{"type":"object","properties":{"x":{"type":"string"}},"required":["x"]}}`
	clientSearch := `{"type":"tool_search","execution":"client","description":"Search deferred tools.","parameters":{"type":"object","properties":{"query":{"type":"string"}},"required":["query"]}}`
	deferredFn := `{"type":"function","name":"hidden_until_discovered","defer_loading":true,"parameters":{"type":"object","properties":{}}}`

	for _, tc := range []struct {
		name  string
		tools string
	}{
		{name: "user function first", tools: userFn + "," + clientSearch + "," + deferredFn},
		{name: "client tool_search first", tools: clientSearch + "," + userFn + "," + deferredFn},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := []byte(`{
				"model": "gemini-3.8-flash-high",
				"tools": [` + tc.tools + `],
				"input": [
					{"type": "message", "role": "user", "content": "hi"},
					{"type": "tool_search_call", "execution": "client", "call_id": "search_1", "status": "completed", "arguments": {"query": "ping"}},
					{"type": "tool_search_output", "execution": "client", "call_id": "search_1", "status": "completed", "tools": []},
					{"type": "function_call", "call_id": "user_call_1", "name": "tool_search", "arguments": "{\"x\":\"val\"}"},
					{"type": "function_call_output", "call_id": "user_call_1", "output": "user_ok"}
				]
			}`)
			geminiReqBytes, err := ConvertOpenAIResponsesRequestToGemini("gemini-3.8-flash-high", request, true)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			geminiReq := gjson.ParseBytes(geminiReqBytes)
			decls := geminiReq.Get("tools.0.functionDeclarations").Array()
			if len(decls) != 2 {
				t.Fatalf("expected 2 functionDeclarations (user tool_search + client shim, with deferred hidden), got %d: %s", len(decls), geminiReqBytes)
			}

			var shimGeminiName string
			for _, d := range decls {
				n := d.Get("name").String()
				if n != "tool_search" {
					shimGeminiName = n
				}
			}
			if shimGeminiName == "" {
				t.Fatalf("expected a distinct Gemini name for the client tool_search shim: %s", geminiReqBytes)
			}

			// History replay: search_1 uses shimGeminiName, user_call_1 uses "tool_search".
			contents := geminiReq.Get("contents").Array()
			if len(contents) != 5 {
				t.Fatalf("expected 5 contents, got %d: %s", len(contents), geminiReq.Get("contents").Raw)
			}
			if got := contents[1].Get("parts.0.functionCall.name").String(); got != shimGeminiName {
				t.Fatalf("replayed tool_search_call name = %q, want %q", got, shimGeminiName)
			}
			if got := contents[2].Get("parts.0.functionResponse.name").String(); got != shimGeminiName {
				t.Fatalf("replayed tool_search_output name = %q, want %q", got, shimGeminiName)
			}
			if got := contents[3].Get("parts.0.functionCall.name").String(); got != "tool_search" {
				t.Fatalf("replayed user function_call name = %q, want tool_search", got)
			}
			if got := contents[4].Get("parts.0.functionResponse.name").String(); got != "tool_search" {
				t.Fatalf("replayed user function_call_output name = %q, want tool_search", got)
			}

			// Response restoration: calling shimGeminiName -> tool_search_call; calling "tool_search" -> function_call.
			streamChunk := []byte(`data: {"candidates":[{"content":{"role":"model","parts":[{"functionCall":{"id":"c_shim","name":"` + shimGeminiName + `","args":{"query":"agents"}}},{"functionCall":{"id":"c_user","name":"tool_search","args":{"x":"abc"}}}]},"finishReason":"STOP"}],"responseId":"resp_coexist"}`)
			var param any
			var doneItems []gjson.Result
			for _, frame := range [][]byte{streamChunk, []byte("data: [DONE]")} {
				for _, output := range ConvertGeminiResponseToOpenAIResponses(context.Background(), "gemini-3.8-flash-high", request, nil, frame, &param) {
					event, data := parseSSEEvent(t, output)
					if event == "response.output_item.done" {
						doneItems = append(doneItems, data.Get("item"))
					}
				}
			}
			if len(doneItems) != 2 {
				t.Fatalf("expected 2 done items, got %d", len(doneItems))
			}
			if doneItems[0].Get("type").String() != "tool_search_call" || doneItems[0].Get("arguments.query").String() != "agents" {
				t.Fatalf("first item (shim call) = %s, want tool_search_call", doneItems[0].Raw)
			}
			if doneItems[1].Get("type").String() != "function_call" || doneItems[1].Get("name").String() != "tool_search" {
				t.Fatalf("second item (user call) = %s, want function_call named tool_search", doneItems[1].Raw)
			}
		})
	}
}

func TestConvertOpenAIResponsesRequestToGemini_HistoryOnlyWithNamespacedToolSearchChild(t *testing.T) {
	request := []byte(`{
		"model": "gemini-3.8-flash-high",
		"stream": true,
		"tools": [],
		"input": [
			{
				"type": "tool_search_call",
				"execution": "client",
				"call_id": "search_1",
				"status": "completed",
				"arguments": {"query": "fixture tools", "limit": 1}
			},
			{
				"type": "tool_search_output",
				"execution": "client",
				"call_id": "search_1",
				"status": "completed",
				"tools": [
					{
						"type": "namespace",
						"name": "fixture_tools",
						"tools": [
							{
								"type": "function",
								"name": "tool_search",
								"description": "Namespaced child named tool_search",
								"parameters": {"type": "object", "properties": {}, "additionalProperties": false}
							}
						]
					}
				]
			}
		]
	}`)
	out, err := ConvertOpenAIResponsesRequestToGemini("gemini-3.8-flash-high", request, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	root := gjson.ParseBytes(out)
	if got := root.Get("tools.0.functionDeclarations.0.name").String(); got != "fixture_tools__tool_search" {
		t.Fatalf("discovered declaration name = %q, want fixture_tools__tool_search; raw: %s", got, out)
	}
	contents := root.Get("contents").Array()
	if len(contents) != 2 {
		t.Fatalf("expected 2 contents, got %d: %s", len(contents), root.Get("contents").Raw)
	}
	if got := contents[0].Get("parts.0.functionCall.name").String(); got != "tool_search" {
		t.Fatalf("replayed tool_search_call name = %q, want tool_search (must not be hijacked by fixture_tools__tool_search)", got)
	}
	if got := contents[1].Get("parts.0.functionResponse.name").String(); got != "tool_search" {
		t.Fatalf("replayed tool_search_output name = %q, want tool_search (must not be hijacked by fixture_tools__tool_search)", got)
	}
}
