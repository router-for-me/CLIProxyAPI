package claude

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

func TestConvertClaudeRequestToCodex_SystemMessageScenarios(t *testing.T) {
	tests := []struct {
		name             string
		inputJSON        string
		wantHasDeveloper bool
		wantTexts        []string
	}{
		{
			name: "No system field",
			inputJSON: `{
				"model": "claude-3-opus",
				"messages": [{"role": "user", "content": "hello"}]
			}`,
			wantHasDeveloper: false,
		},
		{
			name: "Empty string system field",
			inputJSON: `{
				"model": "claude-3-opus",
				"system": "",
				"messages": [{"role": "user", "content": "hello"}]
			}`,
			wantHasDeveloper: false,
		},
		{
			name: "String system field",
			inputJSON: `{
				"model": "claude-3-opus",
				"system": "Be helpful",
				"messages": [{"role": "user", "content": "hello"}]
			}`,
			wantHasDeveloper: true,
			wantTexts:        []string{"Be helpful"},
		},
		{
			name: "Message system role does not become developer",
			inputJSON: `{
				"model": "claude-3-opus",
				"messages": [
					{"role": "system", "content": "Follow the project instructions"},
					{"role": "user", "content": "hello"}
				]
			}`,
			wantHasDeveloper: false,
		},
		{
			name: "Array system field with filtered billing header",
			inputJSON: `{
				"model": "claude-3-opus",
				"system": [
					{"type": "text", "text": "x-anthropic-billing-header: tenant-123"},
					{"type": "text", "text": "Block 1"},
					{"type": "text", "text": "Block 2"}
				],
				"messages": [{"role": "user", "content": "hello"}]
			}`,
			wantHasDeveloper: true,
			wantTexts:        []string{"Block 1", "Block 2"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ConvertClaudeRequestToCodex("test-model", []byte(tt.inputJSON), false)
			resultJSON := gjson.ParseBytes(result)
			inputs := resultJSON.Get("input").Array()

			hasDeveloper := len(inputs) > 0 && inputs[0].Get("role").String() == "developer"
			if hasDeveloper != tt.wantHasDeveloper {
				t.Fatalf("got hasDeveloper = %v, want %v. Output: %s", hasDeveloper, tt.wantHasDeveloper, resultJSON.Get("input").Raw)
			}

			if !tt.wantHasDeveloper {
				return
			}

			content := inputs[0].Get("content").Array()
			if len(content) != len(tt.wantTexts) {
				t.Fatalf("got %d system content items, want %d. Content: %s", len(content), len(tt.wantTexts), inputs[0].Get("content").Raw)
			}

			for i, wantText := range tt.wantTexts {
				if gotType := content[i].Get("type").String(); gotType != "input_text" {
					t.Fatalf("content[%d] type = %q, want %q", i, gotType, "input_text")
				}
				if gotText := content[i].Get("text").String(); gotText != wantText {
					t.Fatalf("content[%d] text = %q, want %q", i, gotText, wantText)
				}
			}
		})
	}
}

func TestConvertClaudeRequestToCodex_MessageSystemRoleWrapsAsUserReminder(t *testing.T) {
	inputJSON := `{
		"model": "claude-3-opus",
		"system": [{"type": "text", "text": "Top-level rules"}],
		"messages": [
			{"role": "user", "content": "hello"},
			{"role": "system", "content": "Follow the project instructions"},
			{"role": "assistant", "content": [{"type": "text", "text": "ok"}]},
			{"role": "system", "content": [{"type": "text", "text": "Use the current repo"}]}
		]
	}`

	result := ConvertClaudeRequestToCodex("test-model", []byte(inputJSON), false)
	inputs := gjson.GetBytes(result, "input").Array()
	if len(inputs) != 5 {
		t.Fatalf("got %d input items, want 5: %s", len(inputs), gjson.GetBytes(result, "input").Raw)
	}

	if got := inputs[0].Get("role").String(); got != "developer" {
		t.Fatalf("top-level system role = %q, want developer", got)
	}
	if got := inputs[2].Get("role").String(); got != "user" {
		t.Fatalf("message-level system role = %q, want user", got)
	}
	if got := inputs[2].Get("content.0.text").String(); got != "<system-reminder>\nFollow the project instructions\n</system-reminder>" {
		t.Fatalf("unexpected first reminder text: %q", got)
	}
	if got := inputs[4].Get("role").String(); got != "user" {
		t.Fatalf("array message-level system role = %q, want user", got)
	}
	if got := inputs[4].Get("content.0.text").String(); got != "<system-reminder>\nUse the current repo\n</system-reminder>" {
		t.Fatalf("unexpected second reminder text: %q", got)
	}
}

func TestConvertClaudeRequestToCodex_ParallelToolCalls(t *testing.T) {
	tests := []struct {
		name                  string
		inputJSON             string
		wantParallelToolCalls bool
	}{
		{
			name: "Default to true when tool_choice.disable_parallel_tool_use is absent",
			inputJSON: `{
				"model": "claude-3-opus",
				"messages": [{"role": "user", "content": "hello"}]
			}`,
			wantParallelToolCalls: true,
		},
		{
			name: "Disable parallel tool calls when client opts out",
			inputJSON: `{
				"model": "claude-3-opus",
				"tool_choice": {"disable_parallel_tool_use": true},
				"messages": [{"role": "user", "content": "hello"}]
			}`,
			wantParallelToolCalls: false,
		},
		{
			name: "Keep parallel tool calls enabled when client explicitly allows them",
			inputJSON: `{
				"model": "claude-3-opus",
				"tool_choice": {"disable_parallel_tool_use": false},
				"messages": [{"role": "user", "content": "hello"}]
			}`,
			wantParallelToolCalls: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ConvertClaudeRequestToCodex("test-model", []byte(tt.inputJSON), false)
			resultJSON := gjson.ParseBytes(result)

			if got := resultJSON.Get("parallel_tool_calls").Bool(); got != tt.wantParallelToolCalls {
				t.Fatalf("parallel_tool_calls = %v, want %v. Output: %s", got, tt.wantParallelToolCalls, string(result))
			}
		})
	}
}

