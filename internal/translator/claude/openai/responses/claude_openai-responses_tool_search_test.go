package responses

import (
	"context"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
)

// Fixtures follow openai/codex: the tool_search spec from
// core/src/tools/handlers/tool_search_spec.rs, and the tool_search_call and
// tool_search_output items from protocol/src/models.rs. Search results are
// namespaces whose children carry defer_loading (tools/src/tool_search.rs).
const (
	codexToolSearchDeclaration = `{"type":"tool_search","execution":"client","description":"# Tool discovery\n\nSearches over deferred tool metadata with BM25 and exposes matching tools for the next model call.\n\nSome of the tools may not have been provided to you upfront, and you should use this tool (` + "`tool_search`" + `) to search for the required tools.","parameters":{"type":"object","properties":{"limit":{"type":"number","description":"Maximum number of tools to return. Defaults to 8."},"query":{"type":"string","description":"Search query for deferred tools."}},"required":["query"],"additionalProperties":false}}`

	codexExecCommandTool = `{"type":"function","name":"exec_command","description":"Run a command.","strict":false,"parameters":{"type":"object","properties":{"cmd":{"type":"string"}},"required":["cmd"]}}`
	codexDeferredTool    = `{"type":"function","name":"lookup_order","description":"Look up an order.","strict":false,"defer_loading":true,"parameters":{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}}`
	codexDeferredNSTool  = `{"type":"namespace","name":"mcp__node_repl__","description":"Node REPL.","tools":[{"type":"function","name":"js","description":"Run JavaScript.","strict":false,"defer_loading":true,"parameters":{"type":"object","properties":{"code":{"type":"string"}},"required":["code"]}}]}`

	codexUserTurn = `{"type":"message","role":"user","content":[{"type":"input_text","text":"create a calendar event"}]}`

	codexCalendarNamespace = `{"type":"namespace","name":"mcp__codex_apps__","description":"Tools in the mcp__codex_apps__ namespace.","tools":[{"type":"function","name":"calendar_create_event","description":"Create a calendar event.","strict":false,"defer_loading":true,"parameters":{"type":"object","properties":{"title":{"type":"string"}},"required":["title"],"additionalProperties":false}}]}`

	codexSearchCall1   = `{"type":"tool_search_call","call_id":"call_search_1","status":"completed","execution":"client","arguments":{"query":"calendar create","limit":8}}`
	codexSearchOutput1 = `{"type":"tool_search_output","call_id":"call_search_1","status":"completed","execution":"client","tools":[{"type":"namespace","name":"mcp__codex_apps__","description":"Tools in the mcp__codex_apps__ namespace.","tools":[{"type":"function","name":"calendar_create_event","description":"Create a calendar event.","strict":false,"defer_loading":true,"parameters":{"type":"object","properties":{"title":{"type":"string"}},"required":["title"],"additionalProperties":false}}]},{"type":"function","name":"lookup_order","description":"Look up an order.","strict":false,"defer_loading":true,"parameters":{"type":"object","properties":{"id":{"type":"string"}},"required":["id"]}}]}`
	codexNSCall        = `{"type":"function_call","call_id":"call_cal_1","name":"calendar_create_event","namespace":"mcp__codex_apps__","arguments":"{\"title\":\"Standup\"}"}`
	codexNSCallOutput  = `{"type":"function_call_output","call_id":"call_cal_1","output":"created"}`
	codexSearchCall2   = `{"type":"tool_search_call","call_id":"call_search_2","status":"completed","execution":"client","arguments":{"query":"javascript"}}`
	codexSearchOutput2 = `{"type":"tool_search_output","call_id":"call_search_2","status":"completed","execution":"client","tools":[{"type":"namespace","name":"mcp__node_repl__","description":"Node REPL.","tools":[{"type":"function","name":"js","description":"Run JavaScript.","strict":false,"defer_loading":true,"parameters":{"type":"object","properties":{"code":{"type":"string"}},"required":["code"]}}]}]}`
)

func codexToolSearchRequest(items ...string) []byte {
	return []byte(`{"model":"claude-sonnet-4-5","instructions":"You are Codex.","input":[` + strings.Join(items, ",") + `],"tools":[` +
		strings.Join([]string{codexExecCommandTool, codexToolSearchDeclaration, codexDeferredTool, codexDeferredNSTool}, ",") +
		`],"tool_choice":"auto","stream":true}`)
}

