package responses

import (
	"context"
	"strings"
	"testing"

	"github.com/MoeForge/moego/ojson"
)

// Synthetic Codex request: one eager tool, the client tool_search declaration,
// and one completed search round that returned a namespaced tool.
const chatToolSearchTestRequest = `{
  "model": "chat-test",
  "stream": true,
  "tools": [
    {"type":"function","name":"exec_command","parameters":{"type":"object","properties":{"cmd":{"type":"string"}}}},
    {"type":"tool_search","execution":"client","description":"Search deferred tools","parameters":{"type":"object","properties":{"query":{"type":"string"},"limit":{"type":"number"}},"required":["query"]}}
  ],
  "input": [
    {"type":"message","role":"user","content":[{"type":"input_text","text":"list my calendars"}]},
    {"type":"tool_search_call","call_id":"call_ts1","status":"completed","execution":"client","arguments":{"query":"calendar"}},
    {"type":"tool_search_output","call_id":"call_ts1","status":"completed","execution":"client","tools":[
      {"type":"namespace","name":"mcp__demo__cal","description":"Calendar","tools":[
        {"type":"function","name":"_list","description":"List calendars","defer_loading":true,"parameters":{"type":"object","properties":{}}}
      ]}
    ]}
  ]
}`

func TestChatToolSearch_RequestBridgesSearchAndDeclaresLoadedTools(t *testing.T) {
	out, errConvert := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("chat-test", []byte(chatToolSearchTestRequest), true)
	if errConvert != nil {
		t.Fatalf("convert request: %v", errConvert)
	}

	names := ojson.GetBytes(out, "tools.#.function.name").Raw
	for _, want := range []string{`"exec_command"`, `"tool_search"`, `"mcp__demo__cal___list"`} {
		if !strings.Contains(names, want) {
			t.Fatalf("tools %s missing %s", names, want)
		}
	}
	search := ojson.GetBytes(out, `tools.#(function.name=="tool_search").function`)
	if search.Get("parameters.properties.limit").Type == ojson.Null {
		t.Fatalf("tool_search schema lost: %s", search.Raw)
	}

	assistant := ojson.GetBytes(out, `messages.#(role=="assistant")`)
	call := assistant.Get("tool_calls.0")
	if call.Get("id").String() != "call_ts1" || call.Get("function.name").String() != "tool_search" {
		t.Fatalf("search call not bridged: %s", assistant.Raw)
	}
	if ojson.Get(call.Get("function.arguments").String(), "query").String() != "calendar" {
		t.Fatalf("search arguments lost: %s", call.Raw)
	}
	tool := ojson.GetBytes(out, `messages.#(role=="tool")`)
	if tool.Get("tool_call_id").String() != "call_ts1" || !strings.Contains(tool.Get("content").String(), "mcp__demo__cal___list") {
		t.Fatalf("search output not bridged: %s", tool.Raw)
	}
}

func TestChatToolSearch_StreamResponseRestoresSearchCall(t *testing.T) {
	lines := []string{
		`data: {"id":"r1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_s","type":"function","function":{"name":"tool_search","arguments":""}}]},"finish_reason":null}]}`,
		`data: {"id":"r1","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"query\":\"cal\"}"}}]},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	}
	var param any
	var added, done, completed ojson.Result
	for _, line := range lines {
		for _, chunk := range ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "m", []byte(chatToolSearchTestRequest), nil, []byte(line), &param) {
			event, data := parseOpenAIResponsesSSEEvent(t, chunk)
			if strings.HasPrefix(event, "response.function_call_arguments") {
				t.Fatalf("search call must not stream function argument events: %s", event)
			}
			switch {
			case event == "response.output_item.added" && data.Get("item.type").String() == "tool_search_call":
				added = data
			case event == "response.output_item.done" && data.Get("item.type").String() == "tool_search_call":
				done = data
			case event == "response.completed":
				completed = data
			}
		}
	}
	if !added.Exists() || added.Get("item.call_id").String() != "call_s" {
		t.Fatalf("missing tool_search_call added event")
	}
	if done.Get("item.arguments.query").String() != "cal" || done.Get("item.execution").String() != "client" {
		t.Fatalf("bad tool_search_call done event: %s", done.Raw)
	}
	if completed.Get("response.output.0.type").String() != "tool_search_call" {
		t.Fatalf("completed output = %s", completed.Get("response.output").Raw)
	}
}

func TestChatToolSearch_NonStreamResponseRestoresSearchAndLoadedToolCalls(t *testing.T) {
	resp := `{"id":"r2","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","content":null,"tool_calls":[
	  {"id":"call_a","type":"function","function":{"name":"tool_search","arguments":"{\"query\":\"cal\"}"}},
	  {"id":"call_b","type":"function","function":{"name":"mcp__demo__cal___list","arguments":"{}"}}
	]}}]}`
	out := ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream(context.Background(), "m", []byte(chatToolSearchTestRequest), nil, []byte(resp), nil)
	search := ojson.GetBytes(out, `output.#(call_id=="call_a")`)
	if search.Get("type").String() != "tool_search_call" || search.Get("arguments.query").String() != "cal" {
		t.Fatalf("want tool_search_call, got %s", search.Raw)
	}
	loaded := ojson.GetBytes(out, `output.#(call_id=="call_b")`)
	if loaded.Get("type").String() != "function_call" || loaded.Get("name").String() != "_list" || loaded.Get("namespace").String() != "mcp__demo__cal" {
		t.Fatalf("loaded tool call must restore namespace identity: %s", loaded.Raw)
	}
}

