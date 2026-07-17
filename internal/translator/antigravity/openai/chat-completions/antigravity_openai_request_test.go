package chat_completions

import (
	"testing"

	"github.com/tidwall/gjson"
)

func TestConvertOpenAIRequestToAntigravitySkipsEmptyTextPartsWithoutNulls(t *testing.T) {
	inputJSON := `{
		"model": "gemini-3-flash",
		"messages": [
			{
				"role": "user",
				"content": [
					{"type": "text", "text": ""},
					{"type": "input_audio", "input_audio": {"data": "SUQzBA==", "format": "mp3"}}
				]
			},
			{
				"role": "assistant",
				"content": [{"type": "text", "text": ""}],
				"tool_calls": [{
					"id": "call_1",
					"type": "function",
					"function": {"name": "read_file", "arguments": "{\"path\":\"a.txt\"}"}
				}]
			},
			{"role": "tool", "tool_call_id": "call_1", "content": "{\"output\":\"ok\"}"},
			{"role": "user", "content": "done"}
		]
	}`

	result := ConvertOpenAIRequestToAntigravity("gemini-3-flash", []byte(inputJSON), false)
	userParts := gjson.GetBytes(result, "request.contents.0.parts").Array()
	if len(userParts) != 1 {
		t.Fatalf("user parts length = %d, want 1. Output: %s", len(userParts), result)
	}
	if userParts[0].Type == gjson.Null {
		t.Fatalf("user parts.0 is null. Output: %s", result)
	}
	if got := userParts[0].Get("inlineData.mime_type").String(); got != "audio/mpeg" {
		t.Fatalf("audio mime_type = %q, want audio/mpeg. Output: %s", got, result)
	}

	assistantParts := gjson.GetBytes(result, "request.contents.1.parts").Array()
	if len(assistantParts) != 1 {
		t.Fatalf("assistant parts length = %d, want 1. Output: %s", len(assistantParts), result)
	}
	if assistantParts[0].Type == gjson.Null {
		t.Fatalf("assistant parts.0 is null. Output: %s", result)
	}
	if !assistantParts[0].Get("functionCall").Exists() {
		t.Fatalf("functionCall missing. Output: %s", result)
	}
}

func TestConvertOpenAIRequestToAntigravityPreservesReasoningContent(t *testing.T) {
	inputJSON := `{
		"model": "gemini-3-flash",
		"messages": [
			{"role": "user", "content": "hi"},
			{"role": "assistant", "content": "", "reasoning_content": "thinking only"},
			{"role": "user", "content": "say ok"}
		]
	}`

	result := ConvertOpenAIRequestToAntigravity("gemini-3-flash", []byte(inputJSON), true)
	contents := gjson.GetBytes(result, "request.contents").Array()
	if len(contents) != 3 {
		t.Fatalf("contents length = %d, want 3. Output: %s", len(contents), result)
	}
	part := contents[1].Get("parts.0")
	if got := contents[1].Get("role").String(); got != "model" {
		t.Fatalf("contents.1.role = %q, want model. Output: %s", got, result)
	}
	if got := part.Get("text").String(); got != "thinking only" {
		t.Fatalf("reasoning text = %q, want thinking only. Output: %s", got, result)
	}
	if !part.Get("thought").Bool() {
		t.Fatalf("reasoning part should be marked as thought. Output: %s", result)
	}
	if got := part.Get("thoughtSignature").String(); got != antigravityFunctionThoughtSignature {
		t.Fatalf("thoughtSignature = %q, want bypass sentinel. Output: %s", got, result)
	}
}