func convertToolSearchRequest(t *testing.T, request []byte) gjson.Result {
	t.Helper()
	out, err := ConvertOpenAIResponsesRequestToClaude("claude-sonnet-4-5", request, true)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	return gjson.ParseBytes(out)
}

func claudeToolNamesOf(req gjson.Result) []string {
	var names []string
	req.Get("tools").ForEach(func(_, tool gjson.Result) bool {
		names = append(names, tool.Get("name").String())
		return true
	})
	return names
}

func TestConvertOpenAIResponsesRequestToClaude_ToolSearchBecomesQueryFunctionTool(t *testing.T) {
	req := convertToolSearchRequest(t, codexToolSearchRequest(codexUserTurn))

	tool := req.Get(`tools.#(name=="tool_search")`)
	if !tool.Exists() {
		t.Fatalf("tool_search tool missing: %s", req.Get("tools").Raw)
	}
	if tool.Get("type").Exists() || tool.Get("execution").Exists() {
		t.Fatalf("raw Responses tool_search fields leaked to Claude: %s", tool.Raw)
	}
	if got := tool.Get("input_schema.properties.query.type").String(); got != "string" {
		t.Fatalf("query type = %q, want string: %s", got, tool.Raw)
	}
	if !tool.Get(`input_schema.required.#(=="query")`).Exists() {
		t.Fatalf("query is not required: %s", tool.Raw)
	}
	if got := tool.Get("input_schema.properties.limit.type").String(); got != "number" {
		t.Fatalf("declared limit property lost: %s", tool.Raw)
	}
	if !strings.HasPrefix(tool.Get("description").String(), "# Tool discovery") {
		t.Fatalf("declared description lost: %s", tool.Raw)
	}
}

func TestConvertOpenAIResponsesRequestToClaude_ToolSearchSchemaAlwaysRequiresQuery(t *testing.T) {
	for _, declaration := range []string{
		`{"type":"tool_search","execution":"client"}`,
		`{"type":"tool_search","execution":"client","parameters":{"type":"object","properties":{"limit":{"type":"number"}}}}`,
	} {
		req := convertToolSearchRequest(t, []byte(`{"model":"claude-sonnet-4-5","input":"hi","tools":[`+declaration+`]}`))
		tool := req.Get("tools.0")
		if tool.Get("name").String() != responsesToolSearchType ||
			tool.Get("input_schema.properties.query.type").String() != "string" ||
			!tool.Get(`input_schema.required.#(=="query")`).Exists() {
			t.Fatalf("declaration %s produced %s", declaration, tool.Raw)
		}
	}
}

func TestConvertOpenAIResponsesRequestToClaude_HostedToolSearchIsNotForwarded(t *testing.T) {
	req := convertToolSearchRequest(t, []byte(`{"model":"claude-sonnet-4-5","input":"hi","tools":[`+codexExecCommandTool+`,{"type":"tool_search","name":"ToolSearch"},`+codexDeferredTool+`]}`))
	if got := strings.Join(claudeToolNamesOf(req), ","); got != "exec_command,lookup_order" {
		t.Fatalf("tools = %s, want hosted search dropped and deferred tools kept", got)
	}
}

func TestConvertOpenAIResponsesRequestToClaude_ToolSearchNameIsCollisionSafe(t *testing.T) {
	req := convertToolSearchRequest(t, []byte(`{"model":"claude-sonnet-4-5","input":"hi","tools":[{"type":"function","name":"tool.search","parameters":{"type":"object"}},`+codexToolSearchDeclaration+`]}`))
	names := claudeToolNamesOf(req)
	// The third tool is the deferred anchor native tool search declares.
	if len(names) != 3 || names[1] != responsesToolSearchType || names[0] == names[1] || names[2] != responsesDeferredAnchorName {
		t.Fatalf("tool names = %v, want unique names with tool_search kept", names)
	}
}

func TestConvertOpenAIResponsesRequestToClaude_ToolSearchOmitsDeferredTools(t *testing.T) {
	req := convertToolSearchRequest(t, codexToolSearchRequest(codexUserTurn))
	if got := strings.Join(claudeToolNamesOf(req), ","); got != "exec_command,tool_search,"+responsesDeferredAnchorName {
		t.Fatalf("tools = %s, want deferred tools omitted and only the anchor declared", got)
	}
}