func TestChatToolSearch_HostedSearchIsNotBridged(t *testing.T) {
	raw := `{"model":"chat-test","tools":[{"type":"tool_search","execution":"server"}],"input":[
	  {"type":"message","role":"user","content":"hi"},
	  {"type":"tool_search_call","call_id":"call_h","execution":"server","arguments":{"query":"x"}},
	  {"type":"tool_search_output","call_id":"call_h","execution":"server","tools":[{"type":"function","name":"hosted_tool","parameters":{"type":"object"}}]}
	]}`
	out, errConvert := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("chat-test", []byte(raw), false)
	if errConvert != nil {
		t.Fatalf("convert request: %v", errConvert)
	}
	if tools := ojson.GetBytes(out, "tools"); tools.Exists() && len(tools.Array()) > 0 {
		t.Fatalf("hosted search must not declare tools: %s", tools.Raw)
	}
	if strings.Contains(string(out), "call_h") {
		t.Fatalf("hosted search history must not be replayed: %s", out)
	}
}

func TestChatToolSearch_NameCollisionKeepsUserFunction(t *testing.T) {
	raw := `{"model":"chat-test","tools":[
	  {"type":"function","name":"tool_search","parameters":{"type":"object","properties":{"q":{"type":"string"}}}},
	  {"type":"tool_search","execution":"client"}
	],"input":[{"type":"message","role":"user","content":"hi"}]}`
	out, errConvert := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("chat-test", []byte(raw), false)
	if errConvert != nil {
		t.Fatalf("convert request: %v", errConvert)
	}
	names := ojson.GetBytes(out, "tools.#.function.name").Array()
	if len(names) != 2 || names[0].String() == names[1].String() {
		t.Fatalf("both tools must be declared with distinct names: %s", ojson.GetBytes(out, "tools").Raw)
	}

	resp := `{"id":"r3","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","content":null,"tool_calls":[
	  {"id":"call_u","type":"function","function":{"name":"tool_search","arguments":"{\"q\":\"x\"}"}},
	  {"id":"call_s","type":"function","function":{"name":"tool_search_1","arguments":"{\"query\":\"y\"}"}}
	]}}]}`
	restored := ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream(context.Background(), "m", []byte(raw), nil, []byte(resp), nil)
	if user := ojson.GetBytes(restored, `output.#(call_id=="call_u")`); user.Get("type").String() != "function_call" || user.Get("name").String() != "tool_search" {
		t.Fatalf("user function must stay a function_call: %s", user.Raw)
	}
	if search := ojson.GetBytes(restored, `output.#(call_id=="call_s")`); search.Get("type").String() != "tool_search_call" || search.Get("arguments.query").String() != "y" {
		t.Fatalf("renamed search call must restore as tool_search_call: %s", search.Raw)
	}
}

func TestChatToolSearch_ToolChoiceAndIncompleteArguments(t *testing.T) {
	raw := `{"model":"chat-test","tools":[
	  {"type":"function","name":"tool_search","parameters":{"type":"object","properties":{"q":{"type":"string"}}}},
	  {"type":"tool_search","execution":"client"}
	],"tool_choice":{"type":"tool_search"},"input":[{"type":"message","role":"user","content":"hi"}]}`
	out, errConvert := ConvertOpenAIResponsesRequestToOpenAIChatCompletions("chat-test", []byte(raw), false)
	if errConvert != nil {
		t.Fatalf("convert request: %v", errConvert)
	}
	if got := ojson.GetBytes(out, "tool_choice.function.name").String(); got != "tool_search_1" {
		t.Fatalf("tool_choice.function.name = %q, want tool_search_1; raw: %s", got, out)
	}

	resp := `{"id":"r4","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","content":null,"tool_calls":[
	  {"id":"call_trunc","type":"function","function":{"name":"tool_search_1","arguments":"{\"query\":"}}
	]}}]}`
	restored := ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream(context.Background(), "m", []byte(raw), nil, []byte(resp), nil)
	search := ojson.GetBytes(restored, `output.#(call_id=="call_trunc")`)
	if search.Get("type").String() != "tool_search_call" || search.Get("status").String() != "incomplete" {
		t.Fatalf("truncated search call must be incomplete tool_search_call: %s", search.Raw)
	}

	emptyLines := []string{
		`data: {"id":"r5","object":"chat.completion.chunk","created":1,"model":"m","choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"call_empty","type":"function","function":{"name":"tool_search_1","arguments":""}}]},"finish_reason":"tool_calls"}]}`,
		`data: [DONE]`,
	}
	var param any
	var doneItem, completedItem ojson.Result
	for _, line := range emptyLines {
		for _, chunk := range ConvertOpenAIChatCompletionsResponseToOpenAIResponses(context.Background(), "m", []byte(raw), nil, []byte(line), &param) {
			event, data := parseOpenAIResponsesSSEEvent(t, chunk)
			switch event {
			case "response.output_item.done":
				doneItem = data.Get("item")
			case "response.completed":
				completedItem = data.Get("response.output.0")
			}
		}
	}
	emptyNonStream := `{"id":"r5","object":"chat.completion","created":1,"model":"m","choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_empty","type":"function","function":{"name":"tool_search_1","arguments":""}}]}}]}`
	nonStreamItem := ojson.GetBytes(ConvertOpenAIChatCompletionsResponseToOpenAIResponsesNonStream(context.Background(), "m", []byte(raw), nil, []byte(emptyNonStream), nil), "output.0")
	if doneItem.Get("status").String() != "incomplete" || doneItem.Raw != completedItem.Raw || doneItem.Raw != nonStreamItem.Raw {
		t.Fatalf("empty-args tool_search_call mismatch: done=%s completed=%s nonstream=%s", doneItem.Raw, completedItem.Raw, nonStreamItem.Raw)
	}
}