func TestConvertClaudeRequestToCodex_ServiceTier(t *testing.T) {
	tests := []struct {
		name            string
		serviceTierJSON string
		speedJSON       string
		want            string
		wantExists      bool
	}{
		{
			name:            "Priority passes through",
			serviceTierJSON: `"priority"`,
			want:            "priority",
			wantExists:      true,
		},
		{
			name:            "Fast tier normalizes to priority",
			serviceTierJSON: `"fast"`,
			want:            "priority",
			wantExists:      true,
		},
		{
			name:            "Unsupported tier is omitted",
			serviceTierJSON: `"default"`,
		},
		{
			name:            "Non-string tier is omitted",
			serviceTierJSON: `true`,
		},
		{
			name:       "Fast speed maps to priority",
			speedJSON:  `"fast"`,
			want:       "priority",
			wantExists: true,
		},
		{
			name:      "Standard speed is omitted",
			speedJSON: `"standard"`,
		},
		{
			name:      "Non-string speed is omitted",
			speedJSON: `true`,
		},
		{
			name:            "Fast speed overrides unsupported Anthropic tier",
			serviceTierJSON: `"auto"`,
			speedJSON:       `"fast"`,
			want:            "priority",
			wantExists:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inputJSON := []byte(`{
				"model": "gpt-5.4",
				"messages": [{"role": "user", "content": "Reply with OK"}]
			}`)
			if tt.serviceTierJSON != "" {
				inputJSON, _ = sjson.SetRawBytes(inputJSON, "service_tier", []byte(tt.serviceTierJSON))
			}
			if tt.speedJSON != "" {
				inputJSON, _ = sjson.SetRawBytes(inputJSON, "speed", []byte(tt.speedJSON))
			}

			result := ConvertClaudeRequestToCodex("gpt-5.4", inputJSON, false)
			serviceTierResult := gjson.GetBytes(result, "service_tier")
			if serviceTierResult.Exists() != tt.wantExists {
				t.Fatalf("service_tier exists = %v, want %v. Output: %s", serviceTierResult.Exists(), tt.wantExists, string(result))
			}
			if !tt.wantExists {
				return
			}
			if got := serviceTierResult.String(); got != tt.want {
				t.Fatalf("service_tier = %q, want %q. Output: %s", got, tt.want, string(result))
			}
		})
	}
}

func TestConvertClaudeRequestToCodex_ShortenLongToolUseIDs(t *testing.T) {
	longID := "toolu_" + strings.Repeat("a", 62)
	if len(longID) <= 64 {
		t.Fatalf("test setup error: longID length = %d, want > 64", len(longID))
	}

	inputJSON := `{
		"model": "claude-3-opus",
		"messages": [
			{"role": "user", "content": [{"type":"text","text":"run pwd"}]},
			{"role": "assistant", "content": [
				{"type":"tool_use","id":"` + longID + `","name":"Bash","input":{"cmd":"pwd"}}
			]},
			{"role": "user", "content": [
				{"type":"tool_result","tool_use_id":"` + longID + `","content":"ok"}
			]}
		]
	}`

	result := ConvertClaudeRequestToCodex("test-model", []byte(inputJSON), false)
	inputs := gjson.GetBytes(result, "input").Array()

	var callID string
	var outputCallID string
	for _, item := range inputs {
		switch item.Get("type").String() {
		case "function_call":
			callID = item.Get("call_id").String()
		case "function_call_output":
			outputCallID = item.Get("call_id").String()
		}
	}

	if callID == "" {
		t.Fatalf("missing function_call item. Output: %s", string(result))
	}
	if outputCallID == "" {
		t.Fatalf("missing function_call_output item. Output: %s", string(result))
	}
	if callID != outputCallID {
		t.Fatalf("call_id mismatch: function_call=%q function_call_output=%q. Output: %s", callID, outputCallID, string(result))
	}
	if len(callID) > 64 {
		t.Fatalf("call_id length = %d, want <= 64: %q", len(callID), callID)
	}
	if callID == longID {
		t.Fatalf("long call_id was not shortened: %q", callID)
	}
}

func TestConvertClaudeRequestToCodex_TranslatesDeferredToolDiscovery(t *testing.T) {
	inputJSON := `{
		"model": "claude-3-opus",
		"tools": [
			{
				"name": "ToolSearch",
				"description": "Search for tools",
				"input_schema": {
					"type": "object",
					"properties": {"query": {"type": "string"}},
					"required": ["query"]
				}
			},
			{
				"name": "mcp__calendar__create_event",
				"description": "Create a calendar event",
				"input_schema": {
					"type": "object",
					"properties": {"title": {"type": "string"}},
					"required": ["title"]
				},
				"defer_loading": true
			}
		],
		"messages": [
			{"role": "user", "content": "Create a planning event"},
			{"role": "assistant", "content": [
				{"type": "text", "text": "I will find the calendar tool."},
				{
					"type": "tool_use",
					"id": "toolu_search",
					"name": "ToolSearch",
					"input": {"query": "calendar create"}
				}
			]},
			{"role": "user", "content": [
				{
					"type": "tool_result",
					"tool_use_id": "toolu_search",
					"content": [
						{"type": "tool_reference", "tool_name": "mcp__calendar__create_event"}
					]
				}
			]},
			{"role": "assistant", "content": [
				{
					"type": "tool_use",
					"id": "toolu_event",
					"name": "mcp__calendar__create_event",
					"input": {"title": "Planning"}
				}
			]},
			{"role": "user", "content": [
				{"type": "tool_result", "tool_use_id": "toolu_event", "content": "created"}
			]}
		]
	}`

	result := ConvertClaudeRequestToCodex("test-model", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)

	tools := resultJSON.Get("tools").Array()
	if len(tools) != 1 || tools[0].Get("type").String() != "tool_search" {
		t.Fatalf("top-level tools = %s, want one native tool_search", resultJSON.Get("tools").Raw)
	}
	if got := tools[0].Get("execution").String(); got != "client" {
		t.Fatalf("tool search execution = %q, want client", got)
	}
	if tools[0].Get("name").Exists() {
		t.Fatalf("native tool_search must not have a function name: %s", tools[0].Raw)
	}
	if got := tools[0].Get("description").String(); got != "Search for tools" {
		t.Fatalf("tool search description = %q, want Search for tools", got)
	}
	if got := tools[0].Get("parameters.required.0").String(); got != "query" {
		t.Fatalf("tool search required field = %q, want query", got)
	}

	inputs := resultJSON.Get("input").Array()
	wantTypes := []string{
		"message",
		"message",
		"tool_search_call",
		"tool_search_output",
		"function_call",
		"function_call_output",
	}
	if len(inputs) != len(wantTypes) {
		t.Fatalf("got %d input items, want %d: %s", len(inputs), len(wantTypes), resultJSON.Get("input").Raw)
	}
	for i, wantType := range wantTypes {
		if got := inputs[i].Get("type").String(); got != wantType {
			t.Fatalf("input[%d].type = %q, want %q: %s", i, got, wantType, resultJSON.Get("input").Raw)
		}
	}

	searchCall := inputs[2]
	if got := searchCall.Get("call_id").String(); got != "toolu_search" {
		t.Fatalf("tool search call_id = %q, want toolu_search", got)
	}
	if got := searchCall.Get("execution").String(); got != "client" {
		t.Fatalf("tool search execution = %q, want client", got)
	}
	if got := searchCall.Get("arguments.query").String(); got != "calendar create" {
		t.Fatalf("tool search query = %q, want calendar create", got)
	}

	searchOutput := inputs[3]
	if got := searchOutput.Get("call_id").String(); got != "toolu_search" {
		t.Fatalf("tool search output call_id = %q, want toolu_search", got)
	}
	if got := searchOutput.Get("execution").String(); got != "client" {
		t.Fatalf("tool search output execution = %q, want client", got)
	}
	if got := searchOutput.Get("status").String(); got != "completed" {
		t.Fatalf("tool search output status = %q, want completed", got)
	}
	loadedTools := searchOutput.Get("tools").Array()
	if len(loadedTools) != 1 {
		t.Fatalf("loaded tools = %s, want one tool", searchOutput.Get("tools").Raw)
	}
	if got := loadedTools[0].Get("type").String(); got != "function" {
		t.Fatalf("loaded tool type = %q, want function", got)
	}
	if got := loadedTools[0].Get("name").String(); got != "mcp__calendar__create_event" {
		t.Fatalf("loaded tool name = %q, want mcp__calendar__create_event", got)
	}
	if !loadedTools[0].Get("defer_loading").Bool() {
		t.Fatalf("loaded tool should retain defer_loading: %s", loadedTools[0].Raw)
	}
	if got := loadedTools[0].Get("parameters.required.0").String(); got != "title" {
		t.Fatalf("loaded tool required field = %q, want title", got)
	}

	if got := inputs[4].Get("name").String(); got != "mcp__calendar__create_event" {
		t.Fatalf("function call after discovery = %q, want mcp__calendar__create_event", got)
	}
	if got := inputs[5].Get("call_id").String(); got != "toolu_event" {
		t.Fatalf("function output call_id = %q, want toolu_event", got)
	}
}