func TestConvertOpenAIResponsesRequestToClaude_ToolSearchHistoryReplaysAndActivatesTools(t *testing.T) {
	turn2 := convertToolSearchRequest(t, codexToolSearchRequest(codexUserTurn, codexSearchCall1, codexSearchOutput1))

	wantTurn2 := "exec_command,tool_search," + responsesDeferredAnchorName + ",mcp__codex_apps__calendar_create_event,lookup_order"
	if got := strings.Join(claudeToolNamesOf(turn2), ","); got != wantTurn2 {
		t.Fatalf("turn 2 tools = %s, want %s", got, wantTurn2)
	}
	// Discovered tools are declared as deferred, so adding them never changes
	// the cached tools prefix; declared tools stay eager.
	for _, name := range []string{"mcp__codex_apps__calendar_create_event", "lookup_order"} {
		tool := turn2.Get(`tools.#(name=="` + name + `")`)
		if !tool.Get("defer_loading").Bool() {
			t.Fatalf("discovered tool %s is not deferred: %s", name, tool.Raw)
		}
	}
	for _, name := range []string{"exec_command", "tool_search"} {
		if tool := turn2.Get(`tools.#(name=="` + name + `")`); tool.Get("defer_loading").Exists() {
			t.Fatalf("declared tool %s must stay eager: %s", name, tool.Raw)
		}
	}
	calendar := turn2.Get(`tools.#(name=="mcp__codex_apps__calendar_create_event")`)
	if calendar.Get("input_schema.required.0").String() != "title" {
		t.Fatalf("discovered tool definition = %s", calendar.Raw)
	}

	toolUse := turn2.Get("messages.1.content.0")
	if toolUse.Get("type").String() != "tool_use" || toolUse.Get("id").String() != "call_search_1" ||
		toolUse.Get("name").String() != "tool_search" || toolUse.Get("input.query").String() != "calendar create" ||
		toolUse.Get("input.limit").Int() != 8 {
		t.Fatalf("tool_search_call replay = %s", toolUse.Raw)
	}
	toolResult := turn2.Get("messages.2.content.0")
	wantContent := `[{"type":"tool_reference","tool_name":"mcp__codex_apps__calendar_create_event"},{"type":"tool_reference","tool_name":"lookup_order"}]`
	if toolResult.Get("type").String() != "tool_result" || toolResult.Get("tool_use_id").String() != "call_search_1" ||
		toolResult.Get("content").Raw != wantContent {
		t.Fatalf("tool_search_output replay = %s", toolResult.Raw)
	}

	// A later turn appends newly discovered tools without reordering earlier
	// ones, so the cached tools prefix stays stable.
	turn3 := convertToolSearchRequest(t, codexToolSearchRequest(codexUserTurn, codexSearchCall1, codexSearchOutput1, codexNSCall, codexNSCallOutput, codexSearchCall2, codexSearchOutput2))
	wantTurn3 := wantTurn2 + ",mcp__node_repl__js"
	if got := strings.Join(claudeToolNamesOf(turn3), ","); got != wantTurn3 {
		t.Fatalf("turn 3 tools = %s, want %s", got, wantTurn3)
	}
	for i, tool := range turn2.Get("tools").Array() {
		if got := turn3.Get("tools").Array()[i].Raw; got != tool.Raw {
			t.Fatalf("tool %d changed between turns:\n%s\n%s", i, tool.Raw, got)
		}
	}
}

func TestConvertOpenAIResponsesRequestToClaude_NamespacedFunctionCallHistoryIsQualified(t *testing.T) {
	req := convertToolSearchRequest(t, codexToolSearchRequest(codexUserTurn, codexSearchCall1, codexSearchOutput1, codexNSCall, codexNSCallOutput))
	var replayed gjson.Result
	req.Get("messages").ForEach(func(_, message gjson.Result) bool {
		message.Get("content").ForEach(func(_, block gjson.Result) bool {
			if block.Get("id").String() == "call_cal_1" {
				replayed = block
			}
			return true
		})
		return true
	})
	if got := replayed.Get("name").String(); got != "mcp__codex_apps__calendar_create_event" {
		t.Fatalf("namespaced function_call replayed as %q, want qualified tool name: %s", got, replayed.Raw)
	}
	if !req.Get(`tools.#(name=="mcp__codex_apps__calendar_create_event")`).Exists() {
		t.Fatalf("replayed tool has no matching definition: %s", req.Get("tools").Raw)
	}
}

