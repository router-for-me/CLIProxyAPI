package claude

import (
	"fmt"
	"strings"
	"testing"

	"github.com/MoeForge/moego/base64"
	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/util"
)

func TestConvertClaudeRequestToCodex_MaxOutputTokens(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"no limit", `{}`, ""},
		{"max_tokens", `{"max_tokens":1024}`, "1024"},
		{"max_output_tokens fallback", `{"max_output_tokens":2048}`, "2048"},
		{"max_tokens takes precedence", `{"max_tokens":1024,"max_output_tokens":2048}`, "1024"},
		{"zero takes precedence", `{"max_tokens":0,"max_output_tokens":2048}`, "0"},
		{"string ignored", `{"max_tokens":"1024"}`, ""},
		{"null ignored", `{"max_tokens":null}`, ""},
		{"boolean ignored", `{"max_tokens":true}`, ""},
		{"object ignored", `{"max_tokens":{}}`, ""},
		{"invalid primary does not use fallback", `{"max_tokens":"1024","max_output_tokens":2048}`, ""},
		{"null primary does not use fallback", `{"max_tokens":null,"max_output_tokens":2048}`, ""},
		{"invalid fallback ignored", `{"max_output_tokens":"2048"}`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, errConvert := ConvertClaudeRequestToCodex("codex-test", []byte(tt.input), false)
			if errConvert != nil {
				t.Fatalf("ConvertClaudeRequestToCodex() error = %v", errConvert)
			}
			if got := ojson.GetBytesNoCopy(out, "max_output_tokens").Raw; got != tt.want {
				t.Fatalf("max_output_tokens = %q, want %q; output=%s", got, tt.want, out)
			}
			if ojson.GetBytesNoCopy(out, "max_tokens").Exists() {
				t.Fatalf("max_tokens should not be forwarded; output=%s", out)
			}
		})
	}
}

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
			result, _ := ConvertClaudeRequestToCodex("test-model", []byte(tt.inputJSON), false)
			resultJSON := util.ParseGJSONBytesNoCopy(result)
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

	result, _ := ConvertClaudeRequestToCodex("test-model", []byte(inputJSON), false)
	inputs := ojson.GetBytesNoCopy(result, "input").Array()
	if len(inputs) != 5 {
		t.Fatalf("got %d input items, want 5: %s", len(inputs), ojson.GetBytesNoCopy(result, "input").Raw)
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

func TestConvertClaudeRequestToCodex_PreservesToolAdjacencyWithInterveningSystemMessage(t *testing.T) {
	inputJSON := `{
		"model": "gpt-5.4",
		"messages": [
			{"role": "user", "content": [{"type": "text", "text": "Execute tools"}]},
			{
				"role": "assistant",
				"content": [
					{"type": "tool_use", "id": "call_1", "name": "tool_one", "input": {"a": 1}},
					{"type": "tool_use", "id": "call_2", "name": "tool_two", "input": {"b": 2}}
				]
			},
			{"role": "system", "content": "Context update between tool call and tool result"},
			{
				"role": "user",
				"content": [
					{"type": "tool_result", "tool_use_id": "call_2", "content": "result 2"},
					{"type": "tool_result", "tool_use_id": "call_1", "content": "result 1"},
					{"type": "text", "text": "Now summarize"}
				]
			}
		]
	}`

	result, _ := ConvertClaudeRequestToCodex("gpt-5.4", []byte(inputJSON), false)
	inputs := ojson.GetBytesNoCopy(result, "input").Array()

	// Expected item types:
	// 0: message (role: user, "Execute tools")
	// 1: function_call (call_id: call_1)
	// 2: function_call (call_id: call_2)
	// 3: function_call_output (call_id: call_1)
	// 4: function_call_output (call_id: call_2)
	// 5: message (role: user, text: <system-reminder>...)
	// 6: message (role: user, text: Now summarize)
	types := make([]string, 0, len(inputs))
	for _, item := range inputs {
		types = append(types, item.Get("type").String())
	}
	wantTypes := []string{"message", "function_call", "function_call", "function_call_output", "function_call_output", "message", "message"}
	if fmt.Sprintf("%v", types) != fmt.Sprintf("%v", wantTypes) {
		t.Fatalf("unexpected types: got %v, want %v", types, wantTypes)
	}

	if inputs[3].Get("call_id").String() != "call_1" {
		t.Fatalf("expected output 0 to respond to call_1, got %q", inputs[3].Get("call_id").String())
	}
	if inputs[4].Get("call_id").String() != "call_2" {
		t.Fatalf("expected output 1 to respond to call_2, got %q", inputs[4].Get("call_id").String())
	}
	if inputs[5].Get("content.0.text").String() != "<system-reminder>\nContext update between tool call and tool result\n</system-reminder>" {
		t.Fatalf("unexpected system reminder content: %q", inputs[5].Get("content.0.text").String())
	}
	if inputs[6].Get("content.0.text").String() != "Now summarize" {
		t.Fatalf("unexpected user summary content: %q", inputs[6].Get("content.0.text").String())
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
			result, _ := ConvertClaudeRequestToCodex("test-model", []byte(tt.inputJSON), false)
			resultJSON := util.ParseGJSONBytesNoCopy(result)

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
			inputDoc := ojson.NewDocString(`{
				"model": "gpt-5.4",
				"messages": [{"role": "user", "content": "Reply with OK"}]
			}`)
			if tt.serviceTierJSON != "" {
				_ = inputDoc.SetRawBytes("service_tier", []byte(tt.serviceTierJSON))
			}
			if tt.speedJSON != "" {
				_ = inputDoc.SetRawBytes("speed", []byte(tt.speedJSON))
			}
			inputJSON := inputDoc.Bytes()

			result, _ := ConvertClaudeRequestToCodex("gpt-5.4", inputJSON, false)
			serviceTierResult := ojson.GetBytesNoCopy(result, "service_tier")
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

	result, _ := ConvertClaudeRequestToCodex("test-model", []byte(inputJSON), false)
	inputs := ojson.GetBytesNoCopy(result, "input").Array()

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

			result, _ := ConvertClaudeRequestToCodex("test-model", []byte(inputJSON), false)
			resultJSON := util.ParseGJSONBytesNoCopy(result)

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

	result, _ := ConvertClaudeRequestToCodex("test-model", []byte(inputJSON), false)
	resultJSON := util.ParseGJSONBytesNoCopy(result)

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

func TestConvertClaudeRequestToCodex_WebSearchSourcesInclude(t *testing.T) {
	tests := []struct {
		name        string
		tools       string
		wantSources bool
	}{
		{name: "no tools"},
		{name: "empty tools", tools: `[]`},
		{name: "ordinary function", tools: `[{"name":"lookup","input_schema":{"type":"object"}}]`},
		{name: "same name function", tools: `[{"name":"web_search","input_schema":{"type":"object"}}]`},
		{name: "unsupported type", tools: `[{"type":"web_search_20990101","name":"web_search"}]`},
		{name: "20250305", tools: `[{"type":"web_search_20250305","name":"web_search"}]`, wantSources: true},
		{name: "20260209", tools: `[{"type":"web_search_20260209","name":"web_search"}]`, wantSources: true},
		{name: "custom name", tools: `[{"type":"web_search_20250305","name":"browser_search"}]`, wantSources: true},
		{name: "nameless search", tools: `[{"type":"web_search_20250305"}]`, wantSources: true},
		{name: "multiple searches and function", tools: `[{"type":"web_search_20250305","name":"search_one"},{"type":"web_search_20260209","name":"search_two"},{"name":"lookup","input_schema":{"type":"object"}}]`, wantSources: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, stream := range []bool{false, true} {
				inputJSON := `{"model":"claude-opus-4-7","messages":[{"role":"user","content":"hello"}]`
				if tt.tools != "" {
					inputJSON += `,"tools":` + tt.tools
				}
				inputJSON += `}`
				result, _ := ConvertClaudeRequestToCodex("test-model", []byte(inputJSON), stream)
				wantInclude := `["reasoning.encrypted_content"]`
				if tt.wantSources {
					wantInclude = `["reasoning.encrypted_content","web_search_call.action.sources"]`
				}
				if got := ojson.GetBytesNoCopy(result, "include").Raw; got != wantInclude {
					t.Errorf("stream=%v: include = %s, want %s", stream, got, wantInclude)
				}
			}
		})
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

	result, _ := ConvertClaudeRequestToCodex("test-model", []byte(inputJSON), false)
	resultJSON := util.ParseGJSONBytesNoCopy(result)

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

	result, _ := ConvertClaudeRequestToCodex("test-model", []byte(inputJSON), false)
	resultJSON := util.ParseGJSONBytesNoCopy(result)

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

	result, _ := ConvertClaudeRequestToCodex("test-model", []byte(inputJSON), false)
	resultJSON := util.ParseGJSONBytesNoCopy(result)
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

func TestConvertClaudeRequestToCodex_PreservesBase64PDFDocumentContent(t *testing.T) {
	inputJSON := `{
		"messages": [{
			"role": "user",
			"content": [
				{"type": "text", "text": "before"},
				{"type": "document", "source": {"type": "base64", "media_type": "application/pdf", "data": "JVBERi0xLjQK"}},
				{"type": "text", "text": "after"}
			]
		}]
	}`

	result, _ := ConvertClaudeRequestToCodex("gpt-5.6-sol", []byte(inputJSON), false)
	content := ojson.GetBytesNoCopy(result, "input.0.content").Array()
	if len(content) != 3 {
		t.Fatalf("got %d content items, want 3. Output: %s", len(content), result)
	}

	wantTypes := []string{"input_text", "input_file", "input_text"}
	for i, wantType := range wantTypes {
		if got := content[i].Get("type").String(); got != wantType {
			t.Fatalf("content[%d].type = %q, want %q. Output: %s", i, got, wantType, result)
		}
	}
	if got := content[0].Get("text").String(); got != "before" {
		t.Fatalf("content[0].text = %q, want %q", got, "before")
	}
	if got := content[1].Get("file_data").String(); got != "data:application/pdf;base64,JVBERi0xLjQK" {
		t.Fatalf("content[1].file_data = %q, want PDF data URL", got)
	}
	if got := content[1].Get("filename").String(); got != "document.pdf" {
		t.Fatalf("content[1].filename = %q, want %q", got, "document.pdf")
	}
	if got := content[2].Get("text").String(); got != "after" {
		t.Fatalf("content[2].text = %q, want %q", got, "after")
	}
}

func TestConvertClaudeRequestToCodex_PreservesContentOrderAcrossToolAndReasoningItems(t *testing.T) {
	signature := validCodexReasoningSignature()
	inputJSON := `{
		"system": "system rules",
		"messages": [
			{"role":"assistant","content":[
				{"type":"text","text":"before reasoning"},
				{"type":"thinking","signature":"` + signature + `"},
				{"type":"text","text":"before tool"},
				{"type":"tool_use","id":"toolu_1","name":"lookup","input":{"query":"test"}},
				{"type":"text","text":"after tool"}
			]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"toolu_1","content":[
					{"type":"text","text":"tool output"},
					{"type":"image","source":{"media_type":"image/png","data":"aW1hZ2U="}}
				]},
				{"type":"text","text":"continue"}
			]}
		],
		"tools": [{"name":"lookup","input_schema":{"type":"object"}}]
	}`

	result, _ := ConvertClaudeRequestToCodex("gpt-5.4", []byte(inputJSON), false)
	inputs := ojson.GetBytesNoCopy(result, "input").Array()
	if len(inputs) != 8 {
		t.Fatalf("got %d input items, want 8. Output: %s", len(inputs), result)
	}

	wantTypes := []string{"message", "message", "reasoning", "message", "function_call", "message", "function_call_output", "message"}
	for i := 0; i < len(wantTypes); i++ {
		if got := inputs[i].Get("type").String(); got != wantTypes[i] {
			t.Fatalf("input[%d].type = %q, want %q. Output: %s", i, got, wantTypes[i], result)
		}
	}

	if got := inputs[1].Get("content.0.text").String(); got != "before reasoning" {
		t.Fatalf("input[1] text = %q, want before reasoning", got)
	}
	if got := inputs[3].Get("content.0.text").String(); got != "before tool" {
		t.Fatalf("input[3] text = %q, want before tool", got)
	}
	if got := inputs[5].Get("content.0.text").String(); got != "after tool" {
		t.Fatalf("input[5] text = %q, want after tool", got)
	}
	if got := inputs[6].Get("output.0.type").String(); got != "input_text" {
		t.Fatalf("tool result output.0.type = %q, want input_text", got)
	}
	if got := inputs[6].Get("output.1.image_url").String(); got != "data:image/png;base64,aW1hZ2U=" {
		t.Fatalf("tool result image_url = %q, want data URL", got)
	}
	if got := inputs[7].Get("content.0.text").String(); got != "continue" {
		t.Fatalf("input[7] text = %q, want continue", got)
	}
}

func TestConvertClaudeRequestToCodex_AssistantGrokSignatureToReasoningItem(t *testing.T) {
	signature := "HmlYdr2aCAqCYP/m9mr8PS6KOsdMs72FGDigmydR+Jsmuv8KX97yWPlbOwmXJgWn0CbHaCacdQD3+n5EvpgLfPNmafS3kdICBjRuDf4bzHy7uBiUhNVhqPtp/ee1y9q4imPE4LYgD1VZ4J+bp9mTeqA1+nC9Oue58CiNEMV9SVaGenCD+aBnVuSTzQhD32Y+68i6HLJW0Dx6ifaRfb8hxYtA/sPM+/FTvAMW11nRho5a2BBSkpnzfqqAz/e/vGJ77/bygpXM823QA9wL9i0X"
	payload := []byte(`{"model":"grok-4.5","messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"summary","signature":""},{"type":"text","text":"answer"}]},{"role":"user","content":"next"}]}`)
	payload, _ = ojson.SetBytes(payload, "messages.0.content.0.signature", signature)

	out, _ := ConvertClaudeRequestToCodex("grok-4.5", payload, false)
	reasoning := ojson.GetBytesNoCopy(out, "input.0")
	if reasoning.Get("type").String() != "reasoning" {
		t.Fatalf("input.0 type = %q, want reasoning; output=%s", reasoning.Get("type").String(), out)
	}
	if got := reasoning.Get("encrypted_content").String(); got != signature {
		t.Fatalf("encrypted_content = %q, want Grok signature", got)
	}
}

func testMetaClaudeSignature() string {
	// Shape-valid synthetic Meta envelope; no captured value is embedded.
	signature := "Q-PaDgE"
	for len(signature) < 1426 {
		signature += "A"
	}
	return signature
}

func TestConvertClaudeRequestToCodex_AssistantMetaSignatureToReasoningItem(t *testing.T) {
	signature := testMetaClaudeSignature()
	payload := []byte(`{"model":"muse-spark-1.3","messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"summary","signature":""},{"type":"text","text":"answer"}]},{"role":"user","content":"next"}]}`)
	payload, _ = ojson.SetBytes(payload, "messages.0.content.0.signature", signature)

	out, _ := ConvertClaudeRequestToCodex("muse-spark-1.3", payload, false)
	reasoning := ojson.GetBytesNoCopy(out, "input.0")
	if reasoning.Get("type").String() != "reasoning" {
		t.Fatalf("input.0 type = %q, want reasoning; output=%s", reasoning.Get("type").String(), out)
	}
	if got := reasoning.Get("encrypted_content").String(); got != signature {
		t.Fatalf("encrypted_content = %q, want Meta signature", got)
	}
}

func TestConvertClaudeRequestToCodex_IgnoresMetaSignatureForNonMetaTargets(t *testing.T) {
	signature := testMetaClaudeSignature()
	payload := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"summary","signature":""},{"type":"text","text":"answer"}]},{"role":"user","content":"next"}]}`)
	payload, _ = ojson.SetBytes(payload, "messages.0.content.0.signature", signature)

	for _, modelName := range []string{"gpt-5.4", "claude-sonnet-4-6", "grok-4.5"} {
		t.Run(modelName, func(t *testing.T) {
			out, _ := ConvertClaudeRequestToCodex(modelName, payload, false)
			if got := countRequestInputItemsByType(out, "reasoning"); got != 0 {
				t.Fatalf("got %d reasoning items for non-Meta target, want 0; output=%s", got, out)
			}
		})
	}
}

func TestConvertClaudeRequestToCodex_AssistantRoutedMetaSignatureToReasoningItem(t *testing.T) {
	// OpenRouter serves Meta Muse models and appends a dot-delimited
	// alphanumeric suffix to the native envelope; the full value must replay
	// verbatim.
	signature := testMetaClaudeSignature() + "." + strings.Repeat("Ab3", 23)[:67]
	payload := []byte(`{"model":"meta/muse-wonder","messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"summary","signature":""},{"type":"text","text":"answer"}]},{"role":"user","content":"next"}]}`)
	payload, _ = ojson.SetBytes(payload, "messages.0.content.0.signature", signature)

	out, _ := ConvertClaudeRequestToCodex("meta/muse-wonder", payload, false)
	reasoning := ojson.GetBytesNoCopy(out, "input.0")
	if reasoning.Get("type").String() != "reasoning" {
		t.Fatalf("input.0 type = %q, want reasoning; output=%s", reasoning.Get("type").String(), out)
	}
	if got := reasoning.Get("encrypted_content").String(); got != signature {
		t.Fatalf("encrypted_content = %q, want routed Meta signature verbatim", got)
	}

	out, _ = ConvertClaudeRequestToCodex("gpt-5.4", payload, false)
	if got := countRequestInputItemsByType(out, "reasoning"); got != 0 {
		t.Fatalf("got %d reasoning items for non-Meta target, want 0; output=%s", got, out)
	}

	// A foreign GPT signature must not be replayed when the target is a Meta Muse model.
	gptPayload := []byte(`{"model":"muse-spark-1.3","messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"summary","signature":""},{"type":"text","text":"answer"}]},{"role":"user","content":"next"}]}`)
	gptPayload, _ = ojson.SetBytes(gptPayload, "messages.0.content.0.signature", validCodexReasoningSignature())
	out, _ = ConvertClaudeRequestToCodex("muse-spark-1.3", gptPayload, false)
	if got := countRequestInputItemsByType(out, "reasoning"); got != 0 {
		t.Fatalf("got %d reasoning items for GPT signature on Meta target, want 0; output=%s", got, out)
	}

	// When modelName is an unmapped upstream deployment alias, the client-requested
	// Meta Muse model in the payload preserves Meta signatures.
	aliasOut, _ := ConvertClaudeRequestToCodex("deployment-123", payload, false)
	if got := countRequestInputItemsByType(aliasOut, "reasoning"); got != 1 {
		t.Fatalf("got %d reasoning items for aliased Meta target, want 1; output=%s", got, aliasOut)
	}
}