func TestConvertClaudeRequestToCodex_PreservesMixedDeferredToolDiscoveryContent(t *testing.T) {
	inputJSON := `{
		"tools": [
			{"name":"ToolSearch","input_schema":{"type":"object"}},
			{"name":"calendar_create","description":"Create event","input_schema":{"type":"object"},"defer_loading":true}
		],
		"messages": [
			{"role":"assistant","content":[{"type":"tool_use","id":"search_1","name":"ToolSearch","input":{"query":"calendar"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"search_1","content":[
				{"type":"text","text":"matched one tool"},
				{"type":"tool_reference","tool_name":"calendar_create"},
				{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGVsbG8="}},
				{"type":"text","text":"ready to call"}
			]}]}
		]
	}`

	result := ConvertClaudeRequestToCodex("test-model", []byte(inputJSON), false)
	inputs := gjson.GetBytes(result, "input").Array()
	wantTypes := []string{"tool_search_call", "tool_search_output", "message"}
	if len(inputs) != len(wantTypes) {
		t.Fatalf("got %d input items, want %d: %s", len(inputs), len(wantTypes), gjson.GetBytes(result, "input").Raw)
	}
	for i, wantType := range wantTypes {
		if got := inputs[i].Get("type").String(); got != wantType {
			t.Fatalf("input[%d].type = %q, want %q: %s", i, got, wantType, gjson.GetBytes(result, "input").Raw)
		}
	}
	if got := inputs[2].Get("role").String(); got != "user" {
		t.Fatalf("residual content role = %q, want user", got)
	}
	residual := inputs[2].Get("content").Array()
	if len(residual) != 3 {
		t.Fatalf("residual content = %s, want three items", inputs[2].Get("content").Raw)
	}
	if got := residual[0].Get("type").String(); got != "input_text" {
		t.Fatalf("residual[0].type = %q, want input_text", got)
	}
	if got := residual[0].Get("text").String(); got != "matched one tool" {
		t.Fatalf("residual[0].text = %q, want matched one tool", got)
	}
	if got := residual[1].Get("type").String(); got != "input_image" {
		t.Fatalf("residual[1].type = %q, want input_image", got)
	}
	if got := residual[1].Get("image_url").String(); got != "data:image/png;base64,aGVsbG8=" {
		t.Fatalf("residual[1].image_url = %q, want data URL", got)
	}
	if got := residual[2].Get("text").String(); got != "ready to call" {
		t.Fatalf("residual[2].text = %q, want ready to call", got)
	}
}

func TestConvertClaudeRequestToCodex_DoesNotPartiallyResolveDeferredTools(t *testing.T) {
	inputJSON := `{
		"tools": [
			{"name":"ToolSearch","input_schema":{"type":"object"}},
			{"name":"known_tool","input_schema":{"type":"object"},"defer_loading":true}
		],
		"messages": [
			{"role":"assistant","content":[{"type":"tool_use","id":"search_1","name":"ToolSearch","input":{"query":"tools"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"search_1","content":[
				{"type":"tool_reference","tool_name":"known_tool"},
				{"type":"tool_reference","tool_name":"missing_tool"}
			]}]}
		]
	}`

	result := ConvertClaudeRequestToCodex("test-model", []byte(inputJSON), false)
	inputs := gjson.GetBytes(result, "input").Array()
	if len(inputs) != 2 || inputs[0].Get("type").String() != "function_call" || inputs[1].Get("type").String() != "function_call_output" {
		t.Fatalf("unresolved discovery should remain an ordinary call/output pair: %s", gjson.GetBytes(result, "input").Raw)
	}
	if strings.Contains(gjson.GetBytes(result, "input").Raw, `"type":"tool_search_output"`) {
		t.Fatalf("unresolved discovery emitted a partial tool_search_output: %s", gjson.GetBytes(result, "input").Raw)
	}
	tools := gjson.GetBytes(result, "tools").Array()
	if len(tools) != 2 || tools[0].Get("type").String() != "function" || tools[1].Get("type").String() != "function" {
		t.Fatalf("unresolved discovery must advertise both tools eagerly: %s", gjson.GetBytes(result, "tools").Raw)
	}
	output := inputs[1].Get("output").String()
	if !strings.Contains(output, "known_tool") || !strings.Contains(output, "missing_tool") {
		t.Fatalf("ordinary fallback output did not preserve references: %q", output)
	}
}