func TestConvertOpenAIResponsesRequestToClaude_WithoutToolSearchIsUnchanged(t *testing.T) {
	// Golden output captured from the translator before tool_search support.
	request := `{"model":"claude-sonnet-4-5","instructions":"You are Codex.","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"run js"}]},{"type":"function_call","call_id":"call_js_1","name":"js","namespace":"mcp__node_repl__","arguments":"{\"code\":\"1+1\"}"},{"type":"function_call_output","call_id":"call_js_1","output":"2"}],"tools":[` +
		codexExecCommandTool + `,` + codexDeferredTool + `,` + codexDeferredNSTool + `],"tool_choice":"auto","stream":true}`
	want := `{"model":"claude-sonnet-4-5","max_tokens":32000,"messages":[{"role":"user","content":"run js"},{"role":"assistant","content":[{"type":"tool_use","id":"call_js_1","name":"mcp__node_repl__js","input":{"code":"1+1"}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"call_js_1","content":"2"}]}],"metadata":{"user_id":"89d5fa27ec7781d2e5b32a000710e2eba99246913042b9a3b01805bcdc3111ef"},"stream":true,"system":[{"type":"text","text":"You are Codex."}],"tools":[{"name":"exec_command","description":"Run a command.","input_schema":{"properties":{"cmd":{"type":"string"}},"required":["cmd"],"type":"object"}},{"name":"lookup_order","description":"Look up an order.","input_schema":{"properties":{"id":{"type":"string"}},"required":["id"],"type":"object"}},{"name":"mcp__node_repl__js","description":"Run JavaScript.","input_schema":{"properties":{"code":{"type":"string"}},"required":["code"],"type":"object"}}],"tool_choice":{"type":"auto"}}`

	out, err := ConvertOpenAIResponsesRequestToClaude("claude-sonnet-4-5", []byte(request), true)
	if err != nil {
		t.Fatalf("convert: %v", err)
	}
	if string(out) != want {
		t.Fatalf("output changed:\n got: %s\nwant: %s", out, want)
	}
}

// claudeToolSearchStream is a Claude stream with a tool_search call, a
// namespaced tool call and a plain tool call.
var claudeToolSearchStream = []string{
	`data: {"type":"message_start","message":{"id":"msg_ts","usage":{"input_tokens":1,"output_tokens":0}}}`,
	`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_search","name":"tool_search","input":{}}}`,
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"calendar"}}`,
	`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":" create\",\"limit\":8}"}}`,
	`data: {"type":"content_block_stop","index":0}`,
	`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_cal","name":"mcp__codex_apps__calendar_create_event","input":{}}}`,
	`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"title\":\"Standup\"}"}}`,
	`data: {"type":"content_block_stop","index":1}`,
	`data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_exec","name":"exec_command","input":{}}}`,
	`data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"cmd\":\"ls\"}"}}`,
	`data: {"type":"content_block_stop","index":2}`,
	`data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":5}}`,
	`data: {"type":"message_stop"}`,
}

func assertCodexToolSearchCall(t *testing.T, label string, item gjson.Result) {
	t.Helper()
	if item.Get("type").String() != "tool_search_call" || item.Get("call_id").String() != "toolu_search" ||
		item.Get("execution").String() != "client" || item.Get("status").String() != "completed" ||
		item.Get("id").String() != "tsc_toolu_search" {
		t.Fatalf("%s tool_search_call = %s", label, item.Raw)
	}
	if !item.Get("arguments").IsObject() || item.Get("arguments.query").String() != "calendar create" || item.Get("arguments.limit").Int() != 8 {
		t.Fatalf("%s arguments must be an object Codex can parse: %s", label, item.Raw)
	}
}

func assertOrdinaryToolCalls(t *testing.T, label string, calendar, exec gjson.Result) {
	t.Helper()
	if calendar.Get("type").String() != "function_call" || calendar.Get("name").String() != "calendar_create_event" ||
		calendar.Get("namespace").String() != "mcp__codex_apps__" || calendar.Get("arguments").String() != `{"title":"Standup"}` {
		t.Fatalf("%s namespaced call = %s", label, calendar.Raw)
	}
	if exec.Get("type").String() != "function_call" || exec.Get("name").String() != "exec_command" ||
		exec.Get("namespace").Exists() || exec.Get("arguments").String() != `{"cmd":"ls"}` {
		t.Fatalf("%s plain call = %s", label, exec.Raw)
	}
}