func TestConvertOpenAIRequestToAntigravityPreservesReasoningBeforeVisibleContentAndToolCall(t *testing.T) {
	inputJSON := `{
		"model": "gemini-3-flash",
		"messages": [
			{"role": "user", "content": "hi"},
			{"role": "assistant", "content": "visible answer", "reasoning_content": "thinking only", "tool_calls": [{"id": "call_1", "type": "function", "function": {"name": "read_file", "arguments": "{}"}}]},
			{"role": "tool", "tool_call_id": "call_1", "content": "{\"output\":\"ok\"}"},
			{"role": "user", "content": "say ok"}
		]
	}`

	result := ConvertOpenAIRequestToAntigravity("gemini-3-flash", []byte(inputJSON), true)
	contents := gjson.GetBytes(result, "request.contents").Array()
	if len(contents) != 4 {
		t.Fatalf("contents length = %d, want 4. Output: %s", len(contents), result)
	}
	parts := contents[1].Get("parts").Array()
	if len(parts) != 3 {
		t.Fatalf("model parts length = %d, want 3. Output: %s", len(parts), result)
	}
	if got := parts[0].Get("text").String(); got != "thinking only" || !parts[0].Get("thought").Bool() {
		t.Fatalf("first part should be the reasoning thought. Output: %s", result)
	}
	if got := parts[1].Get("text").String(); got != "visible answer" || parts[1].Get("thought").Bool() {
		t.Fatalf("second part should be visible assistant content. Output: %s", result)
	}
	if got := parts[2].Get("functionCall.name").String(); got != "read_file" {
		t.Fatalf("functionCall.name = %q, want read_file. Output: %s", got, result)
	}
	if got := parts[2].Get("thoughtSignature").String(); got != antigravityFunctionThoughtSignature {
		t.Fatalf("functionCall thoughtSignature = %q, want bypass sentinel. Output: %s", got, result)
	}
	if got := contents[2].Get("parts.0.functionResponse.name").String(); got != "read_file" {
		t.Fatalf("functionResponse.name = %q, want read_file. Output: %s", got, result)
	}
}

func TestConvertOpenAIRequestToAntigravitySkipsEmptyAssistantMessages(t *testing.T) {
	inputJSON := `{
		"model": "gemini-3-flash",
		"messages": [
			{"role": "user", "content": "hi"},
			{"role": "assistant", "content": "", "tool_calls": [{"type": "function", "function": {"name": "", "arguments": "{}"}}, {"type": "custom"}]},
			{"role": "user", "content": "say ok"}
		]
	}`

	result := ConvertOpenAIRequestToAntigravity("gemini-3-flash", []byte(inputJSON), true)
	contents := gjson.GetBytes(result, "request.contents").Array()
	if len(contents) != 2 {
		t.Fatalf("contents length = %d, want 2. Output: %s", len(contents), result)
	}
}

func TestConvertOpenAIRequestToAntigravityThinkingAliases(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "Default Gemini include thoughts",
			body: `{
				"model":"gemini-3.1-pro-low",
				"messages":[{"role":"user","content":"hi"}]
			}`,
			want: true,
		},
		{
			name: "GenerationConfig snake include thoughts",
			body: `{
				"model":"gemini-3.1-pro-low",
				"messages":[{"role":"user","content":"hi"}],
				"generationConfig":{"thinkingConfig":{"include_thoughts":true}}
			}`,
			want: true,
		},
		{
			name: "Top-level thinking include thoughts",
			body: `{
				"model":"gemini-3.1-pro-low",
				"messages":[{"role":"user","content":"hi"}],
				"thinking":{"include_thoughts":true}
			}`,
			want: true,
		},
		{
			name: "Reasoning exclude false includes thoughts",
			body: `{
				"model":"gemini-3.1-pro-low",
				"messages":[{"role":"user","content":"hi"}],
				"reasoning":{"exclude":false}
			}`,
			want: true,
		},
		{
			name: "Reasoning exclude true hides thoughts",
			body: `{
				"model":"gemini-3.1-pro-low",
				"messages":[{"role":"user","content":"hi"}],
				"reasoning":{"exclude":true}
			}`,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ConvertOpenAIRequestToAntigravity("gemini-3.1-pro-low", []byte(tt.body), false)
			includeThoughts := gjson.GetBytes(result, "request.generationConfig.thinkingConfig.includeThoughts")
			if !includeThoughts.Exists() {
				t.Fatalf("includeThoughts missing. Output: %s", result)
			}
			if got := includeThoughts.Bool(); got != tt.want {
				t.Fatalf("includeThoughts = %v, want %v. Output: %s", got, tt.want, result)
			}
			if snake := gjson.GetBytes(result, "request.generationConfig.thinkingConfig.include_thoughts"); snake.Exists() {
				t.Fatalf("include_thoughts should be normalized away. Output: %s", result)
			}
		})
	}
}