func TestConvertClaudeRequestToCodex_DeferredWebSearchUsesEagerFallback(t *testing.T) {
	inputJSON := `{
		"tools": [
			{"name":"ToolSearch","input_schema":{"type":"object"}},
			{"type":"web_search_20250305","name":"web_search","defer_loading":true}
		],
		"messages": [
			{"role":"assistant","content":[{"type":"tool_use","id":"search_1","name":"ToolSearch","input":{"query":"web"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"search_1","content":[
				{"type":"tool_reference","tool_name":"web_search"}
			]}]}
		]
	}`

	result := ConvertClaudeRequestToCodex("test-model", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)
	tools := resultJSON.Get("tools").Array()
	if len(tools) != 2 {
		t.Fatalf("top-level tools = %s, want ToolSearch plus eager web_search", resultJSON.Get("tools").Raw)
	}
	if got := tools[1].Get("type").String(); got != "web_search" {
		t.Fatalf("deferred built-in type = %q, want eager web_search", got)
	}
	inputs := resultJSON.Get("input").Array()
	if len(inputs) != 2 || inputs[0].Get("type").String() != "tool_search_call" || inputs[1].Get("type").String() != "tool_search_output" {
		t.Fatalf("web-search discovery did not translate: %s", resultJSON.Get("input").Raw)
	}
	if len(inputs[1].Get("tools").Array()) != 0 {
		t.Fatalf("eager web_search should not be duplicated in tool_search_output: %s", inputs[1].Get("tools").Raw)
	}
}

func TestConvertClaudeRequestToCodex_ErrorDiscoveryRemainsOrdinaryToolResult(t *testing.T) {
	inputJSON := `{
		"tools": [
			{"name":"ToolSearch","input_schema":{"type":"object"}},
			{"name":"calendar_create","input_schema":{"type":"object"},"defer_loading":true}
		],
		"messages": [
			{"role":"assistant","content":[{"type":"tool_use","id":"search_1","name":"ToolSearch","input":{"query":"calendar"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"search_1","is_error":true,"content":[
				{"type":"tool_reference","tool_name":"calendar_create"}
			]}]}
		]
	}`

	result := ConvertClaudeRequestToCodex("test-model", []byte(inputJSON), false)
	inputs := gjson.GetBytes(result, "input").Array()
	if len(inputs) != 2 || inputs[0].Get("type").String() != "function_call" || inputs[1].Get("type").String() != "function_call_output" {
		t.Fatalf("error discovery should remain an ordinary call/output pair: %s", gjson.GetBytes(result, "input").Raw)
	}
	if output := inputs[1].Get("output").String(); !strings.Contains(output, "calendar_create") {
		t.Fatalf("error fallback output did not preserve the reference: %q", output)
	}
	tools := gjson.GetBytes(result, "tools").Array()
	if len(tools) != 2 || tools[0].Get("type").String() != "function" || tools[1].Get("type").String() != "function" {
		t.Fatalf("error discovery must advertise both tools eagerly: %s", gjson.GetBytes(result, "tools").Raw)
	}
}

func TestConvertClaudeRequestToCodex_TranslatesDistinctDeferredToolDiscoveries(t *testing.T) {
	inputJSON := `{
		"tools": [
			{"name":"ToolSearch","input_schema":{"type":"object"}},
			{"name":"first_tool","input_schema":{"type":"object"},"defer_loading":true},
			{"name":"second_tool","input_schema":{"type":"object"},"defer_loading":true}
		],
		"messages": [
			{"role":"assistant","content":[{"type":"tool_use","id":"search_1","name":"ToolSearch","input":{"query":"first"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"search_1","content":[{"type":"tool_reference","tool_name":"first_tool"}]}]},
			{"role":"assistant","content":[{"type":"tool_use","id":"search_2","name":"ToolSearch","input":{"query":"second"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"search_2","content":[{"type":"tool_reference","tool_name":"second_tool"}]}]}
		]
	}`

	result := ConvertClaudeRequestToCodex("test-model", []byte(inputJSON), false)
	inputs := gjson.GetBytes(result, "input").Array()
	if len(inputs) != 4 {
		t.Fatalf("got %d items, want two search pairs: %s", len(inputs), gjson.GetBytes(result, "input").Raw)
	}
	for i, callID := range []string{"search_1", "search_2"} {
		callIndex := i * 2
		if inputs[callIndex].Get("type").String() != "tool_search_call" || inputs[callIndex+1].Get("type").String() != "tool_search_output" {
			t.Fatalf("items %d/%d are not a search pair: %s", callIndex, callIndex+1, gjson.GetBytes(result, "input").Raw)
		}
		if got := inputs[callIndex].Get("call_id").String(); got != callID {
			t.Fatalf("search call %d call_id = %q, want %q", i, got, callID)
		}
		if got := inputs[callIndex+1].Get("call_id").String(); got != callID {
			t.Fatalf("search output %d call_id = %q, want %q", i, got, callID)
		}
	}
}

func TestConvertClaudeRequestToCodex_RejectsDuplicateDiscoveryCallID(t *testing.T) {
	inputJSON := `{
		"tools": [
			{"name":"ToolSearch","input_schema":{"type":"object"}},
			{"name":"calendar_create","input_schema":{"type":"object"},"defer_loading":true}
		],
		"messages": [
			{"role":"assistant","content":[
				{"type":"tool_use","id":"duplicate_id","name":"ToolSearch","input":{"query":"first"}},
				{"type":"tool_use","id":"duplicate_id","name":"ToolSearch","input":{"query":"second"}}
			]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"duplicate_id","content":[{"type":"tool_reference","tool_name":"calendar_create"}]}]}
		]
	}`

	result := ConvertClaudeRequestToCodex("test-model", []byte(inputJSON), false)
	inputs := gjson.GetBytes(result, "input").Array()
	if len(inputs) != 3 {
		t.Fatalf("got %d items, want two calls plus one output: %s", len(inputs), gjson.GetBytes(result, "input").Raw)
	}
	for i, item := range inputs {
		if strings.HasPrefix(item.Get("type").String(), "tool_search_") {
			t.Fatalf("ambiguous duplicate call ID emitted tool search item at %d: %s", i, gjson.GetBytes(result, "input").Raw)
		}
	}
	tools := gjson.GetBytes(result, "tools").Array()
	if len(tools) != 2 || tools[0].Get("type").String() != "function" || tools[1].Get("type").String() != "function" {
		t.Fatalf("ambiguous discovery must advertise both tools eagerly: %s", gjson.GetBytes(result, "tools").Raw)
	}
}