func TestConvertClaudeResponseToOpenAIResponses_ToolSearchBecomesToolSearchCall(t *testing.T) {
	request := codexToolSearchRequest(codexUserTurn, codexSearchCall1, codexSearchOutput1)

	var param any
	var added, done []gjson.Result
	var completed gjson.Result
	for _, chunk := range claudeToolSearchStream {
		for _, output := range ConvertClaudeResponseToOpenAIResponses(context.Background(), "claude-test", request, nil, []byte(chunk), &param) {
			event, data := parseClaudeResponsesSSEEvent(t, output)
			switch event {
			case "response.output_item.added":
				added = append(added, data)
			case "response.output_item.done":
				done = append(done, data)
			case "response.function_call_arguments.delta", "response.function_call_arguments.done":
				if data.Get("item_id").String() == "tsc_toolu_search" || data.Get("output_index").Int() == 0 {
					t.Fatalf("tool_search_call must not emit %s: %s", event, data.Raw)
				}
			case "response.completed":
				completed = data
			}
		}
	}

	if len(added) != 3 || len(done) != 3 {
		t.Fatalf("added=%d done=%d, want 3 each", len(added), len(done))
	}
	if item := added[0].Get("item"); item.Get("type").String() != "tool_search_call" || item.Get("call_id").String() != "toolu_search" || item.Get("execution").String() != "client" {
		t.Fatalf("added tool_search_call = %s", item.Raw)
	}
	assertCodexToolSearchCall(t, "done", done[0].Get("item"))
	assertOrdinaryToolCalls(t, "done", done[1].Get("item"), done[2].Get("item"))

	output := completed.Get("response.output").Array()
	if len(output) != 3 {
		t.Fatalf("completed output = %s", completed.Get("response.output").Raw)
	}
	assertCodexToolSearchCall(t, "completed", output[0])
	assertOrdinaryToolCalls(t, "completed", output[1], output[2])
}

func TestConvertClaudeResponseToOpenAIResponsesNonStream_ToolSearchBecomesToolSearchCall(t *testing.T) {
	request := codexToolSearchRequest(codexUserTurn, codexSearchCall1, codexSearchOutput1)
	raw := []byte(strings.Join(claudeToolSearchStream, "\n"))

	out := ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "claude-test", request, nil, raw, nil)
	output := gjson.GetBytes(out, "output").Array()
	if len(output) != 3 {
		t.Fatalf("output = %s", gjson.GetBytes(out, "output").Raw)
	}
	assertCodexToolSearchCall(t, "non-stream", output[0])
	assertOrdinaryToolCalls(t, "non-stream", output[1], output[2])
}

