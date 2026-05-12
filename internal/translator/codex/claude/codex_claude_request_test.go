package claude

import (
	"encoding/base64"
	"strings"
	"testing"

	"github.com/tidwall/gjson"
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