func TestConvertOpenAIRequestToAntigravityDeduplicatesAndDisambiguatesTools(t *testing.T) {
	first := "mcp__plugin_cloudflare_cloudflare-builds__workers_builds_get_build"
	second := "mcp__plugin_cloudflare_cloudflare-builds__workers_builds_get_build_logs"
	inputJSON := `{
		"messages":[
			{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"` + second + `","arguments":"{}"}}]},
			{"role":"tool","tool_call_id":"call_1","content":"{}"}
		],
		"tools":[
			{"type":"function","function":{"name":"lookup","parameters":{"type":"object"}}},
			{"type":"function","function":{"name":"lookup","description":"duplicate","parameters":{"type":"object"}}},
			{"type":"function","function":{"name":"` + first + `","parameters":{"type":"object"}}},
			{"type":"function","function":{"name":"` + second + `","parameters":{"type":"object"}}}
		],
		"tool_choice":{"type":"function","function":{"name":"` + second + `"}}
	}`

	out := ConvertOpenAIRequestToAntigravity("gemini-3-flash", []byte(inputJSON), false)
	declarations := gjson.GetBytes(out, "request.tools.0.functionDeclarations").Array()
	if len(declarations) != 3 {
		t.Fatalf("declaration count = %d, want 3. Output: %s", len(declarations), out)
	}
	firstMapped := declarations[1].Get("name").String()
	secondMapped := declarations[2].Get("name").String()
	if firstMapped == secondMapped || len(secondMapped) > 64 {
		t.Fatalf("collision names = %q and %q, want distinct names <= 64 chars", firstMapped, secondMapped)
	}
	if got := gjson.GetBytes(out, "request.contents.0.parts.0.functionCall.name").String(); got != secondMapped {
		t.Fatalf("functionCall.name = %q, want %q. Output: %s", got, secondMapped, out)
	}
	if got := gjson.GetBytes(out, "request.contents.1.parts.0.functionResponse.name").String(); got != secondMapped {
		t.Fatalf("functionResponse.name = %q, want %q. Output: %s", got, secondMapped, out)
	}
	if got := gjson.GetBytes(out, "request.toolConfig.functionCallingConfig.allowedFunctionNames.0").String(); got != secondMapped {
		t.Fatalf("allowedFunctionNames.0 = %q, want %q. Output: %s", got, secondMapped, out)
	}
}

func TestConvertOpenAIRequestToAntigravityMapsToolChoiceModes(t *testing.T) {
	for _, tt := range []struct {
		choice string
		mode   string
	}{
		{choice: `"none"`, mode: "NONE"},
		{choice: `"auto"`, mode: "AUTO"},
		{choice: `"required"`, mode: "ANY"},
	} {
		t.Run(tt.mode+tt.choice, func(t *testing.T) {
			inputJSON := []byte(`{"messages":[{"role":"user","content":"hi"}],"tool_choice":` + tt.choice + `}`)
			out := ConvertOpenAIRequestToAntigravity("gemini-3-flash", inputJSON, false)
			if got := gjson.GetBytes(out, "request.toolConfig.functionCallingConfig.mode").String(); got != tt.mode {
				t.Fatalf("tool choice mode = %q, want %q. Output: %s", got, tt.mode, out)
			}
		})
	}
}