func TestConvertClaudeResponseToOpenAIResponses_TruncatedToolSearchIsIncomplete(t *testing.T) {
	request := codexToolSearchRequest(codexUserTurn)
	start := `data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_search","name":"tool_search","input":{}}}`
	for _, tc := range []struct {
		name          string
		body          []string
		stopReason    string
		wantArguments string
	}{
		{
			name:          "max_tokens mid input",
			body:          []string{start, `data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"calen"}}`},
			stopReason:    "max_tokens",
			wantArguments: `{}`,
		},
		{
			name:          "max_tokens after complete input keeps arguments",
			body:          []string{start, `data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"calendar\"}"}}`, `data: {"type":"content_block_stop","index":0}`},
			stopReason:    "max_tokens",
			wantArguments: `{"query":"calendar"}`,
		},
		{
			name:          "tool_use stop without input",
			body:          []string{start, `data: {"type":"content_block_stop","index":0}`},
			stopReason:    "tool_use",
			wantArguments: `{}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chunks := append([]string{`data: {"type":"message_start","message":{"id":"msg_trunc","usage":{"input_tokens":1,"output_tokens":0}}}`}, tc.body...)
			chunks = append(chunks, `data: {"type":"message_delta","delta":{"stop_reason":"`+tc.stopReason+`"},"usage":{"output_tokens":5}}`, `data: {"type":"message_stop"}`)

			var param any
			var done, terminal gjson.Result
			for _, chunk := range chunks {
				for _, output := range ConvertClaudeResponseToOpenAIResponses(context.Background(), "claude-test", request, nil, []byte(chunk), &param) {
					event, data := parseClaudeResponsesSSEEvent(t, output)
					switch event {
					case "response.output_item.done":
						done = data.Get("item")
					case "response.completed", "response.incomplete":
						terminal = data.Get("response.output.0")
					}
				}
			}
			for label, item := range map[string]gjson.Result{"done": done, "terminal": terminal} {
				if item.Get("type").String() != "tool_search_call" || item.Get("status").String() != "incomplete" ||
					item.Get("arguments").Raw != tc.wantArguments {
					t.Fatalf("%s item = %s, want incomplete tool_search_call with arguments %s", label, item.Raw, tc.wantArguments)
				}
			}
		})
	}
}

func TestConvertOpenAIResponsesRequestToClaude_ToolSearchNameCollisionRoutesConsistently(t *testing.T) {
	userFunction := `{"type":"function","name":"tool_search","description":"User tool.","parameters":{"type":"object","properties":{"x":{"type":"string"}},"required":["x"]}}`
	claudeCall := strings.Join([]string{
		`data: {"type":"message_start","message":{"id":"msg_collide","usage":{"input_tokens":1,"output_tokens":0}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"tool_search","input":{}}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"x\":\"1\"}"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_stop"}`,
	}, "\n")

	// The first declaration wins, as for any duplicate tool name. Request and
	// response agree on the winner, so a call is never misrouted. Deferred
	// tools are hidden only when the client search tool wins; otherwise the
	// model would have neither the deferred tools nor a way to find them.
	for _, tc := range []struct {
		name      string
		tools     string
		wantTools string
		wantType  string
		wantProp  string
	}{
		{"user function first", userFunction + "," + codexToolSearchDeclaration, "tool_search,lookup_order", "function_call", "x"},
		{"client declaration first", codexToolSearchDeclaration + "," + userFunction, "tool_search," + responsesDeferredAnchorName, "tool_search_call", "query"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request := []byte(`{"model":"claude-sonnet-4-5","input":"hi","tools":[` + tc.tools + `,` + codexDeferredTool + `]}`)
			req := convertToolSearchRequest(t, request)
			if got := strings.Join(claudeToolNamesOf(req), ","); got != tc.wantTools {
				t.Fatalf("tools = %s, want %s", got, tc.wantTools)
			}
			if !req.Get("tools.0.input_schema.properties." + tc.wantProp).Exists() {
				t.Fatalf("declared tool = %s, want %s winner", req.Get("tools.0").Raw, tc.wantProp)
			}

			out := ConvertClaudeResponseToOpenAIResponsesNonStream(context.Background(), "claude-test", request, nil, []byte(claudeCall), nil)
			if got := gjson.GetBytes(out, "output.0.type").String(); got != tc.wantType {
				t.Fatalf("Claude call became %s, want %s: %s", got, tc.wantType, out)
			}
		})
	}

	// When the user function wins, nothing is deferred and search output stays
	// text, because the request has no native tool search to expand references.
	t.Run("user function first keeps history as text", func(t *testing.T) {
		request := []byte(`{"model":"claude-sonnet-4-5","input":[` + strings.Join([]string{codexUserTurn, codexSearchCall1, codexSearchOutput1}, ",") +
			`],"tools":[` + userFunction + `,` + codexToolSearchDeclaration + `,` + codexDeferredTool + `]}`)
		req := convertToolSearchRequest(t, request)
		if strings.Contains(req.Raw, `"defer_loading"`) || strings.Contains(req.Raw, `"tool_reference"`) {
			t.Fatalf("user function winner must not use native tool search: %s", req.Raw)
		}
	})
}

func TestConvertOpenAIResponsesRequestToClaude_ToolSearchOutputSkipsDirectlyDeclaredTools(t *testing.T) {
	declaredOnly := `{"type":"tool_search_output","call_id":"call_search_1","status":"completed","execution":"client","tools":[` + codexExecCommandTool + `]}`
	req := convertToolSearchRequest(t, codexToolSearchRequest(codexUserTurn, codexSearchCall1, declaredOnly))
	if got := req.Get("messages.2.content.0.content"); got.Type != gjson.String || got.String() != "Tools now available to call: exec_command." {
		t.Fatalf("declared-only output content = %s", got.Raw)
	}

	mixed := `{"type":"tool_search_output","call_id":"call_search_1","status":"completed","execution":"client","tools":[` + codexExecCommandTool + `,` + codexCalendarNamespace + `]}`
	req = convertToolSearchRequest(t, codexToolSearchRequest(codexUserTurn, codexSearchCall1, mixed))
	want := `[{"type":"tool_reference","tool_name":"mcp__codex_apps__calendar_create_event"}]`
	if got := req.Get("messages.2.content.0.content").Raw; got != want {
		t.Fatalf("mixed output content = %s, want %s", got, want)
	}
	if req.Get(`tools.#(name=="exec_command").defer_loading`).Exists() {
		t.Fatalf("declared exec_command became deferred: %s", req.Get("tools").Raw)
	}
}

