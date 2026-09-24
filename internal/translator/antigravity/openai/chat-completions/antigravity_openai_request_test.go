package chat_completions

import (
	"regexp"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/signature"
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

func TestConvertOpenAIRequestToAntigravity_ClaudeModelSanitizesUnsignedReasoningContent(t *testing.T) {
	inputJSON := `{
		"model": "claude-sonnet-4-6",
		"messages": [
			{"role": "user", "content": "hi"},
			{"role": "assistant", "content": "visible text", "reasoning_content": "unsigned reasoning"},
			{"role": "user", "content": "say ok"}
		]
	}`

	result := ConvertOpenAIRequestToAntigravity("claude-sonnet-4-6", []byte(inputJSON), false)
	contents := gjson.GetBytes(result, "request.contents").Array()
	if len(contents) != 3 {
		t.Fatalf("contents length = %d, want 3. Output: %s", len(contents), result)
	}
	parts := contents[1].Get("parts").Array()
	if len(parts) != 1 {
		t.Fatalf("model parts length = %d, want 1 (thinking part dropped). Output: %s", len(parts), result)
	}
	if got := parts[0].Get("text").String(); got != "visible text" {
		t.Fatalf("parts[0].text = %q, want visible text. Output: %s", got, result)
	}
	if parts[0].Get("thought").Exists() {
		t.Fatalf("parts[0] should not be thought part. Output: %s", result)
	}
}

func TestConvertOpenAIRequestToAntigravity_ClaudeModelDropsEmptyAssistantTurnAfterSanitizingReasoningContent(t *testing.T) {
	inputJSON := `{
		"model": "claude-sonnet-4-6",
		"messages": [
			{"role": "user", "content": "hi"},
			{"role": "assistant", "content": "", "reasoning_content": "unsigned reasoning"},
			{"role": "user", "content": "say ok"}
		]
	}`

	result := ConvertOpenAIRequestToAntigravity("claude-sonnet-4-6", []byte(inputJSON), false)
	contents := gjson.GetBytes(result, "request.contents").Array()
	if len(contents) != 2 {
		t.Fatalf("contents length = %d, want 2 (empty model turn dropped). Output: %s", len(contents), result)
	}
	if got := contents[0].Get("role").String(); got != "user" {
		t.Fatalf("contents[0].role = %q, want user. Output: %s", got, result)
	}
	if got := contents[1].Get("role").String(); got != "user" {
		t.Fatalf("contents[1].role = %q, want user. Output: %s", got, result)
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
	if part.Get("thoughtSignature").Exists() {
		t.Fatalf("reasoning part should not synthesize thoughtSignature; output=%s", result)
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
	if parts[0].Get("thoughtSignature").Exists() {
		t.Fatalf("first part should not synthesize thoughtSignature. Output: %s", result)
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

func TestConvertOpenAIRequestToAntigravity_MidSessionDeveloperMessageDoesNotMutateSystemInstruction(t *testing.T) {
	inputJSON := `{
		"model": "gemini-3-flash",
		"messages": [
			{"role": "system", "content": "You are a helpful assistant"},
			{"role": "user", "content": "Turn 1 user"},
			{"role": "assistant", "content": "Turn 1 assistant"},
			{"role": "developer", "content": "<image_resize_notice>Image 1 was resized to 800x600</image_resize_notice>"},
			{"role": "user", "content": "Turn 2 user"}
		]
	}`

	result := ConvertOpenAIRequestToAntigravity("gemini-3-flash", []byte(inputJSON), false)
	output := gjson.ParseBytes(result)

	// request.systemInstruction must contain only original system prompt
	sysParts := output.Get("request.systemInstruction.parts").Array()
	if len(sysParts) != 1 {
		t.Fatalf("request.systemInstruction parts = %d, want 1. Output: %s", len(sysParts), result)
	}
	if got := sysParts[0].Get("text").String(); got != "You are a helpful assistant" {
		t.Fatalf("systemInstruction text = %q, want %q", got, "You are a helpful assistant")
	}

	// contents must contain user, model, user (demoted dev message), user
	contents := output.Get("request.contents").Array()
	if len(contents) != 4 {
		t.Fatalf("contents length = %d, want 4. Output: %s", len(contents), result)
	}
	if contents[0].Get("role").String() != "user" || contents[0].Get("parts.0.text").String() != "Turn 1 user" {
		t.Fatalf("turn 0 mismatch: %s", contents[0].Raw)
	}
	if contents[1].Get("role").String() != "model" || contents[1].Get("parts.0.text").String() != "Turn 1 assistant" {
		t.Fatalf("turn 1 mismatch: %s", contents[1].Raw)
	}
	expectedDevText := "<system-reminder>\n<image_resize_notice>Image 1 was resized to 800x600</image_resize_notice>\n</system-reminder>"
	if contents[2].Get("role").String() != "user" || contents[2].Get("parts.0.text").String() != expectedDevText {
		t.Fatalf("turn 2 mismatch: %s", contents[2].Raw)
	}
	if contents[3].Get("role").String() != "user" || contents[3].Get("parts.0.text").String() != "Turn 2 user" {
		t.Fatalf("turn 3 mismatch: %s", contents[3].Raw)
	}
}

func TestConvertOpenAIRequestToAntigravity_MidSessionSystemReminderEnvelope(t *testing.T) {
	inputJSON := `{
		"model": "gemini-3-flash",
		"messages": [
			{"role": "system", "content": "You are a helpful assistant"},
			{"role": "user", "content": "Hello"},
			{"role": "assistant", "content": "Hi there"},
			{"role": "system", "content": "Please decide which tool to call next."},
			{"role": "user", "content": "Search for news"}
		]
	}`

	result := ConvertOpenAIRequestToAntigravity("gemini-3-flash", []byte(inputJSON), false)
	output := gjson.ParseBytes(result)

	contents := output.Get("request.contents").Array()
	if len(contents) != 4 {
		t.Fatalf("contents length = %d, want 4. Output: %s", len(contents), result)
	}
	expectedReminder := "<system-reminder>\nPlease decide which tool to call next.\n</system-reminder>"
	if got := contents[2].Get("parts.0.text").String(); got != expectedReminder {
		t.Fatalf("mid-session system reminder mismatch:\ngot:  %q\nwant: %q", got, expectedReminder)
	}
}

func TestConvertOpenAIRequestToAntigravity_MidSessionTransientSystemInstructionPreservesTurnBoundaries(t *testing.T) {
	turnWithTransient := `{
		"model": "gemini-3-flash",
		"messages": [
			{"role": "system", "content": "System prompt"},
			{"role": "user", "content": "Turn 1 user"},
			{"role": "assistant", "content": "Turn 1 assistant"},
			{"role": "system", "content": "Call tool now"},
			{"role": "user", "content": "Turn 2 user"}
		]
	}`

	turnWithoutTransient := `{
		"model": "gemini-3-flash",
		"messages": [
			{"role": "system", "content": "System prompt"},
			{"role": "user", "content": "Turn 1 user"},
			{"role": "assistant", "content": "Turn 1 assistant"},
			{"role": "user", "content": "Turn 2 user"},
			{"role": "assistant", "content": "Turn 2 assistant"}
		]
	}`

	outWith := ConvertOpenAIRequestToAntigravity("gemini-3-flash", []byte(turnWithTransient), false)
	outWithout := ConvertOpenAIRequestToAntigravity("gemini-3-flash", []byte(turnWithoutTransient), false)

	contentsWith := gjson.GetBytes(outWith, "request.contents").Array()
	contentsWithout := gjson.GetBytes(outWithout, "request.contents").Array()

	// Ensure demoted system instruction is standalone and not merged into adjacent user turn
	if len(contentsWith) != 4 {
		t.Fatalf("expected 4 standalone content items in request with transient instruction, got %d", len(contentsWith))
	}
	expectedReminder := "<system-reminder>\nCall tool now\n</system-reminder>"
	if contentsWith[2].Get("role").String() != "user" || contentsWith[2].Get("parts.0.text").String() != expectedReminder {
		t.Fatalf("turn 2 mismatch: %s", contentsWith[2].Raw)
	}
	if contentsWith[3].Get("role").String() != "user" || contentsWith[3].Get("parts.0.text").String() != "Turn 2 user" {
		t.Fatalf("turn 3 mismatch: %s", contentsWith[3].Raw)
	}

	// Prior turn history entries (Turn 1 user, Turn 1 assistant) are byte-identical
	if contentsWith[0].Raw != contentsWithout[0].Raw {
		t.Fatalf("turn 0 diverged: %s vs %s", contentsWith[0].Raw, contentsWithout[0].Raw)
	}
	if contentsWith[1].Raw != contentsWithout[1].Raw {
		t.Fatalf("turn 1 diverged: %s vs %s", contentsWith[1].Raw, contentsWithout[1].Raw)
	}
	// Turn 2 user text is also identical between turns because it was not merged
	if contentsWith[3].Get("parts.0.text").String() != contentsWithout[2].Get("parts.0.text").String() {
		t.Fatalf("turn 2 user text diverged due to merging: %s vs %s", contentsWith[3].Raw, contentsWithout[2].Raw)
	}
}

func TestConvertOpenAIRequestToAntigravity_MidSessionSystemReminderObjectAndArrayContent(t *testing.T) {
	inputJSON := `{
		"model": "gemini-3-flash",
		"messages": [
			{"role": "user", "content": "Hello"},
			{"role": "assistant", "content": "Hi"},
			{"role": "system", "content": {"type": "text", "text": "Object instruction"}},
			{"role": "developer", "content": [{"type": "text", "text": "Array instruction"}]}
		]
	}`

	result := ConvertOpenAIRequestToAntigravity("gemini-3-flash", []byte(inputJSON), false)
	output := gjson.ParseBytes(result)

	contents := output.Get("request.contents").Array()
	if len(contents) != 4 {
		t.Fatalf("contents length = %d, want 4. Output: %s", len(contents), result)
	}
	expectedObject := "<system-reminder>\nObject instruction\n</system-reminder>"
	if got := contents[2].Get("parts.0.text").String(); got != expectedObject {
		t.Fatalf("object instruction mismatch:\ngot:  %q\nwant: %q", got, expectedObject)
	}
	expectedArray := "<system-reminder>\nArray instruction\n</system-reminder>"
	if got := contents[3].Get("parts.0.text").String(); got != expectedArray {
		t.Fatalf("array instruction mismatch:\ngot:  %q\nwant: %q", got, expectedArray)
	}
}

func TestConvertOpenAIRequestToAntigravityThinkingAliases(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		wantExists bool
		want       bool
	}{
		{
			name: "Missing summary intent leaves include thoughts absent",
			body: `{
				"model":"gemini-3.1-pro-low",
				"messages":[{"role":"user","content":"hi"}]
			}`,
		},
		{
			name: "Reasoning effort enables thoughts",
			body: `{
				"model":"gemini-3.1-pro-low",
				"messages":[{"role":"user","content":"hi"}],
				"reasoning_effort":"high"
			}`,
			wantExists: true,
			want:       true,
		},
		{
			name: "GenerationConfig snake include thoughts",
			body: `{
				"model":"gemini-3.1-pro-low",
				"messages":[{"role":"user","content":"hi"}],
				"generationConfig":{"thinkingConfig":{"include_thoughts":true}}
			}`,
			wantExists: true,
			want:       true,
		},
		{
			name: "String include thoughts is ignored",
			body: `{
				"model":"gemini-3.1-pro-low",
				"messages":[{"role":"user","content":"hi"}],
				"generationConfig":{"thinkingConfig":{"includeThoughts":"true"}}
			}`,
		},
		{
			name: "Top-level thinking include thoughts",
			body: `{
				"model":"gemini-3.1-pro-low",
				"messages":[{"role":"user","content":"hi"}],
				"thinking":{"include_thoughts":true}
			}`,
			wantExists: true,
			want:       true,
		},
		{
			name: "Reasoning exclude false includes thoughts",
			body: `{
				"model":"gemini-3.1-pro-low",
				"messages":[{"role":"user","content":"hi"}],
				"reasoning":{"exclude":false}
			}`,
			wantExists: true,
			want:       true,
		},
		{
			name: "Reasoning exclude true hides thoughts",
			body: `{
				"model":"gemini-3.1-pro-low",
				"messages":[{"role":"user","content":"hi"}],
				"reasoning":{"exclude":true}
			}`,
			wantExists: true,
			want:       false,
		},
		{
			name: "Google extension disables thoughts",
			body: `{
				"model":"gemini-3.1-pro-low",
				"messages":[{"role":"user","content":"hi"}],
				"reasoning_effort":"high",
				"extra_body":{"google":{"thinking_config":{"include_thoughts":false}}}
			}`,
			wantExists: true,
			want:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ConvertOpenAIRequestToAntigravity("gemini-3.1-pro-low", []byte(tt.body), false)
			includeThoughts := gjson.GetBytes(result, "request.generationConfig.thinkingConfig.includeThoughts")
			if includeThoughts.Exists() != tt.wantExists {
				t.Fatalf("includeThoughts exists = %v, want %v. Output: %s", includeThoughts.Exists(), tt.wantExists, result)
			}
			if tt.wantExists {
				if got := includeThoughts.Bool(); got != tt.want {
					t.Fatalf("includeThoughts = %v, want %v. Output: %s", got, tt.want, result)
				}
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

func TestConvertOpenAIRequestToAntigravityMapsResponseFormatJSONObject(t *testing.T) {
	inputJSON := []byte(`{
		"model":"gemini-3.6-flash-high",
		"messages":[{"role":"user","content":"hi"}],
		"generationConfig":{
			"responseSchema":{"type":"string","description":"stale"},
			"responseJsonSchema":{"type":"string"},
			"response_schema":{"type":"string"},
			"response_json_schema":{"type":"string"}
		},
		"response_format":{"type":"json_object"}
	}`)

	out := ConvertOpenAIRequestToAntigravity("gemini-3.6-flash-high", inputJSON, false)
	if got := gjson.GetBytes(out, "request.generationConfig.responseMimeType").String(); got != "application/json" {
		t.Fatalf("responseMimeType = %q, want application/json. Output: %s", got, out)
	}
	if gjson.GetBytes(out, "request.generationConfig.responseSchema").Exists() {
		t.Fatalf("responseSchema should not be set for json_object. Output: %s", out)
	}
	assertNoResponseSchemaAliases(t, out)
}

func TestConvertOpenAIRequestToAntigravityMapsResponseFormatJSONSchema(t *testing.T) {
	inputJSON := []byte(`{
		"model":"gemini-3.6-flash-high",
		"messages":[{"role":"user","content":"hi"}],
		"generationConfig":{
			"responseSchema":{"type":"string","description":"stale"},
			"responseJsonSchema":{"type":"string"},
			"response_schema":{"type":"string"},
			"response_json_schema":{"type":"string"}
		},
		"response_format":{
			"type":"json_schema",
			"json_schema":{
				"name":"verdict",
				"schema":{
					"type":"object",
					"properties":{"score":{"type":"integer"}},
					"required":["score"]
				}
			}
		}
	}`)

	out := ConvertOpenAIRequestToAntigravity("gemini-3.6-flash-high", inputJSON, false)
	if got := gjson.GetBytes(out, "request.generationConfig.responseMimeType").String(); got != "application/json" {
		t.Fatalf("responseMimeType = %q, want application/json. Output: %s", got, out)
	}
	schema := gjson.GetBytes(out, "request.generationConfig.responseSchema")
	if !schema.Exists() {
		t.Fatalf("responseSchema missing. Output: %s", out)
	}
	if got := schema.Get("properties.score.type").String(); got != "integer" {
		t.Fatalf("responseSchema.properties.score.type = %q, want integer. Output: %s", got, out)
	}
	if schema.Get("description").Exists() {
		t.Fatalf("stale responseSchema survived. Output: %s", out)
	}
	assertNoResponseSchemaAliases(t, out)
}

func assertNoResponseSchemaAliases(t *testing.T, out []byte) {
	t.Helper()
	for _, schemaKey := range []string{"responseJsonSchema", "response_schema", "response_json_schema"} {
		if gjson.GetBytes(out, "request.generationConfig."+schemaKey).Exists() {
			t.Errorf("stale %s survived response_format mapping. Output: %s", schemaKey, out)
		}
	}
}

func TestConvertOpenAIRequestToAntigravityTranslatesVideoURL(t *testing.T) {
	inputJSON := []byte(`{
		"model": "gemini-3.7-flash-high",
		"messages": [{
			"role": "user",
			"content": [
				{"type": "text", "text": "Name the colours in order"},
				{"type": "video_url", "video_url": {"url": "data:video/mp4;base64,AAAAIGZ0eXBtcDQy"}}
			]
		}]
	}`)

	out := ConvertOpenAIRequestToAntigravity("gemini-3.7-flash-high", inputJSON, false)
	parts := gjson.GetBytes(out, "request.contents.0.parts").Array()
	if len(parts) != 2 {
		t.Fatalf("parts length = %d, want 2. Output: %s", len(parts), out)
	}

	if got := parts[0].Get("text").String(); got != "Name the colours in order" {
		t.Fatalf("parts[0].text = %q, want 'Name the colours in order'", got)
	}

	inlineData := parts[1].Get("inlineData")
	if !inlineData.Exists() {
		t.Fatalf("parts[1].inlineData missing. Output: %s", out)
	}
	if got := inlineData.Get("mimeType").String(); got != "video/mp4" {
		t.Fatalf("inlineData.mimeType = %q, want video/mp4. Output: %s", got, out)
	}
	if got := inlineData.Get("data").String(); got != "AAAAIGZ0eXBtcDQy" {
		t.Fatalf("inlineData.data = %q, want AAAAIGZ0eXBtcDQy. Output: %s", got, out)
	}
}

func TestConvertOpenAIRequestToAntigravity_MaxCompletionTokens(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		expected float64
	}{
		{
			name:     "only max_tokens",
			body:     `{"model":"gemini-2.5-flash","messages":[{"role":"user","content":"hi"}],"max_tokens":100}`,
			expected: 100,
		},
		{
			name:     "only max_completion_tokens",
			body:     `{"model":"gemini-2.5-flash","messages":[{"role":"user","content":"hi"}],"max_completion_tokens":200}`,
			expected: 200,
		},
		{
			name:     "max_tokens preferred over max_completion_tokens",
			body:     `{"model":"gemini-2.5-flash","messages":[{"role":"user","content":"hi"}],"max_tokens":100,"max_completion_tokens":200}`,
			expected: 100,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := ConvertOpenAIRequestToAntigravity("gemini-2.5-flash", []byte(tt.body), false)
			got := gjson.GetBytes(out, "request.generationConfig.maxOutputTokens")
			if !got.Exists() {
				t.Fatalf("request.generationConfig.maxOutputTokens missing. Output: %s", out)
			}
			if got.Float() != tt.expected {
				t.Fatalf("maxOutputTokens = %v, want %v. Output: %s", got.Float(), tt.expected, out)
			}
		})
	}
}

func TestConvertOpenAIRequestToAntigravityPreservesToolResponseAsString(t *testing.T) {
	inputJSON := `{
		"model": "gemini-3-flash",
		"messages": [
			{
				"role": "user",
				"content": "read file"
			},
			{
				"role": "assistant",
				"tool_calls": [{
					"id": "call_1",
					"type": "function",
					"function": {"name": "read_file", "arguments": "{\"path\":\"config.json\"}"}
				}]
			},
			{
				"role": "tool",
				"tool_call_id": "call_1",
				"content": "{\"key\":\"value\",\"items\":[1,2,3]}"
			}
		]
	}`

	result := ConvertOpenAIRequestToAntigravity("gemini-3-flash", []byte(inputJSON), false)
	contents := gjson.GetBytes(result, "request.contents").Array()
	if len(contents) < 3 {
		t.Fatalf("expected at least 3 contents, got %d. Output: %s", len(contents), result)
	}
	frResult := contents[2].Get("parts.0.functionResponse.response.result")
	if frResult.Type != gjson.String {
		t.Fatalf("expected functionResponse.response.result to be string, got type %s (raw: %s)", frResult.Type, frResult.Raw)
	}
	expected := `{"key":"value","items":[1,2,3]}`
	if got := frResult.String(); got != expected {
		t.Fatalf("expected %q, got %q", expected, got)
	}
}

func TestConvertOpenAIRequestToAntigravityToolChoiceNoneOmitsTools(t *testing.T) {
	for _, tc := range []struct {
		name       string
		toolChoice string
	}{
		{name: "string none", toolChoice: `"none"`},
		{name: "object none", toolChoice: `{"type":"none"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inputJSON := []byte(`{
				"model":"gemini-3-flash",
				"messages":[{"role":"user","content":"hi"}],
				"tools":[{"type":"function","function":{"name":"get_weather","parameters":{"type":"object"}}}],
				"tool_choice":` + tc.toolChoice + `
			}`)
			out := ConvertOpenAIRequestToAntigravity("gemini-3-flash", inputJSON, false)
			if got := gjson.GetBytes(out, "request.toolConfig.functionCallingConfig.mode").String(); got != "NONE" {
				t.Fatalf("expected mode NONE, got %q", got)
			}
			if gjson.GetBytes(out, "request.tools").Exists() {
				t.Fatalf("expected request.tools to be omitted, got %s", gjson.GetBytes(out, "request.tools").Raw)
			}
		})
	}
}

func TestConvertOpenAIRequestToAntigravity_MultiTurnRepeatedToolCallID_Issue5933(t *testing.T) {
	inputJSON := `{
		"model": "gemini-3-flash",
		"messages": [
			{"role": "user", "content": "list files"},
			{
				"role": "assistant",
				"tool_calls": [{
					"id": "call_1",
					"type": "function",
					"function": {"name": "glob", "arguments": "{\"pattern\":\"*.go\"}"}
				}]
			},
			{"role": "tool", "tool_call_id": "call_1", "content": "[\"main.go\"]"},
			{"role": "user", "content": "read main.go"},
			{
				"role": "assistant",
				"tool_calls": [{
					"id": "call_1",
					"type": "function",
					"function": {"name": "read", "arguments": "{\"path\":\"main.go\"}"}
				}]
			},
			{"role": "tool", "tool_call_id": "call_1", "content": "package main"}
		]
	}`

	out := ConvertOpenAIRequestToAntigravity("gemini-3-flash", []byte(inputJSON), false)

	// In Turn 1 (contents[1] = model functionCall, contents[2] = user functionResponse):
	// functionCall.name must be "glob", and functionResponse.name must be "glob".
	call1Name := gjson.GetBytes(out, "request.contents.1.parts.0.functionCall.name").String()
	resp1Name := gjson.GetBytes(out, "request.contents.2.parts.0.functionResponse.name").String()
	resp1Result := gjson.GetBytes(out, "request.contents.2.parts.0.functionResponse.response.result").String()

	if call1Name != "glob" {
		t.Fatalf("turn 1 functionCall.name = %q, want glob", call1Name)
	}
	if resp1Name != "glob" {
		t.Fatalf("turn 1 functionResponse.name = %q, want glob (got overwritten by subsequent turn)", resp1Name)
	}
	if resp1Result != "[\"main.go\"]" {
		t.Fatalf("turn 1 functionResponse result = %q, want [\"main.go\"]", resp1Result)
	}

	// In Turn 2 (contents[4] = model functionCall, contents[5] = user functionResponse):
	// functionCall.name must be "read", and functionResponse.name must be "read".
	call2Name := gjson.GetBytes(out, "request.contents.4.parts.0.functionCall.name").String()
	resp2Name := gjson.GetBytes(out, "request.contents.5.parts.0.functionResponse.name").String()
	resp2Result := gjson.GetBytes(out, "request.contents.5.parts.0.functionResponse.response.result").String()

	if call2Name != "read" {
		t.Fatalf("turn 2 functionCall.name = %q, want read", call2Name)
	}
	if resp2Name != "read" {
		t.Fatalf("turn 2 functionResponse.name = %q, want read", resp2Name)
	}
	if resp2Result != "package main" {
		t.Fatalf("turn 2 functionResponse result = %q, want package main", resp2Result)
	}

	// Verify pairing validator passes without error
	if errPairing := signature.ValidateGeminiFunctionCallPairing(out); errPairing != nil {
		t.Fatalf("ValidateGeminiFunctionCallPairing failed on Antigravity output: %v; output=%s", errPairing, out)
	}
}

func TestConvertOpenAIRequestToAntigravity_ParallelAndOutOfOrderToolResponses(t *testing.T) {
	inputJSON := `{
		"model": "gemini-3-flash",
		"messages": [
			{"role": "user", "content": "run parallel tools"},
			{
				"role": "assistant",
				"tool_calls": [
					{"id": "call_1", "type": "function", "function": {"name": "tool_a", "arguments": "{}"}},
					{"id": "call_2", "type": "function", "function": {"name": "tool_b", "arguments": "{}"}}
				]
			},
			{"role": "tool", "tool_call_id": "call_2", "content": "res_b"},
			{"role": "tool", "tool_call_id": "call_1", "content": "res_a"}
		]
	}`

	out := ConvertOpenAIRequestToAntigravity("gemini-3-flash", []byte(inputJSON), false)

	resp0Name := gjson.GetBytes(out, "request.contents.2.parts.0.functionResponse.name").String()
	resp0Result := gjson.GetBytes(out, "request.contents.2.parts.0.functionResponse.response.result").String()
	resp1Name := gjson.GetBytes(out, "request.contents.2.parts.1.functionResponse.name").String()
	resp1Result := gjson.GetBytes(out, "request.contents.2.parts.1.functionResponse.response.result").String()

	if resp0Name != "tool_a" || resp0Result != "res_a" {
		t.Fatalf("part 0 want tool_a / res_a, got %s / %s", resp0Name, resp0Result)
	}
	if resp1Name != "tool_b" || resp1Result != "res_b" {
		t.Fatalf("part 1 want tool_b / res_b, got %s / %s", resp1Name, resp1Result)
	}

	if errPairing := signature.ValidateGeminiFunctionCallPairing(out); errPairing != nil {
		t.Fatalf("ValidateGeminiFunctionCallPairing failed: %v; output=%s", errPairing, out)
	}
}

func TestConvertOpenAIRequestToAntigravity_SanitizesClaudeToolIDs(t *testing.T) {
	inputJSON := `{
		"model": "claude-sonnet-4-6",
		"messages": [
			{"role": "user", "content": "hello"},
			{
				"role": "assistant",
				"tool_calls": [
					{
						"id": "call_test|fc_test",
						"type": "function",
						"function": {"name": "example_tool", "arguments": "{\"arg\":\"val\"}"}
					},
					{
						"id": "call_valid-123_456",
						"type": "function",
						"function": {"name": "valid_tool", "arguments": "{}"}
					}
				]
			},
			{"role": "tool", "tool_call_id": "call_test|fc_test", "content": "{\"result\":\"ok\"}"},
			{"role": "tool", "tool_call_id": "call_valid-123_456", "content": "{\"result\":\"valid_ok\"}"}
		]
	}`

	out := ConvertOpenAIRequestToAntigravity("claude-sonnet-4-6", []byte(inputJSON), false)

	claudePattern := regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

	fc0ID := gjson.GetBytes(out, "request.contents.1.parts.0.functionCall.id").String()
	fc1ID := gjson.GetBytes(out, "request.contents.1.parts.1.functionCall.id").String()
	fr0ID := gjson.GetBytes(out, "request.contents.2.parts.0.functionResponse.id").String()
	fr1ID := gjson.GetBytes(out, "request.contents.2.parts.1.functionResponse.id").String()

	if !claudePattern.MatchString(fc0ID) {
		t.Fatalf("functionCall 0 id %q does not match Claude pattern ^[a-zA-Z0-9_-]+$", fc0ID)
	}
	if want := "call_test_fc_test"; fc0ID != want {
		t.Fatalf("functionCall 0 id = %q, want %q", fc0ID, want)
	}
	if fr0ID != fc0ID {
		t.Fatalf("functionResponse 0 id = %q, want matching functionCall id %q", fr0ID, fc0ID)
	}

	fr0Result := gjson.GetBytes(out, "request.contents.2.parts.0.functionResponse.response.result").String()
	if want := `{"result":"ok"}`; fr0Result != want {
		t.Fatalf("functionResponse 0 result = %q, want %q", fr0Result, want)
	}

	if !claudePattern.MatchString(fc1ID) {
		t.Fatalf("functionCall 1 id %q does not match Claude pattern ^[a-zA-Z0-9_-]+$", fc1ID)
	}
	if want := "call_valid-123_456"; fc1ID != want {
		t.Fatalf("functionCall 1 id = %q, want %q", fc1ID, want)
	}
	if fr1ID != fc1ID {
		t.Fatalf("functionResponse 1 id = %q, want matching functionCall id %q", fr1ID, fc1ID)
	}

	fr1Result := gjson.GetBytes(out, "request.contents.2.parts.1.functionResponse.response.result").String()
	if want := `{"result":"valid_ok"}`; fr1Result != want {
		t.Fatalf("functionResponse 1 result = %q, want %q", fr1Result, want)
	}
}

func TestConvertOpenAIRequestToAntigravity_SanitizesClaudeToolIDs_DisambiguatesCollisionsAndReversedResponses(t *testing.T) {
	inputJSON := `{
		"model": "claude-sonnet-4-6",
		"messages": [
			{"role": "user", "content": "run multiple tools"},
			{
				"role": "assistant",
				"tool_calls": [
					{
						"id": "call_a|b",
						"type": "function",
						"function": {"name": "tool_first", "arguments": "{\"n\":1}"}
					},
					{
						"id": "call_a_b",
						"type": "function",
						"function": {"name": "tool_second", "arguments": "{\"n\":2}"}
					}
				]
			},
			{"role": "tool", "tool_call_id": "call_a_b", "content": "result_for_a_b"},
			{"role": "tool", "tool_call_id": "call_a|b", "content": "result_for_a_pipe_b"}
		]
	}`

	out := ConvertOpenAIRequestToAntigravity("claude-sonnet-4-6", []byte(inputJSON), false)

	claudePattern := regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

	fc0ID := gjson.GetBytes(out, "request.contents.1.parts.0.functionCall.id").String()
	fc1ID := gjson.GetBytes(out, "request.contents.1.parts.1.functionCall.id").String()
	fr0ID := gjson.GetBytes(out, "request.contents.2.parts.0.functionResponse.id").String()
	fr1ID := gjson.GetBytes(out, "request.contents.2.parts.1.functionResponse.id").String()

	if !claudePattern.MatchString(fc0ID) {
		t.Fatalf("functionCall 0 id %q does not match Claude pattern", fc0ID)
	}
	if !claudePattern.MatchString(fc1ID) {
		t.Fatalf("functionCall 1 id %q does not match Claude pattern", fc1ID)
	}
	if fc0ID == fc1ID {
		t.Fatalf("functionCall IDs collided: fc0=%q, fc1=%q", fc0ID, fc1ID)
	}

	if fr0ID != fc0ID {
		t.Fatalf("functionResponse 0 id = %q, want matching functionCall id %q", fr0ID, fc0ID)
	}
	if fr1ID != fc1ID {
		t.Fatalf("functionResponse 1 id = %q, want matching functionCall id %q", fr1ID, fc1ID)
	}

	// Verify that results did not get swapped or overwritten despite reverse response order and raw ID collision
	fr0Result := gjson.GetBytes(out, "request.contents.2.parts.0.functionResponse.response.result").String()
	if want := "result_for_a_pipe_b"; fr0Result != want {
		t.Fatalf("functionResponse 0 result = %q, want %q", fr0Result, want)
	}

	fr1Result := gjson.GetBytes(out, "request.contents.2.parts.1.functionResponse.response.result").String()
	if want := "result_for_a_b"; fr1Result != want {
		t.Fatalf("functionResponse 1 result = %q, want %q", fr1Result, want)
	}

	if errPairing := signature.ValidateGeminiFunctionCallPairing(out); errPairing != nil {
		t.Fatalf("ValidateGeminiFunctionCallPairing failed: %v; output=%s", errPairing, out)
	}
}

func TestConvertOpenAIRequestToAntigravity_SanitizesClaudeToolIDs_MissingResponseDoesNotStealCollidingID(t *testing.T) {
	inputJSON := `{
		"model": "claude-sonnet-4-6",
		"messages": [
			{"role": "user", "content": "run multiple tools"},
			{
				"role": "assistant",
				"tool_calls": [
					{
						"id": "call_a|b",
						"type": "function",
						"function": {"name": "tool_first", "arguments": "{\"n\":1}"}
					},
					{
						"id": "call_a_b",
						"type": "function",
						"function": {"name": "tool_second", "arguments": "{\"n\":2}"}
					}
				]
			},
			{"role": "tool", "tool_call_id": "call_a_b", "content": "result_for_a_b"}
		]
	}`

	out := ConvertOpenAIRequestToAntigravity("claude-sonnet-4-6", []byte(inputJSON), false)

	claudePattern := regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

	fc0ID := gjson.GetBytes(out, "request.contents.1.parts.0.functionCall.id").String()
	fc1ID := gjson.GetBytes(out, "request.contents.1.parts.1.functionCall.id").String()
	fr0ID := gjson.GetBytes(out, "request.contents.2.parts.0.functionResponse.id").String()
	fr1ID := gjson.GetBytes(out, "request.contents.2.parts.1.functionResponse.id").String()

	if !claudePattern.MatchString(fc0ID) || !claudePattern.MatchString(fc1ID) {
		t.Fatalf("functionCall ids do not match Claude pattern: fc0=%q, fc1=%q", fc0ID, fc1ID)
	}
	if fc0ID == fc1ID {
		t.Fatalf("functionCall IDs collided: fc0=%q, fc1=%q", fc0ID, fc1ID)
	}
	if fr0ID != fc0ID || fr1ID != fc1ID {
		t.Fatalf("functionResponse ids must match functionCall ids: fr0=%q, fc0=%q, fr1=%q, fc1=%q", fr0ID, fc0ID, fr1ID, fc1ID)
	}

	// First call has missing response, should fallback to "{}" and NOT steal second call's result
	fr0Result := gjson.GetBytes(out, "request.contents.2.parts.0.functionResponse.response.result").String()
	if want := "{}"; fr0Result != want {
		t.Fatalf("functionResponse 0 result = %q, want %q (stole colliding call's result)", fr0Result, want)
	}

	fr1Result := gjson.GetBytes(out, "request.contents.2.parts.1.functionResponse.response.result").String()
	if want := "result_for_a_b"; fr1Result != want {
		t.Fatalf("functionResponse 1 result = %q, want %q", fr1Result, want)
	}

	if errPairing := signature.ValidateGeminiFunctionCallPairing(out); errPairing != nil {
		t.Fatalf("ValidateGeminiFunctionCallPairing failed: %v; output=%s", errPairing, out)
	}
}