func TestConvertClaudeRequestToCodex_RejectsDiscoveryCallIDSharedWithOrdinaryTool(t *testing.T) {
	inputJSON := `{
		"tools": [
			{"name":"ToolSearch","input_schema":{"type":"object"}},
			{"name":"calendar_create","input_schema":{"type":"object"},"defer_loading":true},
			{"name":"lookup","input_schema":{"type":"object"}}
		],
		"messages": [
			{"role":"assistant","content":[
				{"type":"tool_use","id":"shared_id","name":"ToolSearch","input":{"query":"calendar"}},
				{"type":"tool_use","id":"shared_id","name":"lookup","input":{"query":"calendar"}}
			]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"shared_id","content":[{"type":"tool_reference","tool_name":"calendar_create"}]}]}
		]
	}`

	result := ConvertClaudeRequestToCodex("test-model", []byte(inputJSON), false)
	for i, item := range gjson.GetBytes(result, "input").Array() {
		if strings.HasPrefix(item.Get("type").String(), "tool_search_") {
			t.Fatalf("shared call ID emitted tool search item at %d: %s", i, gjson.GetBytes(result, "input").Raw)
		}
	}
	tools := gjson.GetBytes(result, "tools").Array()
	if len(tools) != 3 {
		t.Fatalf("ambiguous discovery must advertise every function eagerly: %s", gjson.GetBytes(result, "tools").Raw)
	}
}

func TestConvertClaudeRequestToCodex_RejectsDiscoveryWithEmptyCallID(t *testing.T) {
	inputJSON := `{
		"tools": [
			{"name":"ToolSearch","input_schema":{"type":"object"}},
			{"name":"calendar_create","input_schema":{"type":"object"},"defer_loading":true}
		],
		"messages": [
			{"role":"assistant","content":[{"type":"tool_use","id":"","name":"ToolSearch","input":{"query":"calendar"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"","content":[{"type":"tool_reference","tool_name":"calendar_create"}]}]}
		]
	}`

	result := ConvertClaudeRequestToCodex("test-model", []byte(inputJSON), false)
	if strings.Contains(gjson.GetBytes(result, "input").Raw, `"type":"tool_search_`) {
		t.Fatalf("empty call ID emitted tool search history: %s", gjson.GetBytes(result, "input").Raw)
	}
	tools := gjson.GetBytes(result, "tools").Array()
	if len(tools) != 2 || tools[0].Get("type").String() != "function" || tools[1].Get("type").String() != "function" {
		t.Fatalf("empty call ID must advertise both tools eagerly: %s", gjson.GetBytes(result, "tools").Raw)
	}
}

func TestConvertClaudeRequestToCodex_RejectsOrphanDiscoveryResult(t *testing.T) {
	inputJSON := `{
		"tools": [
			{"name":"ToolSearch","input_schema":{"type":"object"}},
			{"name":"calendar_create","input_schema":{"type":"object"},"defer_loading":true}
		],
		"messages": [
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"orphan_search","content":[{"type":"tool_reference","tool_name":"calendar_create"}]}]}
		]
	}`

	result := ConvertClaudeRequestToCodex("test-model", []byte(inputJSON), false)
	if strings.Contains(gjson.GetBytes(result, "input").Raw, `"type":"tool_search_`) {
		t.Fatalf("orphan discovery result emitted tool search history: %s", gjson.GetBytes(result, "input").Raw)
	}
	tools := gjson.GetBytes(result, "tools").Array()
	if len(tools) != 2 || tools[0].Get("type").String() != "function" || tools[1].Get("type").String() != "function" {
		t.Fatalf("orphan discovery result must advertise both tools eagerly: %s", gjson.GetBytes(result, "tools").Raw)
	}
}

func TestConvertClaudeRequestToCodex_RejectsDiscoveryResultBeforeCall(t *testing.T) {
	inputJSON := `{
		"tools": [
			{"name":"ToolSearch","input_schema":{"type":"object"}},
			{"name":"calendar_create","input_schema":{"type":"object"},"defer_loading":true}
		],
		"messages": [
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"search_1","content":[{"type":"tool_reference","tool_name":"calendar_create"}]}]},
			{"role":"assistant","content":[{"type":"tool_use","id":"search_1","name":"ToolSearch","input":{"query":"calendar"}}]}
		]
	}`

	result := ConvertClaudeRequestToCodex("test-model", []byte(inputJSON), false)
	for i, item := range gjson.GetBytes(result, "input").Array() {
		if strings.HasPrefix(item.Get("type").String(), "tool_search_") {
			t.Fatalf("reversed discovery emitted tool search item at %d: %s", i, gjson.GetBytes(result, "input").Raw)
		}
	}
	tools := gjson.GetBytes(result, "tools").Array()
	if len(tools) != 2 {
		t.Fatalf("reversed discovery must advertise both functions eagerly: %s", gjson.GetBytes(result, "tools").Raw)
	}
}

func TestConvertClaudeRequestToCodex_RejectsHostedToolNamedToolSearch(t *testing.T) {
	inputJSON := `{
		"tools": [
			{"type":"web_search_20250305","name":"ToolSearch"},
			{"name":"calendar_create","input_schema":{"type":"object"},"defer_loading":true}
		],
		"messages": [
			{"role":"assistant","content":[{"type":"tool_use","id":"search_1","name":"ToolSearch","input":{"query":"calendar"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"search_1","content":[{"type":"tool_reference","tool_name":"calendar_create"}]}]}
		]
	}`

	result := ConvertClaudeRequestToCodex("test-model", []byte(inputJSON), false)
	if strings.Contains(gjson.GetBytes(result, "input").Raw, `"type":"tool_search_`) {
		t.Fatalf("hosted declaration must not activate client tool search: %s", gjson.GetBytes(result, "input").Raw)
	}
	tools := gjson.GetBytes(result, "tools").Array()
	if len(tools) != 2 || tools[0].Get("type").String() != "web_search" || tools[1].Get("name").String() != "calendar_create" {
		t.Fatalf("hosted declaration fallback must retain the deferred function eagerly: %s", gjson.GetBytes(result, "tools").Raw)
	}
}