func TestConvertOpenAIResponsesRequestToClaude_ToolSearchOutputDeduplicatesReferences(t *testing.T) {
	twice := `{"type":"tool_search_output","call_id":"call_search_1","status":"completed","execution":"client","tools":[` + codexCalendarNamespace + `,` + codexCalendarNamespace + `]}`
	req := convertToolSearchRequest(t, codexToolSearchRequest(codexUserTurn, codexSearchCall1, twice))
	want := `[{"type":"tool_reference","tool_name":"mcp__codex_apps__calendar_create_event"}]`
	if got := req.Get("messages.2.content.0.content").Raw; got != want {
		t.Fatalf("duplicated output content = %s, want %s", got, want)
	}

	rediscovered := `{"type":"tool_search_output","call_id":"call_search_2","status":"completed","execution":"client","tools":[` + codexCalendarNamespace + `]}`
	req = convertToolSearchRequest(t, codexToolSearchRequest(codexUserTurn, codexSearchCall1, codexSearchOutput1, codexSearchCall2, rediscovered))
	if got := req.Get(`tools.#(name=="mcp__codex_apps__calendar_create_event")#`).Array(); len(got) != 1 {
		t.Fatalf("calendar declared %d times: %s", len(got), req.Get("tools").Raw)
	}
	var references []string
	req.Get("messages").ForEach(func(_, message gjson.Result) bool {
		message.Get("content").ForEach(func(_, block gjson.Result) bool {
			block.Get("content").ForEach(func(_, part gjson.Result) bool {
				if part.Get("type").String() == "tool_reference" && part.Get("tool_name").String() == "mcp__codex_apps__calendar_create_event" {
					references = append(references, block.Get("tool_use_id").String())
				}
				return true
			})
			return true
		})
		return true
	})
	if strings.Join(references, ",") != "call_search_1,call_search_2" {
		t.Fatalf("calendar references = %v, want one per search result", references)
	}
}

func TestConvertOpenAIResponsesRequestToClaude_ToolSearchOutputWithoutToolsKeepsText(t *testing.T) {
	empty := `{"type":"tool_search_output","call_id":"call_search_1","status":"completed","execution":"client","tools":[]}`
	req := convertToolSearchRequest(t, codexToolSearchRequest(codexUserTurn, codexSearchCall1, empty))
	if got := req.Get("messages.2.content.0.content"); got.Type != gjson.String || got.String() != "No matching tools were found." {
		t.Fatalf("empty output content = %s", got.Raw)
	}
}

// Deferred tools cannot carry cache_control, so a cache marker on a discovered
// tool definition must not survive into the deferred Claude tool.
func TestConvertOpenAIResponsesRequestToClaude_DeferredToolsDropCacheControl(t *testing.T) {
	cached := `{"type":"tool_search_output","call_id":"call_search_1","status":"completed","execution":"client","tools":[` +
		`{"type":"function","name":"cached_fn","description":"Cached.","cache_control":{"type":"ephemeral"},"defer_loading":true,"parameters":{"type":"object","properties":{}}},` +
		`{"type":"custom","name":"cached_custom","description":"Cached custom.","cache_control":{"type":"ephemeral"},"defer_loading":true}` +
		`]}`
	req := convertToolSearchRequest(t, codexToolSearchRequest(codexUserTurn, codexSearchCall1, cached))
	for _, name := range []string{"cached_fn", "cached_custom"} {
		tool := req.Get(`tools.#(name=="` + name + `")`)
		if !tool.Get("defer_loading").Bool() || tool.Get("cache_control").Exists() {
			t.Fatalf("deferred tool %s = %s, want defer_loading without cache_control", name, tool.Raw)
		}
	}
}