func TestConvertClaudeRequestToCodex_ResolvesReplayTargetModelOncePerRequest(t *testing.T) {
	if raceDetectorEnabled {
		t.Skip("allocation budgets are not meaningful with race detector instrumentation")
	}

	const thinkingBlocks = 64
	var body strings.Builder
	body.WriteString(`{"messages":[{"role":"assistant","content":[`)
	for i := 0; i < thinkingBlocks; i++ {
		if i > 0 {
			body.WriteByte(',')
		}
		body.WriteString(`{"type":"thinking","thinking":"step","signature":"x"}`)
	}
	body.WriteString(`,{"type":"text","text":"done"}]}],"model":"MY-DEPLOYMENT"}`)
	payload := []byte(body.String())

	mappedAllocs := testing.AllocsPerRun(20, func() {
		_, _ = ConvertClaudeRequestToCodex("gpt-5", payload, false)
	})
	unmappedAllocs := testing.AllocsPerRun(20, func() {
		_, _ = ConvertClaudeRequestToCodex("my-deployment", payload, false)
	})

	extraAllocs := unmappedAllocs - mappedAllocs
	if extraAllocs > 4 {
		t.Fatalf("unmapped model allocated %v more times than mapped model across %d thinking blocks (unmapped=%v, mapped=%v); expected O(1) target model resolution per request", extraAllocs, thinkingBlocks, unmappedAllocs, mappedAllocs)
	}
}