func TestConvertClaudeRequestToCodex_RejectsTypedToolNamedToolSearch(t *testing.T) {
	inputJSON := `{
		"tools": [
			{"type":"computer_20250124","name":"ToolSearch","display_width_px":1024,"display_height_px":768,"display_number":1},
			{"name":"calendar_create","input_schema":{"type":"object"},"defer_loading":true}
		],
		"messages": [
			{"role":"assistant","content":[{"type":"tool_use","id":"search_1","name":"ToolSearch","input":{"query":"calendar"}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"search_1","content":[{"type":"tool_reference","tool_name":"calendar_create"}]}]}
		]
	}`

	result := ConvertClaudeRequestToCodex("test-model", []byte(inputJSON), false)
	if strings.Contains(gjson.GetBytes(result, "input").Raw, `"type":"tool_search_`) {
		t.Fatalf("typed declaration must not activate client tool search: %s", gjson.GetBytes(result, "input").Raw)
	}
	tools := gjson.GetBytes(result, "tools").Array()
	if len(tools) != 2 || tools[0].Get("type").String() != "function" || tools[1].Get("name").String() != "calendar_create" {
		t.Fatalf("typed declaration fallback must retain deferred functions eagerly: %s", gjson.GetBytes(result, "tools").Raw)
	}
}

func TestConvertClaudeRequestToCodex_DeduplicatesDeferredToolReferences(t *testing.T) {
	inputJSON := `{
		"tools": [
			{"name":"ToolSearch","input_schema":{"type":"object"}},
			{"name":"first_tool","input_schema":{"type":"object"},"defer_loading":true},
			{"name":"second_tool","input_schema":{"type":"object"},"defer_loading":true}
		],
		"messages": [
			{"role":"assistant","content":[{"type":"tool_use","id":"search_1","name":"ToolSearch","input":{}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"search_1","content":[
				{"type":"tool_reference","tool_name":"first_tool"},
				{"type":"tool_reference","tool_name":"second_tool"},
				{"type":"tool_reference","tool_name":"first_tool"}
			]}]}
		]
	}`

	result := ConvertClaudeRequestToCodex("test-model", []byte(inputJSON), false)
	loadedTools := gjson.GetBytes(result, "input.1.tools").Array()
	if len(loadedTools) != 2 {
		t.Fatalf("loaded tools = %s, want two deduplicated tools", gjson.GetBytes(result, "input.1.tools").Raw)
	}
	if loadedTools[0].Get("name").String() != "first_tool" || loadedTools[1].Get("name").String() != "second_tool" {
		t.Fatalf("deduplicated tool order changed: %s", gjson.GetBytes(result, "input.1.tools").Raw)
	}
}

func TestConvertClaudeRequestToCodex_PreservesRawDiscoveryArguments(t *testing.T) {
	tests := []struct {
		name      string
		arguments string
	}{
		{name: "scalar", arguments: `"calendar"`},
		{name: "array", arguments: `["calendar",2]`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inputJSON := `{
				"tools": [
					{"name":"ToolSearch","input_schema":{}},
					{"name":"calendar_create","input_schema":{"type":"object"},"defer_loading":true}
				],
				"messages": [
					{"role":"assistant","content":[{"type":"tool_use","id":"search_1","name":"ToolSearch","input":` + tt.arguments + `}]},
					{"role":"user","content":[{"type":"tool_result","tool_use_id":"search_1","content":[{"type":"tool_reference","tool_name":"calendar_create"}]}]}
				]
			}`

			result := ConvertClaudeRequestToCodex("test-model", []byte(inputJSON), false)
			if got := gjson.GetBytes(result, "input.0.arguments").Raw; got != tt.arguments {
				t.Fatalf("arguments = %s, want %s: %s", got, tt.arguments, gjson.GetBytes(result, "input").Raw)
			}
		})
	}
}

func TestConvertClaudeRequestToCodex_LoadedToolUsesTargetFieldWhitelist(t *testing.T) {
	inputJSON := `{
		"tools": [
			{"name":"ToolSearch","input_schema":{"type":"object"}},
			{
				"name":"calendar_create",
				"description":"Create event",
				"input_schema":{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object"},
				"defer_loading":true,
				"cache_control":{"type":"ephemeral"},
				"allowed_callers":["direct"],
				"input_examples":[{"title":"Planning"}],
				"source_only":"drop me"
			}
		],
		"messages": [
			{"role":"assistant","content":[{"type":"tool_use","id":"search_1","name":"ToolSearch","input":{}}]},
			{"role":"user","content":[{"type":"tool_result","tool_use_id":"search_1","content":[{"type":"tool_reference","tool_name":"calendar_create"}]}]}
		]
	}`

	result := ConvertClaudeRequestToCodex("test-model", []byte(inputJSON), false)
	loadedTool := gjson.GetBytes(result, "input.1.tools.0")
	for _, path := range []string{"cache_control", "allowed_callers", "input_examples", "source_only", "input_schema", "parameters.$schema"} {
		if loadedTool.Get(path).Exists() {
			t.Fatalf("loaded tool leaked %s: %s", path, loadedTool.Raw)
		}
	}
	for _, path := range []string{"type", "name", "description", "parameters", "strict", "defer_loading"} {
		if !loadedTool.Get(path).Exists() {
			t.Fatalf("loaded tool missing target field %s: %s", path, loadedTool.Raw)
		}
	}
}

func TestConvertClaudeRequestToCodex_ToolChoiceModeMapping(t *testing.T) {
	tests := []struct {
		name                string
		claudeToolChoice    string
		wantCodexToolChoice string
	}{
		{
			name:                "Any requires at least one tool",
			claudeToolChoice:    `{"type":"any"}`,
			wantCodexToolChoice: "required",
		},
		{
			name:                "None disables tools",
			claudeToolChoice:    `{"type":"none"}`,
			wantCodexToolChoice: "none",
		},
		{
			name:                "Auto stays auto",
			claudeToolChoice:    `{"type":"auto"}`,
			wantCodexToolChoice: "auto",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inputJSON := `{
				"model": "claude-3-opus",
				"tools": [
					{"name": "lookup", "description": "Lookup", "input_schema": {"type":"object","properties":{}}}
				],
				"tool_choice": ` + tt.claudeToolChoice + `,
				"messages": [{"role": "user", "content": "hello"}]
			}`

			result := ConvertClaudeRequestToCodex("test-model", []byte(inputJSON), false)
			resultJSON := gjson.ParseBytes(result)

			if got := resultJSON.Get("tool_choice").String(); got != tt.wantCodexToolChoice {
				t.Fatalf("tool_choice = %q, want %q. Output: %s", got, tt.wantCodexToolChoice, string(result))
			}
		})
	}
}