// Anthropic keeps the prompt cache when deferred tools are added, but only once
// the request already carries a deferred tool: going from no deferred tools to
// one misses the cache (measured on claude-opus-5-5, 2026-10-12). Codex hides
// deferred tools until a search returns them, so native tool search always
// declares one constant deferred anchor and discoveries only append after it.
func TestConvertOpenAIResponsesRequestToClaude_DeferredAnchorKeepsToolsPrefixStable(t *testing.T) {
	turn1 := convertToolSearchRequest(t, codexToolSearchRequest(codexUserTurn))
	turn2 := convertToolSearchRequest(t, codexToolSearchRequest(codexUserTurn, codexSearchCall1, codexSearchOutput1))

	wantTurn1 := "exec_command,tool_search," + responsesDeferredAnchorName
	if got := strings.Join(claudeToolNamesOf(turn1), ","); got != wantTurn1 {
		t.Fatalf("turn 1 tools = %s, want %s", got, wantTurn1)
	}
	anchor := turn1.Get(`tools.#(name=="` + responsesDeferredAnchorName + `")`)
	if !anchor.Get("defer_loading").Bool() || anchor.Get("cache_control").Exists() || !anchor.Get("input_schema").IsObject() {
		t.Fatalf("anchor = %s, want a deferred tool without cache_control", anchor.Raw)
	}
	turn1Tools, turn2Tools := turn1.Get("tools").Array(), turn2.Get("tools").Array()
	if len(turn2Tools) <= len(turn1Tools) {
		t.Fatalf("turn 2 tools = %s, want turn 1 tools plus discoveries", turn2.Get("tools").Raw)
	}
	for i, tool := range turn1Tools {
		if turn2Tools[i].Raw != tool.Raw {
			t.Fatalf("tool %d changed after discovery:\n%s\n%s", i, tool.Raw, turn2Tools[i].Raw)
		}
	}
	references := turn2.Get("messages.2.content.0.content").Raw
	if strings.Contains(references, responsesDeferredAnchorName) {
		t.Fatalf("anchor must never be referenced: %s", references)
	}
}

// A client tool whose final Claude name equals the anchor name, directly, after
// name normalization, or as a discovered tool, keeps its declaration and the
// request carries no second tool with that name.
func TestConvertOpenAIResponsesRequestToClaude_DeferredAnchorYieldsToClientToolName(t *testing.T) {
	declared := func(name string) string {
		return `{"type":"function","name":"` + name + `","description":"Client tool.","parameters":{"type":"object","properties":{}}}`
	}
	discovered := `{"type":"tool_search_output","call_id":"call_search_1","status":"completed","execution":"client","tools":[{"type":"function","name":"` +
		responsesDeferredAnchorName + `","description":"Client tool.","defer_loading":true,"parameters":{"type":"object","properties":{}}}]}`
	for name, request := range map[string][]byte{
		"exact declared name": []byte(`{"model":"claude-sonnet-4-5","input":"hi","tools":[` + codexToolSearchDeclaration + `,` + declared(responsesDeferredAnchorName) + `]}`),
		"normalized name":     []byte(`{"model":"claude-sonnet-4-5","input":"hi","tools":[` + codexToolSearchDeclaration + `,` + declared("tool.search.deferred.anchor") + `]}`),
		"discovered tool":     codexToolSearchRequest(codexUserTurn, codexSearchCall1, discovered),
	} {
		req := convertToolSearchRequest(t, request)
		matches := req.Get(`tools.#(name=="` + responsesDeferredAnchorName + `")#`).Array()
		if len(matches) != 1 || matches[0].Get("description").String() != "Client tool." {
			t.Fatalf("%s: tools = %s, want only the client tool under the anchor name", name, req.Get("tools").Raw)
		}
	}
}

// Without native tool search nothing is deferred, so no anchor is declared.
func TestConvertOpenAIResponsesRequestToClaude_NoDeferredAnchorWithoutNativeToolSearch(t *testing.T) {
	userFunction := `{"type":"function","name":"tool_search","parameters":{"type":"object","properties":{"q":{"type":"string"}}}}`
	for name, request := range map[string][]byte{
		"no tool_search":          []byte(`{"model":"claude-sonnet-4-5","input":"hi","tools":[` + codexExecCommandTool + `,` + codexDeferredTool + `]}`),
		"user function wins name": []byte(`{"model":"claude-sonnet-4-5","input":"hi","tools":[` + userFunction + `,` + codexToolSearchDeclaration + `,` + codexDeferredTool + `]}`),
	} {
		req := convertToolSearchRequest(t, request)
		if strings.Contains(req.Get("tools").Raw, responsesDeferredAnchorName) {
			t.Fatalf("%s: anchor declared without native tool search: %s", name, req.Get("tools").Raw)
		}
	}
	out, err := ConvertOpenAIResponsesRequestToClaude("claude-sonnet-4-5", codexToolSearchRequest(codexUserTurn), true)
	if err != nil {
		t.Fatal(err)
	}
	setClientToolSearchEnabledForTest(t, false)
	off, err := ConvertOpenAIResponsesRequestToClaude("claude-sonnet-4-5", codexToolSearchRequest(codexUserTurn), true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), responsesDeferredAnchorName) || strings.Contains(string(off), responsesDeferredAnchorName) {
		t.Fatalf("switch on/off: on=%s off=%s", gjson.GetBytes(out, "tools").Raw, gjson.GetBytes(off, "tools").Raw)
	}
}