func TestConvertClaudeRequestToCodex_IgnoresGrokSignatureForNonGrokTargets(t *testing.T) {
	signature := "HmlYdr2aCAqCYP/m9mr8PS6KOsdMs72FGDigmydR+Jsmuv8KX97yWPlbOwmXJgWn0CbHaCacdQD3+n5EvpgLfPNmafS3kdICBjRuDf4bzHy7uBiUhNVhqPtp/ee1y9q4imPE4LYgD1VZ4J+bp9mTeqA1+nC9Oue58CiNEMV9SVaGenCD+aBnVuSTzQhD32Y+68i6HLJW0Dx6ifaRfb8hxYtA/sPM+/FTvAMW11nRho5a2BBSkpnzfqqAz/e/vGJ77/bygpXM823QA9wL9i0X"
	payload := []byte(`{"messages":[{"role":"assistant","content":[{"type":"thinking","thinking":"summary","signature":""},{"type":"text","text":"answer"}]},{"role":"user","content":"next"}]}`)
	payload, _ = ojson.SetBytes(payload, "messages.0.content.0.signature", signature)

	for _, modelName := range []string{"gpt-5.4", "claude-sonnet-4-6"} {
		t.Run(modelName, func(t *testing.T) {
			out, _ := ConvertClaudeRequestToCodex(modelName, payload, false)
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
			result, _ := ConvertClaudeRequestToCodex("test-model", []byte(tt.inputJSON), false)
			if got := countRequestInputItemsByType(result, "reasoning"); got != 0 {
				t.Fatalf("got %d reasoning items, want 0. Output: %s", got, string(result))
			}
		})
	}
}