func TestConvertClaudeRequestToCodex_ToolChoiceSpecificFunctionUsesConvertedName(t *testing.T) {
	longName := "mcp__server_with_a_very_long_name_that_exceeds_sixty_four_characters__search"
	inputJSON := `{
		"model": "claude-3-opus",
		"tools": [
			{"name": "` + longName + `", "description": "Search", "input_schema": {"type":"object","properties":{}}}
		],
		"tool_choice": {"type":"tool","name":"` + longName + `"},
		"messages": [{"role": "user", "content": "hello"}]
	}`

	result := ConvertClaudeRequestToCodex("test-model", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)

	if got := resultJSON.Get("tool_choice.type").String(); got != "function" {
		t.Fatalf("tool_choice.type = %q, want function. Output: %s", got, string(result))
	}
	toolName := resultJSON.Get("tools.0.name").String()
	choiceName := resultJSON.Get("tool_choice.name").String()
	if choiceName != toolName {
		t.Fatalf("tool_choice.name = %q, want converted tool name %q. Output: %s", choiceName, toolName, string(result))
	}
	if choiceName == longName {
		t.Fatalf("tool_choice.name should use shortened Codex tool name. Output: %s", string(result))
	}
}

func TestConvertClaudeRequestToCodex_ToolChoiceToolSearchUsesNativeType(t *testing.T) {
	inputJSON := `{
		"tools": [
			{"name":"ToolSearch","input_schema":{"type":"object"}},
			{"name":"calendar_create","input_schema":{"type":"object"},"defer_loading":true}
		],
		"tool_choice": {"type":"tool","name":"ToolSearch"},
		"messages": [{"role":"user","content":"find a calendar tool"}]
	}`

	result := ConvertClaudeRequestToCodex("test-model", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)
	if got := resultJSON.Get("tool_choice.type").String(); got != "tool_search" {
		t.Fatalf("tool_choice.type = %q, want tool_search. Output: %s", got, string(result))
	}
	if resultJSON.Get("tool_choice.name").Exists() {
		t.Fatalf("native tool search choice must not carry a name. Output: %s", string(result))
	}
}

func TestConvertClaudeRequestToCodex_ToolChoiceDeferredFunctionUsesEagerFallback(t *testing.T) {
	inputJSON := `{
		"tools": [
			{"name":"ToolSearch","input_schema":{"type":"object"}},
			{"name":"calendar_create","input_schema":{"type":"object"},"defer_loading":true}
		],
		"tool_choice": {"type":"tool","name":"calendar_create"},
		"messages": [{"role":"user","content":"create an event"}]
	}`

	result := ConvertClaudeRequestToCodex("test-model", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)
	tools := resultJSON.Get("tools").Array()
	if len(tools) != 2 || tools[0].Get("type").String() != "function" || tools[1].Get("type").String() != "function" {
		t.Fatalf("forced deferred function must disable native search and advertise all functions: %s", resultJSON.Get("tools").Raw)
	}
	if got := resultJSON.Get("tool_choice.type").String(); got != "function" {
		t.Fatalf("tool_choice.type = %q, want function. Output: %s", got, string(result))
	}
	if got := resultJSON.Get("tool_choice.name").String(); got != tools[1].Get("name").String() {
		t.Fatalf("tool_choice.name = %q, want %q. Output: %s", got, tools[1].Get("name").String(), string(result))
	}
}

func TestConvertClaudeRequestToCodex_WebSearchToolMapping(t *testing.T) {
	inputJSON := `{
		"model": "claude-3-opus",
		"tools": [
			{
				"type": "web_search_20260209",
				"name": "web_search",
				"allowed_domains": ["example.com"],
				"blocked_domains": ["blocked.example"],
				"user_location": {
					"type": "approximate",
					"city": "Beijing",
					"country": "CN",
					"timezone": "Asia/Shanghai"
				}
			}
		],
		"tool_choice": {"type":"tool","name":"web_search"},
		"messages": [{"role": "user", "content": "hello"}]
	}`

	result := ConvertClaudeRequestToCodex("test-model", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)

	if got := resultJSON.Get("tools.0.type").String(); got != "web_search" {
		t.Fatalf("tools.0.type = %q, want web_search. Output: %s", got, string(result))
	}
	if got := resultJSON.Get("tools.0.filters.allowed_domains.0").String(); got != "example.com" {
		t.Fatalf("tools.0.filters.allowed_domains.0 = %q, want example.com. Output: %s", got, string(result))
	}
	if resultJSON.Get("tools.0.blocked_domains").Exists() {
		t.Fatalf("tools.0.blocked_domains should not be forwarded to Codex. Output: %s", string(result))
	}
	if got := resultJSON.Get("tools.0.user_location.city").String(); got != "Beijing" {
		t.Fatalf("tools.0.user_location.city = %q, want Beijing. Output: %s", got, string(result))
	}
	if got := resultJSON.Get("tool_choice.type").String(); got != "web_search" {
		t.Fatalf("tool_choice.type = %q, want web_search. Output: %s", got, string(result))
	}
}

func TestConvertClaudeRequestToCodex_WebSearchToolChoiceUsesDeclaredTypedToolName(t *testing.T) {
	inputJSON := `{
		"model": "claude-opus-4-7",
		"tools": [
			{"type": "web_search_20250305", "name": "browser_search"},
			{"name": "web_search", "description": "Local search", "input_schema": {"type":"object","properties":{}}}
		],
		"tool_choice": {"type":"tool","name":"web_search"},
		"messages": [{"role": "user", "content": "hello"}]
	}`

	result := ConvertClaudeRequestToCodex("test-model", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)

	if got := resultJSON.Get("tool_choice.type").String(); got != "function" {
		t.Fatalf("tool_choice.type = %q, want function. Output: %s", got, string(result))
	}
	if got := resultJSON.Get("tool_choice.name").String(); got != "web_search" {
		t.Fatalf("tool_choice.name = %q, want web_search. Output: %s", got, string(result))
	}
}