func countRequestInputItemsByType(result []byte, itemType string) int {
	count := 0
	ojson.GetBytesNoCopy(result, "input").ForEach(func(_, item ojson.Result) bool {
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

func TestConvertClaudeRequestToCodex_OutputConfigFormat(t *testing.T) {
	t.Run("Valid json_schema format", func(t *testing.T) {
		payload := []byte(`{
			"model": "gpt-5.4",
			"max_tokens": 128,
			"messages": [
				{"role": "user", "content": "Return an object with one string field named answer."}
			],
			"output_config": {
				"format": {
					"type": "json_schema",
					"schema": {
						"type": "object",
						"properties": {
							"answer": {"type": "string"}
						},
						"required": ["answer"],
						"additionalProperties": false
					}
				}
			}
		}`)

		translated, _ := ConvertClaudeRequestToCodex("gpt-5.4", payload, false)
		root := util.ParseGJSONBytesNoCopy(translated)

		if !root.Get("text.format").Exists() {
			t.Fatalf("expected text.format in translated payload, got: %s", translated)
		}
		if got := root.Get("text.format.type").String(); got != "json_schema" {
			t.Errorf("expected text.format.type to be 'json_schema', got %q", got)
		}
		if got := root.Get("text.format.name").String(); got != "cli_proxy_structured_output" {
			t.Errorf("expected text.format.name to be 'cli_proxy_structured_output', got %q", got)
		}
		if got := root.Get("text.format.strict").Bool(); !got {
			t.Errorf("expected text.format.strict to be true, got %v", got)
		}
		if got := root.Get("text.format.schema.properties.answer.type").String(); got != "string" {
			t.Errorf("expected schema.properties.answer.type to be 'string', got %q", got)
		}
	})

	t.Run("Valid json_schema format with custom name and strict false", func(t *testing.T) {
		payload := []byte(`{
			"model": "gpt-5.4",
			"messages": [
				{"role": "user", "content": "hello"}
			],
			"output_config": {
				"format": {
					"type": "json_schema",
					"name": "custom_schema",
					"strict": false,
					"schema": {
						"type": "object"
					}
				}
			}
		}`)

		translated, _ := ConvertClaudeRequestToCodex("gpt-5.4", payload, false)
		root := util.ParseGJSONBytesNoCopy(translated)

		if got := root.Get("text.format.name").String(); got != "custom_schema" {
			t.Errorf("expected text.format.name to be 'custom_schema', got %q", got)
		}
		if got := root.Get("text.format.strict").Bool(); got != false {
			t.Errorf("expected text.format.strict to be false, got %v", got)
		}
	})

	t.Run("No output_config.format", func(t *testing.T) {
		payload := []byte(`{
			"model": "gpt-5.4",
			"messages": [
				{"role": "user", "content": "hello"}
			]
		}`)

		translated, _ := ConvertClaudeRequestToCodex("gpt-5.4", payload, false)
		root := util.ParseGJSONBytesNoCopy(translated)
		if root.Get("text.format").Exists() {
			t.Fatalf("expected no text.format in translated payload, got: %s", translated)
		}
	})

	t.Run("output_config with effort only", func(t *testing.T) {
		payload := []byte(`{
			"model": "gpt-5.4",
			"output_config": {"effort": "high"},
			"messages": [
				{"role": "user", "content": "hello"}
			]
		}`)

		translated, _ := ConvertClaudeRequestToCodex("gpt-5.4", payload, false)
		root := util.ParseGJSONBytesNoCopy(translated)
		if root.Get("text.format").Exists() {
			t.Fatalf("expected no text.format in translated payload, got: %s", translated)
		}
		if got := root.Get("reasoning.effort").String(); got != "high" {
			t.Errorf("expected reasoning.effort to be 'high', got %q", got)
		}
	})

	t.Run("json_schema with optional property downgrades strict", func(t *testing.T) {
		payload := []byte(`{
			"model": "gpt-5.4",
			"messages": [
				{"role": "user", "content": "hello"}
			],
			"output_config": {
				"format": {
					"type": "json_schema",
					"name": "cli_proxy_structured_output",
					"strict": true,
					"schema": {
						"type": "object",
						"properties": {
							"answer": {"type": "string"},
							"impossible": {"type": "string"}
						},
						"required": ["answer"],
						"additionalProperties": false
					}
				}
			}
		}`)

		translated, _ := ConvertClaudeRequestToCodex("gpt-5.4", payload, false)
		root := util.ParseGJSONBytesNoCopy(translated)
		if got := root.Get("text.format.strict").Bool(); got != false {
			t.Errorf("expected text.format.strict to be false for non-strict-compatible schema, got %v (%s)", got, translated)
		}
		if got := root.Get("text.format.name").String(); got != "cli_proxy_structured_output" {
			t.Errorf("expected text.format.name to be preserved, got %q", got)
		}
	})

	t.Run("json_schema fully required keeps strict", func(t *testing.T) {
		payload := []byte(`{
			"model": "gpt-5.4",
			"messages": [
				{"role": "user", "content": "hello"}
			],
			"output_config": {
				"format": {
					"type": "json_schema",
					"schema": {
						"type": "object",
						"properties": {
							"answer": {"type": "string"}
						},
						"required": ["answer"],
						"additionalProperties": false
					}
				}
			}
		}`)

		translated, _ := ConvertClaudeRequestToCodex("gpt-5.4", payload, false)
		root := util.ParseGJSONBytesNoCopy(translated)
		if got := root.Get("text.format.strict").Bool(); !got {
			t.Errorf("expected text.format.strict to stay true for strict-compatible schema, got %v (%s)", got, translated)
		}
	})
}

func TestNormalizeToolParameters_StripsNestedSchemaAndId(t *testing.T) {
	input := `{
		"type": "object",
		"$schema": "http://json-schema.org/draft-07/schema#",
		"$id": "https://example.invalid/root",
		"properties": {
			"q": {
				"type": "string",
				"$schema": "http://json-schema.org/draft-07/schema#",
				"$id": "https://example.invalid/q"
			},
			"tags": {
				"type": "array",
				"items": {"type": "string", "$id": "https://example.invalid/tag"}
			},
			"mode": {
				"anyOf": [
					{"type": "string", "$schema": "http://json-schema.org/draft-07/schema#"},
					{"type": "null"}
				]
			},
			"refField": {
				"$ref": "#/$defs/hint"
			}
		},
		"$defs": {
			"hint": {"type": "string", "$id": "https://example.invalid/hint"}
		},
		"required": ["q"]
	}`

	got := normalizeToolParameters(input)
	parsed := ojson.Parse(got)

	if parsed.Get("$schema").Exists() {
		t.Errorf("expected root $schema to be removed, got %v", parsed.Get("$schema").Raw)
	}
	if parsed.Get("$id").Exists() {
		t.Errorf("expected root $id to be removed, got %v", parsed.Get("$id").Raw)
	}
	if parsed.Get("properties.q.$schema").Exists() {
		t.Errorf("expected properties.q.$schema to be removed, got %v", parsed.Get("properties.q.$schema").Raw)
	}
	if parsed.Get("properties.q.$id").Exists() {
		t.Errorf("expected properties.q.$id to be removed, got %v", parsed.Get("properties.q.$id").Raw)
	}
	if parsed.Get("properties.tags.items.$id").Exists() {
		t.Errorf("expected properties.tags.items.$id to be removed, got %v", parsed.Get("properties.tags.items.$id").Raw)
	}
	if parsed.Get("properties.mode.anyOf.0.$schema").Exists() {
		t.Errorf("expected properties.mode.anyOf.0.$schema to be removed, got %v", parsed.Get("properties.mode.anyOf.0.$schema").Raw)
	}
	if parsed.Get("$defs.hint.$id").Exists() {
		t.Errorf("expected $defs.hint.$id to be removed, got %v", parsed.Get("$defs.hint.$id").Raw)
	}
	if parsed.Get("properties.refField.$ref").String() != "#/$defs/hint" {
		t.Errorf("expected $ref to be preserved, got %v", parsed.Get("properties.refField.$ref").Raw)
	}
	if parsed.Get("properties.q.type").String() != "string" {
		t.Errorf("expected properties.q.type to be 'string', got %v", parsed.Get("properties.q.type").Raw)
	}
}

func TestNormalizeToolParameters_PreservesPropertyNamesAndLiteralData(t *testing.T) {
	input := `{
		"type": "object",
		"properties": {
			"$schema": {
				"type": "string",
				"$schema": "http://json-schema.org/draft-07/schema#",
				"$id": "https://example.invalid/sub-schema"
			},
			"$id": {
				"type": "string"
			},
			"config": {
				"type": "object",
				"default": {
					"$id": "default-id-123"
				}
			}
		}
	}`

	got := normalizeToolParameters(input)
	parsed := ojson.Parse(got)

	if !parsed.Get("properties.$schema").Exists() {
		t.Errorf("expected property named '$schema' to be preserved")
	}
	if parsed.Get("properties.$schema.$schema").Exists() {
		t.Errorf("expected properties.$schema.$schema dialect keyword to be removed")
	}
	if parsed.Get("properties.$schema.$id").Exists() {
		t.Errorf("expected properties.$schema.$id dialect keyword to be removed")
	}
	if !parsed.Get("properties.$id").Exists() {
		t.Errorf("expected property named '$id' to be preserved")
	}
	if gotDefaultID := parsed.Get("properties.config.default.$id").String(); gotDefaultID != "default-id-123" {
		t.Errorf("expected literal default.$id to be preserved, got %q", gotDefaultID)
	}

	// Test empty / null / invalid fallback
	if emptyGot := normalizeToolParameters(""); emptyGot != `{"type":"object","properties":{}}` {
		t.Errorf("expected empty string to normalize to empty object schema, got %s", emptyGot)
	}
	if nullGot := normalizeToolParameters("null"); nullGot != `{"type":"object","properties":{}}` {
		t.Errorf("expected null to normalize to empty object schema, got %s", nullGot)
	}

	// Test array union type preservation (e.g. ["object", "null"])
	unionInput := `{"type": ["object", "null"]}`
	unionGot := normalizeToolParameters(unionInput)
	unionParsed := ojson.Parse(unionGot)
	typeArr := unionParsed.Get("type").Array()
	if len(typeArr) != 2 || typeArr[0].String() != "object" || typeArr[1].String() != "null" {
		t.Errorf("expected union type array to be preserved, got %s", unionParsed.Get("type").Raw)
	}
	if !unionParsed.Get("properties").Exists() {
		t.Errorf("expected properties to be added when object is part of union type")
	}
}

func TestConvertClaudeRequestToCodex_StripsNestedToolSchemaMeta(t *testing.T) {
	inputJSON := `{
		"model": "gpt-5",
		"messages": [{"role": "user", "content": "hi"}],
		"tools": [{
			"name": "lookup",
			"description": "Lookup",
			"input_schema": {
				"type": "object",
				"$schema": "http://json-schema.org/draft-07/schema#",
				"properties": {
					"q": {
						"type": "string",
						"$schema": "http://json-schema.org/draft-07/schema#",
						"$id": "https://example.invalid/q"
					},
					"tags": {
						"type": "array",
						"items": {"type": "string", "$id": "https://example.invalid/tag"}
					},
					"mode": {
						"anyOf": [
							{"type": "string", "$schema": "http://json-schema.org/draft-07/schema#"},
							{"type": "null"}
						]
					}
				},
				"$defs": {
					"hint": {"type": "string", "$id": "https://example.invalid/hint"}
				},
				"required": ["q"]
			}
		}]
	}`

	translated, _ := ConvertClaudeRequestToCodex("gpt-5", []byte(inputJSON), false)
	tools := ojson.GetBytesNoCopy(translated, "tools").Array()
	if len(tools) == 0 {
		t.Fatalf("expected tools in translated payload, got: %s", translated)
	}
	params := tools[0].Get("parameters")
	if params.Get("$schema").Exists() {
		t.Errorf("expected root parameters.$schema to be removed, got %v", params.Get("$schema").Raw)
	}
	if params.Get("properties.q.$schema").Exists() {
		t.Errorf("expected parameters.properties.q.$schema to be removed, got %v", params.Get("properties.q.$schema").Raw)
	}
	if params.Get("properties.q.$id").Exists() {
		t.Errorf("expected parameters.properties.q.$id to be removed, got %v", params.Get("properties.q.$id").Raw)
	}
	if params.Get("properties.tags.items.$id").Exists() {
		t.Errorf("expected parameters.properties.tags.items.$id to be removed, got %v", params.Get("properties.tags.items.$id").Raw)
	}
	if params.Get("properties.mode.anyOf.0.$schema").Exists() {
		t.Errorf("expected parameters.properties.mode.anyOf.0.$schema to be removed, got %v", params.Get("properties.mode.anyOf.0.$schema").Raw)
	}
	if params.Get("$defs.hint.$id").Exists() {
		t.Errorf("expected parameters.$defs.hint.$id to be removed, got %v", params.Get("$defs.hint.$id").Raw)
	}
}

func TestConvertClaudeRequestToCodex_StripsUnsupportedUnicodePropertyEscapePatterns(t *testing.T) {
	inputJSON := `{
		"model": "gpt-5.6",
		"messages": [{"role": "user", "content": "hello"}],
		"tools": [{
			"name": "Artifact",
			"description": "Render an HTML file to an Artifact",
			"input_schema": {
				"type": "object",
				"properties": {
					"field": {
						"type": "string",
						"description": "field to replace",
						"pattern": "^(?!__.*__$)[^\\p{Cc}\\p{Cf}\\p{Zl}\\p{Zp}\"\\\\./[\\]]{1,200}$"
					},
					"asset_id": {
						"type": "string",
						"pattern": "^[0-9a-f]{32}$"
					},
					"lookahead_safe": {
						"type": "string",
						"pattern": "^(?!__.*__$).{1,200}$"
					},
					"literal_p": {
						"type": "string",
						"pattern": "^\\\\p{Cc}$"
					},
					"nested": {
						"type": "object",
						"properties": {
							"inner_field": {
								"type": "string",
								"pattern": "\\P{L}+"
							}
						}
					},
					"union_field": {
						"anyOf": [
							{
								"type": "string",
								"pattern": "\\p{N}+"
							},
							{
								"type": "null"
							}
						]
					}
				},
				"required": ["field"]
			}
		}]
	}`

	translated, _ := ConvertClaudeRequestToCodex("gpt-5.6", []byte(inputJSON), false)
	tools := ojson.GetBytesNoCopy(translated, "tools").Array()
	if len(tools) == 0 {
		t.Fatalf("expected tools in translated payload, got: %s", translated)
	}
	params := tools[0].Get("parameters")

	// The \p{...} pattern on field must be removed to avoid Python re failure upstream
	if params.Get("properties.field.pattern").Exists() {
		t.Errorf("expected properties.field.pattern to be removed, got %v", params.Get("properties.field.pattern").Raw)
	}
	// Other attributes on field must be preserved
	if got := params.Get("properties.field.type").String(); got != "string" {
		t.Errorf("expected properties.field.type == 'string', got %q", got)
	}
	if got := params.Get("properties.field.description").String(); got != "field to replace" {
		t.Errorf("expected properties.field.description == 'field to replace', got %q", got)
	}

	// Valid patterns without unescaped \p must be preserved
	if got := params.Get("properties.asset_id.pattern").String(); got != "^[0-9a-f]{32}$" {
		t.Errorf("expected asset_id.pattern preserved, got %q", got)
	}
	if got := params.Get("properties.lookahead_safe.pattern").String(); got != "^(?!__.*__$).{1,200}$" {
		t.Errorf("expected lookahead_safe.pattern preserved, got %q", got)
	}
	if got := params.Get("properties.literal_p.pattern").String(); got != "^\\\\p{Cc}$" {
		t.Errorf("expected literal_p.pattern preserved, got %q", got)
	}

	// Nested object with \P{L}+ must have its pattern removed
	if params.Get("properties.nested.properties.inner_field.pattern").Exists() {
		t.Errorf("expected properties.nested.properties.inner_field.pattern to be removed, got %v", params.Get("properties.nested.properties.inner_field.pattern").Raw)
	}
	if got := params.Get("properties.nested.properties.inner_field.type").String(); got != "string" {
		t.Errorf("expected nested inner_field.type preserved, got %q", got)
	}

	// Union anyOf with \p{N}+ must have its pattern removed
	if params.Get("properties.union_field.anyOf.0.pattern").Exists() {
		t.Errorf("expected union_field.anyOf.0.pattern to be removed, got %v", params.Get("properties.union_field.anyOf.0.pattern").Raw)
	}

	// Required array must be preserved
	if got := params.Get("required.0").String(); got != "field" {
		t.Errorf("expected required.0 == 'field', got %q", got)
	}
}

func TestConvertClaudeRequestToCodex_StripsPatternPropertiesIncompatibleKeys(t *testing.T) {
	inputJSON := `{
		"model": "gpt-5.6",
		"messages": [{"role": "user", "content": "hello"}],
		"tools": [{
			"name": "pattern_tool",
			"input_schema": {
				"type": "object",
				"patternProperties": {
					"^\\\\p{L}+$": {
						"type": "string"
					},
					"^[a-z]+$": {
						"type": "number"
					}
				}
			}
		}]
	}`

	translated, _ := ConvertClaudeRequestToCodex("gpt-5.6", []byte(inputJSON), false)
	tools := ojson.GetBytesNoCopy(translated, "tools").Array()
	if len(tools) == 0 {
		t.Fatalf("expected tools in translated payload, got: %s", translated)
	}
	params := tools[0].Get("parameters")

	patternProps := params.Get("patternProperties").Map()
	if _, exists := patternProps[`^\p{L}+$`]; exists {
		t.Errorf("expected patternProperties key '^\\\\p{L}+$' to be removed, got: %s", params.Get("patternProperties").Raw)
	}
	if _, exists := patternProps[`^[a-z]+$`]; !exists {
		t.Errorf("expected patternProperties key '^[a-z]+$' to be preserved, got: %s", params.Get("patternProperties").Raw)
	}
}

func TestConvertClaudeRequestToCodex_MidConversationToolAdditions(t *testing.T) {
	longMCPTool := "mcp__very_long_server_name_for_testing__deliver_final_subagent_handback_report"
	inputJSON := `{
		"model": "gpt-6.1-sol",
		"tools": [
			{
				"name": "Read",
				"description": "Read a file",
				"input_schema": {"type": "object", "properties": {"path": {"type": "string"}}}
			},
			{
				"name": "deferred_lookup",
				"description": "Look up deferred notes",
				"defer_loading": true,
				"input_schema": {"type": "object", "properties": {"query": {"type": "string"}}, "required": ["query"]}
			}
		],
		"messages": [
			{"role": "user", "content": "Start task"},
			{
				"role": "system",
				"content": [
					{"type": "text", "text": "<system-reminder>SubagentHandback is now available</system-reminder>"},
					{
						"type": "tool_addition",
						"tool": {
							"type": "tool_definition",
							"definition": {
								"name": "SubagentHandback",
								"description": "Deliver subagent report",
								"input_schema": {
									"$schema": "https://json-schema.org/draft/2020-12/schema",
									"type": "object",
									"properties": {"summary": {"type": "string"}},
									"required": ["summary"]
								}
							}
						}
					},
					{
						"type": "tool_addition",
						"tool": {"type": "tool_reference", "name": "deferred_lookup"}
					},
					{
						"type": "tool_removal",
						"tool": {"type": "tool_reference", "name": "Read"}
					}
				]
			},
			{
				"role": "assistant",
				"content": [
					{"type": "tool_use", "id": "call_1", "name": "Read", "input": {"path": "README.md"}}
				]
			},
			{
				"role": "system",
				"content": [
					{
						"type": "tool_addition",
						"tool": {
							"type": "tool_definition",
							"definition": {
								"name": "` + longMCPTool + `",
								"description": "Long MCP handback tool",
								"input_schema": {"type": "object"}
							}
						}
					}
				]
			},
			{
				"role": "user",
				"content": [
					{"type": "tool_result", "tool_use_id": "call_1", "content": "file contents"},
					{"type": "text", "text": "Finish up"}
				]
			}
		]
	}`

	translated, err := ConvertClaudeRequestToCodex("gpt-6.1-sol", []byte(inputJSON), false)
	if err != nil {
		t.Fatalf("ConvertClaudeRequestToCodex returned error: %v", err)
	}

	// Top-level tools[] must include only non-deferred tools (Read) to preserve the prompt cache prefix.
	topTools := ojson.GetBytesNoCopy(translated, "tools").Array()
	if len(topTools) != 1 {
		t.Fatalf("expected 1 top-level tool, got %d: %s", len(topTools), ojson.GetBytesNoCopy(translated, "tools").Raw)
	}
	if topTools[0].Get("name").String() != "Read" {
		t.Fatalf("unexpected top-level tool name: %q", topTools[0].Get("name").String())
	}

	// Expected input items:
	// 0: message (user: "Start task")
	// 1: message (user: "<system-reminder>...")
	// 2: additional_tools (developer: SubagentHandback, deferred_lookup)
	// 3: function_call (call_1)
	// 4: function_call_output (call_1)
	// 5: additional_tools (developer: shortened longMCPTool, deferred after function_call_output)
	// 6: message (user: "Finish up")
	inputs := ojson.GetBytesNoCopy(translated, "input").Array()
	if len(inputs) != 7 {
		t.Fatalf("expected 7 input items, got %d: %s", len(inputs), ojson.GetBytesNoCopy(translated, "input").Raw)
	}

	addTools1 := inputs[2]
	if got := addTools1.Get("type").String(); got != "additional_tools" {
		t.Fatalf("inputs[2].type = %q, want additional_tools: %s", got, addTools1.Raw)
	}
	if got := addTools1.Get("role").String(); got != "developer" {
		t.Fatalf("inputs[2].role = %q, want developer: %s", got, addTools1.Raw)
	}
	addTools1List := addTools1.Get("tools").Array()
	if len(addTools1List) != 2 {
		t.Fatalf("expected 2 tools in first additional_tools, got %d: %s", len(addTools1List), addTools1.Raw)
	}
	if addTools1List[0].Get("type").String() != "function" || addTools1List[0].Get("name").String() != "SubagentHandback" {
		t.Fatalf("unexpected first additional tool: %s", addTools1List[0].Raw)
	}
	if addTools1List[0].Get("strict").Type != ojson.False {
		t.Fatalf("expected strict=false on SubagentHandback, got: %s", addTools1List[0].Raw)
	}
	if addTools1List[0].Get("input_schema").Exists() || addTools1List[0].Get("parameters.$schema").Exists() {
		t.Fatalf("expected input_schema and parameters.$schema to be stripped: %s", addTools1List[0].Raw)
	}
	if addTools1List[0].Get("parameters.properties.summary.type").String() != "string" {
		t.Fatalf("expected normalized parameters on SubagentHandback, got: %s", addTools1List[0].Raw)
	}
	if addTools1List[1].Get("type").String() != "function" || addTools1List[1].Get("name").String() != "deferred_lookup" {
		t.Fatalf("unexpected second additional tool (resolved tool_reference): %s", addTools1List[1].Raw)
	}
	if addTools1List[1].Get("defer_loading").Exists() {
		t.Fatalf("expected defer_loading to be stripped on resolved tool_reference: %s", addTools1List[1].Raw)
	}

	if inputs[3].Get("type").String() != "function_call" || inputs[4].Get("type").String() != "function_call_output" {
		t.Fatalf("expected function_call (3) followed by function_call_output (4), got %q and %q", inputs[3].Get("type").String(), inputs[4].Get("type").String())
	}
	addTools2 := inputs[5]
	if got := addTools2.Get("type").String(); got != "additional_tools" {
		t.Fatalf("inputs[5].type = %q, want additional_tools: %s", got, addTools2.Raw)
	}
	shortLongName := addTools2.Get("tools.0.name").String()
	if shortLongName == "" || shortLongName == longMCPTool || len(shortLongName) > 64 {
		t.Fatalf("expected shortened MCP tool name <= 64 chars, got %q (len=%d)", shortLongName, len(shortLongName))
	}
}

func TestConvertClaudeRequestToCodex_ToolSearchDeferredLoadingPreservesPromptCachePrefix(t *testing.T) {
	beforeDiscoveryJSON := `{
		"model": "gpt-6-astra",
		"system": "You are Claude Code.",
		"tools": [
			{
				"name": "ToolSearch",
				"description": "Search deferred tools",
				"input_schema": {
					"type": "object",
					"properties": {
						"query": {"type": "string"},
						"max_results": {"type": "number"}
					},
					"required": ["query"]
				}
			},
			{
				"name": "Workflow",
				"description": "Run workflow",
				"input_schema": {"type": "object", "properties": {"name": {"type": "string"}}}
			},
			{
				"name": "DeferredToolPlaceholder",
				"description": "Placeholder for deferred tools",
				"defer_loading": true,
				"input_schema": {"type": "object", "properties": {}}
			},
			{
				"name": "Write",
				"description": "Write file",
				"input_schema": {"type": "object", "properties": {"path": {"type": "string"}}}
			}
		],
		"messages": [
			{"role": "user", "content": "Load WebSearch and WebFetch"},
			{
				"role": "assistant",
				"content": [
					{
						"type": "tool_use",
						"id": "toolu_search_1",
						"name": "ToolSearch",
						"input": {"query": "select:WebSearch,WebFetch", "max_results": 2}
					}
				]
			}
		]
	}`

	afterDiscoveryJSON := `{
		"model": "gpt-6-astra",
		"system": "You are Claude Code.",
		"tools": [
			{
				"name": "ToolSearch",
				"description": "Search deferred tools",
				"input_schema": {
					"type": "object",
					"properties": {
						"query": {"type": "string"},
						"max_results": {"type": "number"}
					},
					"required": ["query"]
				}
			},
			{
				"name": "WebFetch",
				"description": "Fetch a URL",
				"defer_loading": true,
				"input_schema": {
					"$schema": "https://json-schema.org/draft/2020-12/schema",
					"type": "object",
					"properties": {"url": {"type": "string"}},
					"required": ["url"]
				}
			},
			{
				"name": "WebSearch",
				"description": "Search the web",
				"defer_loading": true,
				"input_schema": {
					"$schema": "https://json-schema.org/draft/2020-12/schema",
					"type": "object",
					"properties": {"query": {"type": "string"}},
					"required": ["query"]
				}
			},
			{
				"name": "Workflow",
				"description": "Run workflow",
				"input_schema": {"type": "object", "properties": {"name": {"type": "string"}}}
			},
			{
				"name": "DeferredToolPlaceholder",
				"description": "Placeholder for deferred tools",
				"defer_loading": true,
				"input_schema": {"type": "object", "properties": {}}
			},
			{
				"name": "Write",
				"description": "Write file",
				"input_schema": {"type": "object", "properties": {"path": {"type": "string"}}}
			}
		],
		"messages": [
			{"role": "user", "content": "Load WebSearch and WebFetch"},
			{
				"role": "assistant",
				"content": [
					{
						"type": "tool_use",
						"id": "toolu_search_1",
						"name": "ToolSearch",
						"input": {"query": "select:WebSearch,WebFetch", "max_results": 2}
					}
				]
			},
			{
				"role": "user",
				"content": [
					{
						"type": "tool_result",
						"tool_use_id": "toolu_search_1",
						"content": [
							{"type": "tool_reference", "tool_name": "WebSearch"},
							{"type": "tool_reference", "tool_name": "WebFetch"},
							{"type": "tool_reference", "tool_name": "WebSearch"}
						]
					}
				]
			}
		]
	}`

	beforeOut, err := ConvertClaudeRequestToCodex("gpt-6-astra", []byte(beforeDiscoveryJSON), false)
	if err != nil {
		t.Fatalf("beforeDiscovery ConvertClaudeRequestToCodex error: %v", err)
	}
	afterOut, err := ConvertClaudeRequestToCodex("gpt-6-astra", []byte(afterDiscoveryJSON), false)
	if err != nil {
		t.Fatalf("afterDiscovery ConvertClaudeRequestToCodex error: %v", err)
	}

	beforeToolsRaw := ojson.GetBytesNoCopy(beforeOut, "tools").Raw
	afterToolsRaw := ojson.GetBytesNoCopy(afterOut, "tools").Raw
	if beforeToolsRaw != afterToolsRaw {
		t.Fatalf("top-level tools[] changed after ToolSearch discovery:\nbefore: %s\nafter:  %s", beforeToolsRaw, afterToolsRaw)
	}

	topTools := ojson.GetBytesNoCopy(afterOut, "tools").Array()
	if len(topTools) != 3 {
		t.Fatalf("expected 3 non-deferred top-level tools (ToolSearch, Workflow, Write), got %d: %s", len(topTools), afterToolsRaw)
	}
	wantTopNames := []string{"ToolSearch", "Workflow", "Write"}
	for i, wantName := range wantTopNames {
		if got := topTools[i].Get("name").String(); got != wantName {
			t.Fatalf("topTools[%d].name = %q, want %q", i, got, wantName)
		}
	}

	beforeInputs := ojson.GetBytesNoCopy(beforeOut, "input").Array()
	afterInputs := ojson.GetBytesNoCopy(afterOut, "input").Array()
	if len(beforeInputs) != 3 {
		t.Fatalf("expected 3 input items before discovery, got %d: %s", len(beforeInputs), ojson.GetBytesNoCopy(beforeOut, "input").Raw)
	}
	if len(afterInputs) != 5 {
		t.Fatalf("expected 5 input items after discovery (developer, user, function_call, function_call_output, additional_tools), got %d: %s", len(afterInputs), ojson.GetBytesNoCopy(afterOut, "input").Raw)
	}
	for i := 0; i < len(beforeInputs); i++ {
		if beforeInputs[i].Raw != afterInputs[i].Raw {
			t.Fatalf("input[%d] prefix changed after discovery:\nbefore: %s\nafter:  %s", i, beforeInputs[i].Raw, afterInputs[i].Raw)
		}
	}

	if got := afterInputs[3].Get("type").String(); got != "function_call_output" {
		t.Fatalf("afterInputs[3].type = %q, want function_call_output", got)
	}
	addTools := afterInputs[4]
	if got := addTools.Get("type").String(); got != "additional_tools" {
		t.Fatalf("afterInputs[4].type = %q, want additional_tools: %s", got, addTools.Raw)
	}
	if got := addTools.Get("role").String(); got != "developer" {
		t.Fatalf("afterInputs[4].role = %q, want developer: %s", got, addTools.Raw)
	}
	discovered := addTools.Get("tools").Array()
	if len(discovered) != 2 {
		t.Fatalf("expected 2 deduplicated discovered tools in additional_tools, got %d: %s", len(discovered), addTools.Raw)
	}
	if got := discovered[0].Get("name").String(); got != "WebSearch" {
		t.Fatalf("discovered[0].name = %q, want WebSearch (preserving reference order)", got)
	}
	if got := discovered[1].Get("name").String(); got != "WebFetch" {
		t.Fatalf("discovered[1].name = %q, want WebFetch (preserving reference order)", got)
	}
	for i, tool := range discovered {
		if tool.Get("defer_loading").Exists() {
			t.Fatalf("discovered[%d] still has defer_loading: %s", i, tool.Raw)
		}
		if tool.Get("input_schema").Exists() || tool.Get("parameters.$schema").Exists() {
			t.Fatalf("discovered[%d] still has unnormalized schema fields: %s", i, tool.Raw)
		}
		if tool.Get("strict").Type != ojson.False {
			t.Fatalf("discovered[%d] expected strict=false: %s", i, tool.Raw)
		}
	}
}

func TestConvertClaudeRequestToCodex_RepeatedToolSearchAndLongMCPToolMapping(t *testing.T) {
	// Two >64-char MCP tools that collide under shortenNameIfNeeded ("mcp__shared-long-server_deliver_final_subagent_handback_report"):
	// eagerLongTool is non-deferred; deferredLongTool is deferred and inserted earlier in tools[] after discovery.
	eagerLongTool := "mcp__shared-long-server-11111111-2222-3333-4444-555555555555_deliver_final_subagent_handback_report"
	deferredLongTool := "mcp__shared-long-server-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee_deliver_final_subagent_handback_report"

	beforeJSON := fmt.Sprintf(`{
		"model": "gpt-6-astra",
		"tools": [
			{"name": "ToolSearch", "input_schema": {"type": "object"}},
			{"name": %q, "description": "Eager MCP tool", "input_schema": {"type": "object"}}
		],
		"messages": [
			{"role": "user", "content": "Search tools"}
		]
	}`, eagerLongTool)

	afterJSON := fmt.Sprintf(`{
		"model": "gpt-6-astra",
		"tools": [
			{"name": "ToolSearch", "input_schema": {"type": "object"}},
			{"name": %q, "description": "Deferred MCP tool inserted before eager tool", "defer_loading": true, "input_schema": {"type": "object"}},
			{"name": %q, "description": "Eager MCP tool", "input_schema": {"type": "object"}}
		],
		"messages": [
			{"role": "user", "content": "Search tools"},
			{
				"role": "assistant",
				"content": [
					{"type": "tool_use", "id": "call_ts_1", "name": "ToolSearch", "input": {"query": "action"}}
				]
			},
			{
				"role": "user",
				"content": [
					{
						"type": "tool_result",
						"tool_use_id": "call_ts_1",
						"content": [
							{"type": "tool_reference", "tool_name": %q}
						]
					}
				]
			},
			{
				"role": "assistant",
				"content": [
					{"type": "tool_use", "id": "call_ts_2", "name": "ToolSearch", "input": {"query": "action again"}},
					{"type": "tool_use", "id": "call_mcp_1", "name": %q, "input": {}}
				]
			},
			{
				"role": "user",
				"content": [
					{
						"type": "tool_result",
						"tool_use_id": "call_ts_2",
						"content": [
							{"type": "tool_reference", "tool_name": %q}
						]
					},
					{
						"type": "tool_result",
						"tool_use_id": "call_mcp_1",
						"content": "done"
					}
				]
			}
		]
	}`, deferredLongTool, eagerLongTool, deferredLongTool, deferredLongTool, deferredLongTool)

	beforeOut, err := ConvertClaudeRequestToCodex("gpt-6-astra", []byte(beforeJSON), false)
	if err != nil {
		t.Fatalf("before ConvertClaudeRequestToCodex error: %v", err)
	}
	afterOut, err := ConvertClaudeRequestToCodex("gpt-6-astra", []byte(afterJSON), false)
	if err != nil {
		t.Fatalf("after ConvertClaudeRequestToCodex error: %v", err)
	}

	// Top-level tools[] must stay byte-identical even when a colliding deferred tool is inserted earlier in tools[].
	if gotBefore, gotAfter := ojson.GetBytesNoCopy(beforeOut, "tools").Raw, ojson.GetBytesNoCopy(afterOut, "tools").Raw; gotBefore != gotAfter {
		t.Fatalf("top-level tools[] changed when colliding deferred tool was inserted:\nbefore: %s\nafter:  %s", gotBefore, gotAfter)
	}

	inputs := ojson.GetBytesNoCopy(afterOut, "input").Array()
	// Count additional_tools items: only the first ToolSearch result should emit additional_tools;
	// the second ToolSearch result references the same already-loaded tool and must not emit a duplicate additional_tools item.
	var addToolsItems []ojson.Result
	for _, item := range inputs {
		if item.Get("type").String() == "additional_tools" {
			addToolsItems = append(addToolsItems, item)
		}
	}
	if len(addToolsItems) != 1 {
		t.Fatalf("expected exactly 1 additional_tools item across repeated references, got %d: %s", len(addToolsItems), ojson.GetBytesNoCopy(afterOut, "input").Raw)
	}

	deferredShortName := addToolsItems[0].Get("tools.0.name").String()
	eagerShortName := ojson.GetBytesNoCopy(afterOut, "tools.1.name").String()
	if deferredShortName == "" || deferredShortName == deferredLongTool || len(deferredShortName) > 64 {
		t.Fatalf("expected shortened deferred tool name <= 64 chars, got %q", deferredShortName)
	}
	if deferredShortName == eagerShortName {
		t.Fatalf("expected colliding deferred and eager tools to have distinct short names, both got %q", deferredShortName)
	}

	if !strings.HasSuffix(deferredShortName, "_1") {
		t.Fatalf("expected deferred colliding tool to receive _1 suffix so eager tool keeps base name, got eager=%q deferred=%q", eagerShortName, deferredShortName)
	}

	// Verify the assistant tool_use for deferredLongTool and the response reverse map use the same shortened name.
	if gotCallName := ojson.GetBytesNoCopy(afterOut, `input.#(call_id=="call_mcp_1").name`).String(); gotCallName != deferredShortName {
		t.Fatalf("function_call name = %q, want %q", gotCallName, deferredShortName)
	}
	revMap := buildReverseMapFromClaudeOriginalShortToOriginal([]byte(afterJSON))
	if gotOrig := revMap[deferredShortName]; gotOrig != deferredLongTool {
		t.Fatalf("reverse map[%q] = %q, want %q", deferredShortName, gotOrig, deferredLongTool)
	}
	if gotOrig := revMap[eagerShortName]; gotOrig != eagerLongTool {
		t.Fatalf("reverse map[%q] = %q, want %q", eagerShortName, gotOrig, eagerLongTool)
	}
	// Verify function_call_output.output also rewrote tool_reference.tool_name to the shortened name.
	var ts1Output string
	for _, item := range inputs {
		if item.Get("type").String() == "function_call_output" && item.Get("call_id").String() == "call_ts_1" {
			ts1Output = item.Get("output").String()
			break
		}
	}
	if !strings.Contains(ts1Output, deferredShortName) {
		t.Fatalf("expected function_call_output.output to use shortened tool_name %q, got %s", deferredShortName, ts1Output)
	}
}

func TestConvertClaudeRequestToCodex_ToolResultMixedContentAndUnknownToolReference(t *testing.T) {
	inputJSON := `{
		"model": "gpt-6-astra",
		"tools": [
			{"name": "ToolSearch", "input_schema": {"type": "object"}},
			{"name": "WebSearch", "defer_loading": true, "input_schema": {"type": "object", "properties": {"query": {"type": "string"}}}}
		],
		"messages": [
			{"role": "user", "content": "Find tools"},
			{
				"role": "assistant",
				"content": [
					{"type": "tool_use", "id": "call_1", "name": "ToolSearch", "input": {"query": "web"}}
				]
			},
			{
				"role": "user",
				"content": [
					{
						"type": "tool_result",
						"tool_use_id": "call_1",
						"content": [
							{"type": "text", "text": "Matched tools:"},
							{"type": "tool_reference", "tool_name": "WebSearch"},
							{"type": "tool_reference", "tool_name": "NonExistentTool"}
						]
					}
				]
			}
		]
	}`

	out, err := ConvertClaudeRequestToCodex("gpt-6-astra", []byte(inputJSON), false)
	if err != nil {
		t.Fatalf("ConvertClaudeRequestToCodex error: %v", err)
	}

	outputParts := ojson.GetBytesNoCopy(out, `input.#(type=="function_call_output").output`).Array()
	if len(outputParts) != 3 {
		t.Fatalf("expected 3 output parts in mixed tool_result, got %d: %s", len(outputParts), ojson.GetBytesNoCopy(out, `input.#(call_id=="call_1").output`).Raw)
	}
	if outputParts[0].Get("text").String() != "Matched tools:" {
		t.Fatalf("unexpected outputParts[0]: %s", outputParts[0].Raw)
	}
	if !strings.Contains(outputParts[1].Get("text").String(), "WebSearch") {
		t.Fatalf("expected WebSearch tool_reference in outputParts[1], got: %s", outputParts[1].Raw)
	}

	addTools := ojson.GetBytesNoCopy(out, `input.#(type=="additional_tools").tools`).Array()
	if len(addTools) != 1 || addTools[0].Get("name").String() != "WebSearch" {
		t.Fatalf("expected only known WebSearch in additional_tools, got: %s", ojson.GetBytesNoCopy(out, `input.#(type=="additional_tools")`).Raw)
	}
}

func TestConvertClaudeRequestToCodex_StandaloneToolReferenceAndSearchResultOrdering(t *testing.T) {
	longDeferredTool := "mcp__shared-long-server-aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee_deliver_final_subagent_handback_report"
	inputJSON := fmt.Sprintf(`{
		"model": "gpt-6-astra",
		"tools": [
			{"name": "ToolSearch", "input_schema": {"type": "object"}},
			{"name": "WebSearch", "defer_loading": true, "input_schema": {"type": "object", "properties": {"query": {"type": "string"}}}},
			{"name": "WebFetch", "defer_loading": true, "input_schema": {"type": "object", "properties": {"url": {"type": "string"}}}},
			{"name": %q, "defer_loading": true, "input_schema": {"type": "object"}}
		],
		"messages": [
			{
				"role": "user",
				"content": [
					{"type": "text", "text": "before standalone ref"},
					{"type": "tool_reference", "tool_name": "WebSearch"},
					{"type": "text", "text": "between ref and search result"},
					{
						"type": "tool_search_tool_result",
						"tool_use_id": "srvtoolu_1",
						"content": {
							"type": "tool_search_tool_search_result",
							"tool_references": [
								{"type": "tool_reference", "tool_name": "WebFetch"}
							]
						}
					},
					{"type": "text", "text": "after search result"}
				]
			},
			{
				"role": "assistant",
				"content": [
					{"type": "tool_use", "id": "call_img_ref", "name": "ToolSearch", "input": {"query": "long"}}
				]
			},
			{
				"role": "user",
				"content": [
					{
						"type": "tool_result",
						"tool_use_id": "call_img_ref",
						"content": [
							{
								"type": "image",
								"source": {"type": "base64", "media_type": "image/png", "data": "aGVsbG8="}
							},
							{"type": "tool_reference", "tool_name": %q}
						]
					}
				]
			}
		]
	}`, longDeferredTool, longDeferredTool)

	out, err := ConvertClaudeRequestToCodex("gpt-6-astra", []byte(inputJSON), false)
	if err != nil {
		t.Fatalf("ConvertClaudeRequestToCodex error: %v", err)
	}

	inputs := ojson.GetBytesNoCopy(out, "input").Array()
	// Expected input sequence:
	// 0: message ("before standalone ref")
	// 1: additional_tools (WebSearch)
	// 2: message ("between ref and search result")
	// 3: additional_tools (WebFetch)
	// 4: message ("after search result")
	// 5: function_call (call_img_ref)
	// 6: function_call_output (call_img_ref with input_image + shortened tool_reference input_text)
	// 7: additional_tools (shortened longDeferredTool)
	if len(inputs) != 8 {
		t.Fatalf("expected 8 input items, got %d: %s", len(inputs), ojson.GetBytesNoCopy(out, "input").Raw)
	}
	if inputs[0].Get("type").String() != "message" || inputs[0].Get("content.0.text").String() != "before standalone ref" {
		t.Fatalf("inputs[0] = %s, want message 'before standalone ref'", inputs[0].Raw)
	}
	if inputs[1].Get("type").String() != "additional_tools" || inputs[1].Get("tools.0.name").String() != "WebSearch" {
		t.Fatalf("inputs[1] = %s, want additional_tools(WebSearch)", inputs[1].Raw)
	}
	if inputs[2].Get("type").String() != "message" || inputs[2].Get("content.0.text").String() != "between ref and search result" {
		t.Fatalf("inputs[2] = %s, want message 'between ref and search result'", inputs[2].Raw)
	}
	if inputs[3].Get("type").String() != "additional_tools" || inputs[3].Get("tools.0.name").String() != "WebFetch" {
		t.Fatalf("inputs[3] = %s, want additional_tools(WebFetch)", inputs[3].Raw)
	}
	if inputs[4].Get("type").String() != "message" || inputs[4].Get("content.0.text").String() != "after search result" {
		t.Fatalf("inputs[4] = %s, want message 'after search result'", inputs[4].Raw)
	}
	if inputs[6].Get("type").String() != "function_call_output" {
		t.Fatalf("inputs[6] = %s, want function_call_output", inputs[6].Raw)
	}
	outArr := inputs[6].Get("output").Array()
	if len(outArr) != 2 || outArr[0].Get("type").String() != "input_image" || outArr[1].Get("type").String() != "input_text" {
		t.Fatalf("expected [input_image, input_text] in mixed tool_result output, got: %s", inputs[6].Get("output").Raw)
	}
	shortLong := inputs[7].Get("tools.0.name").String()
	if shortLong == "" || shortLong == longDeferredTool || !strings.Contains(outArr[1].Get("text").String(), shortLong) {
		t.Fatalf("expected shortened tool name %q in mixed tool_result input_text, got: %s", shortLong, outArr[1].Raw)
	}
}