func TestConvertClaudeRequestToCodex_AssistantThinkingSignatureToReasoningItem(t *testing.T) {
	signature := validCodexReasoningSignature()
	inputJSON := `{
		"model": "claude-3-opus",
		"messages": [
			{
				"role": "assistant",
				"content": [
					{
						"type": "thinking",
						"thinking": "visible summary must not be replayed",
						"signature": "` + signature + `"
					},
					{
						"type": "text",
						"text": "visible answer"
					}
				]
			},
			{
				"role": "user",
				"content": "continue"
			}
		]
	}`

	result := ConvertClaudeRequestToCodex("test-model", []byte(inputJSON), false)
	resultJSON := gjson.ParseBytes(result)
	inputs := resultJSON.Get("input").Array()
	if len(inputs) != 3 {
		t.Fatalf("got %d input items, want 3. Output: %s", len(inputs), string(result))
	}

	reasoning := inputs[0]
	if got := reasoning.Get("type").String(); got != "reasoning" {
		t.Fatalf("first input type = %q, want reasoning. Output: %s", got, string(result))
	}
	if got := reasoning.Get("encrypted_content").String(); got != signature {
		t.Fatalf("encrypted_content = %q, want %q", got, signature)
	}
	if got := reasoning.Get("summary").Raw; got != "[]" {
		t.Fatalf("summary = %s, want []", got)
	}
	if got := reasoning.Get("content").Raw; got != "null" {
		t.Fatalf("content = %s, want null", got)
	}

	assistantMessage := inputs[1]
	if got := assistantMessage.Get("role").String(); got != "assistant" {
		t.Fatalf("second input role = %q, want assistant. Output: %s", got, string(result))
	}
	if got := assistantMessage.Get("content.0.type").String(); got != "output_text" {
		t.Fatalf("assistant content type = %q, want output_text", got)
	}
	if got := assistantMessage.Get("content.0.text").String(); got != "visible answer" {
		t.Fatalf("assistant text = %q, want visible answer", got)
	}
	if strings.Contains(string(result), "visible summary must not be replayed") {
		t.Fatalf("thinking text should not be replayed into Codex input. Output: %s", string(result))
	}
}

func TestConvertClaudeRequestToCodex_AssistantGrokSignatureToReasoningItem(t *testing.T) {
	signature := "HmlYdr2aCAqCYP/m9mr8PS6KOsdMs72FGDigmydR+Jsmuv8KX97yWPlbOwmXJgWn0CbHaCacdQD3+n5EvpgLfPNmafS3kdICBjRuDf4bzHy7uBiUhNVhqPtp/ee1y9q4imPE4LYgD1VZ4J+bp9mTeqA1+nC9Oue58CiNEMV9SVaGenCD+aBnVuSTzQhD32Y+68i6HLJW0Dx6ifaRfb8hxYtA/sPM+/FTvAMW11nRho5a2BBSkpnzfqqAz/e/vGJ77/bygpXM823QA9wL9i0X"
	payload := []byte(`{"model":"grok-4.5","messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"summary","signature":""},{"type":"text","text":"answer"}]},{"role":"user","content":"next"}]}`)
	payload, _ = sjson.SetBytes(payload, "messages.0.content.0.signature", signature)

	out := ConvertClaudeRequestToCodex("grok-4.5", payload, false)
	reasoning := gjson.GetBytes(out, "input.0")
	if reasoning.Get("type").String() != "reasoning" {
		t.Fatalf("input.0 type = %q, want reasoning; output=%s", reasoning.Get("type").String(), out)
	}
	if got := reasoning.Get("encrypted_content").String(); got != signature {
		t.Fatalf("encrypted_content = %q, want Grok signature", got)
	}
}

func TestConvertClaudeRequestToCodex_IgnoresGrokSignatureForNonGrokTargets(t *testing.T) {
	signature := "HmlYdr2aCAqCYP/m9mr8PS6KOsdMs72FGDigmydR+Jsmuv8KX97yWPlbOwmXJgWn0CbHaCacdQD3+n5EvpgLfPNmafS3kdICBjRuDf4bzHy7uBiUhNVhqPtp/ee1y9q4imPE4LYgD1VZ4J+bp9mTeqA1+nC9Oue58CiNEMV9SVaGenCD+aBnVuSTzQhD32Y+68i6HLJW0Dx6ifaRfb8hxYtA/sPM+/FTvAMW11nRho5a2BBSkpnzfqqAz/e/vGJ77/bygpXM823QA9wL9i0X"
	payload := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"summary","signature":""},{"type":"text","text":"answer"}]},{"role":"user","content":"next"}]}`)
	payload, _ = sjson.SetBytes(payload, "messages.0.content.0.signature", signature)

	for _, modelName := range []string{"gpt-5.4", "claude-sonnet-4-6"} {
		t.Run(modelName, func(t *testing.T) {
			out := ConvertClaudeRequestToCodex(modelName, payload, false)
			if got := countRequestInputItemsByType(out, "reasoning"); got != 0 {
				t.Fatalf("got %d reasoning items for non-Grok target, want 0; output=%s", got, out)
			}
		})
	}
}

func TestConvertClaudeRequestToCodex_IgnoresNonCodexThinkingSignatures(t *testing.T) {
	tests := []struct {
		name      string
		inputJSON string
	}{
		{
			name: "Ignore user thinking even with Codex-shaped signature",
			inputJSON: `{
				"model": "claude-3-opus",
				"messages": [
					{
						"role": "user",
						"content": [
							{
								"type": "thinking",
								"thinking": "user supplied thinking",
								"signature": "` + validCodexReasoningSignature() + `"
							},
							{
								"type": "text",
								"text": "hello"
							}
						]
					}
				]
			}`,
		},
		{
			name: "Ignore Anthropic native signature",
			inputJSON: `{
				"model": "claude-3-opus",
				"messages": [
					{
						"role": "assistant",
						"content": [
							{
								"type": "thinking",
								"thinking": "anthropic thinking",
								"signature": "Eo8Canthropic-state"
							},
							{
								"type": "text",
								"text": "visible answer"
							}
						]
					}
				]
			}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ConvertClaudeRequestToCodex("test-model", []byte(tt.inputJSON), false)
			if got := countRequestInputItemsByType(result, "reasoning"); got != 0 {
				t.Fatalf("got %d reasoning items, want 0. Output: %s", got, string(result))
			}
		})
	}
}

func countRequestInputItemsByType(result []byte, itemType string) int {
	count := 0
	gjson.GetBytes(result, "input").ForEach(func(_, item gjson.Result) bool {
		if item.Get("type").String() == itemType {
			count++
		}
		return true
	})
	return count
}

func validCodexReasoningSignature() string {
	raw := make([]byte, 1+8+16+16+32)
	raw[0] = 0x80
	raw[8] = 1
	return base64.URLEncoding.EncodeToString(raw)
}
